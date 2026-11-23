package mobflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"pocketlab/internal/device"
	"pocketlab/internal/events"
)

// Errors of the run service.
var (
	// ErrDeviceBusy: another flow run is using the device (HTTP 409).
	ErrDeviceBusy = errors.New("device busy")
	// ErrNotImplemented marks run modes that a later slice provides (HTTP 501).
	ErrNotImplemented = errors.New("not implemented")
)

// AgentExternal is the agent name of a run that an outside agent (for example
// Claude Code with the Pocketlab MCP server) drives through the workflow_*
// tools; the service launches nothing.
const AgentExternal = "external"

// AgentRequest is what the service asks an AgentLauncher to start.
type AgentRequest struct {
	Agent        string // configured agent name; "" = the default of the workflow slot
	DeviceID     string
	SystemPrompt string
	Prompt       string
	// OnLine receives the agent's output lines (stream is "stdout" or "stderr").
	OnLine func(stream, line string)
	// OnDone must be called exactly once after the agent process exited:
	// nil on a clean exit, context.Canceled after a cancel, else the failure.
	OnDone func(err error)
}

// AgentLauncher starts and cancels agent processes (aiconfig.Manager in the
// server, a fake in tests).
type AgentLauncher interface {
	Start(req AgentRequest) (agentRunID string, err error)
	Cancel(agentRunID string)
}

// ScreenshotFunc captures a PNG from a device.
type ScreenshotFunc func(ctx context.Context, deviceID string) ([]byte, error)

// Service is the flows feature: the store, run orchestration, the state
// machine and event streaming. The zero value of the optional fields is valid.
type Service struct {
	Store *Store

	// Devices resolves a device id; it provides the run platform and the
	// default ScreenshotFunc. Optional (tests, headless use).
	Devices func(id string) (device.Device, error)
	// ScreenshotFunc overrides the per-step screenshot capture. When nil the
	// device from Devices is used; with neither, no screenshots are taken.
	ScreenshotFunc ScreenshotFunc
	// Launcher starts workflow agents for record runs. Nil means only
	// external agents (agent "external") are supported.
	Launcher AgentLauncher
	// Secrets resolves a secret id to its plaintext: replay uses it for
	// type_secret, and submitted caches are checked for literal values.
	// Optional.
	Secrets func(id string) (string, error)
	// HealTimeout bounds one self-heal agent run (default 10 minutes).
	HealTimeout time.Duration

	once  sync.Once
	wg    sync.WaitGroup // replay goroutines; see Close
	rt    *runtime
	rtErr error
}

// NewService opens a service rooted at root (see DefaultRoot).
func NewService(root string) (*Service, error) {
	st, err := NewStore(root)
	if err != nil {
		return nil, err
	}
	return &Service{Store: st}, nil
}

// Close stops the replay goroutines of all live runs and waits until they
// have returned, so nothing writes under the runs directory any more. A run
// turns finished (and its run.json is saved) before its goroutine unwinds, so
// tests call Close before their temp dir is removed.
func (s *Service) Close() {
	if rt, err := s.runtime(); err == nil {
		rt.mu.Lock()
		for _, l := range rt.live {
			if l.cancel != nil {
				l.cancel()
			}
		}
		rt.mu.Unlock()
	}
	s.wg.Wait()
}

// live is the in-memory side of a run.
type live struct {
	run *Run
	br  *broker
	// cacheOpen: the agent may still submit a cache for this run.
	cacheOpen bool
	// cancel stops the replay goroutine of a replay run.
	cancel context.CancelFunc
	// healStep is the step a repair agent currently works on ("" = none).
	healStep string
	// healCached marks steps whose repaired segment was accepted.
	healCached map[string]bool
	// stepHashes and caseHash are the hashes of the case as the run started;
	// a cache submission is stamped with these, not the on-disk case, so edits
	// made while the run was going show up as stale segments.
	stepHashes map[string]string
	caseHash   string
}

type runtime struct {
	store *RunStore
	mu    sync.Mutex
	live  map[string]*live
	locks map[string]string // device id -> run id
}

func (s *Service) runtime() (*runtime, error) {
	s.once.Do(func() {
		st, err := NewRunStore(s.Store.Root())
		if err != nil {
			s.rtErr = err
			return
		}
		s.rt = &runtime{store: st, live: map[string]*live{}, locks: map[string]string{}}
	})
	return s.rt, s.rtErr
}

// StartRunRequest is the body of POST /runs.
type StartRunRequest struct {
	Project  string            `json:"project"`
	Case     string            `json:"case"`
	DeviceID string            `json:"deviceId"`
	Mode     string            `json:"mode"` // auto (default) | record | replay
	Heal     bool              `json:"heal"`
	Params   map[string]string `json:"params"`
	Agent    string            `json:"agent"` // "" = configured default, "external" = driven from outside
}

func (r *Run) snapshot() *Run {
	b, _ := json.Marshal(r)
	var c Run
	_ = json.Unmarshal(b, &c)
	return &c
}

// effectiveParams overlays the supplied values on the case defaults and
// rejects names the case does not declare.
func effectiveParams(c *Case, in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, p := range c.Params {
		if p.Default != "" {
			out[k] = p.Default
		}
	}
	for k, v := range in {
		if _, ok := c.Params[k]; !ok {
			names := c.ParamNames()
			sort.Strings(names)
			return nil, fmt.Errorf("%w: unknown param %q; declared params: %s", ErrInvalid, k, idList(names))
		}
		out[k] = v
	}
	return out, nil
}

// StartRun creates a run. Record runs launch an agent; replay runs start the
// deterministic replayer in the background. Mode auto becomes replay when
// every step has a fresh cached segment, record when none has, and for a mix
// replay with heal when an agent can be launched (it records the stale or
// missing segments), else a replay that fails naming them.
func (s *Service) StartRun(req StartRunRequest) (*Run, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	if req.Project == "" || req.Case == "" {
		return nil, fmt.Errorf("%w: project and case are required", ErrInvalid)
	}
	if strings.TrimSpace(req.DeviceID) == "" {
		return nil, fmt.Errorf("%w: deviceId is required", ErrInvalid)
	}
	mode := req.Mode
	if mode == "" {
		mode = "auto"
	}
	switch mode {
	case ModeRecord, ModeReplay, "auto":
	default:
		return nil, fmt.Errorf("%w: mode must be auto, record or replay", ErrInvalid)
	}
	e, err := s.Store.Get(req.Project, req.Case)
	if err != nil {
		return nil, err
	}
	if e.Case == nil || len(e.Errors) > 0 {
		return nil, fmt.Errorf("%w: case %q is invalid: %s", ErrInvalid, e.ID, e.Errors)
	}
	c := e.Case
	params, err := effectiveParams(c, req.Params)
	if err != nil {
		return nil, err
	}
	platform := ""
	if s.Devices != nil {
		dev, derr := s.Devices(req.DeviceID)
		if derr != nil {
			if errors.Is(derr, device.ErrNotFound) {
				return nil, fmt.Errorf("%w: device %q", ErrNotFound, req.DeviceID)
			}
			return nil, derr
		}
		platform = string(dev.Info().Platform)
	}
	if want := casePlatform(c); want != "" && platform != "" && want != platform {
		return nil, fmt.Errorf("%w: case targets %s but device %q is %s", ErrInvalid, want, req.DeviceID, platform)
	}
	agent := strings.TrimSpace(req.Agent)
	if strings.EqualFold(agent, "default") {
		agent = "" // "default" is an alias for the Workflow-slot default
	}
	lookupPlatform := lookupCachePlatform(c, platform)

	// Resolve auto and check what replay needs.
	var cache *Cache
	var stale map[string]string
	heal := req.Heal
	if mode != ModeRecord {
		cache, _ = s.Store.GetCache(e.Project, e.Name, lookupPlatform)
		stale = Staleness(c, cache)
		canAgent := s.Launcher != nil && agent != AgentExternal
		if mode == "auto" {
			fresh := 0
			for _, st := range stale {
				if st == StateFresh {
					fresh++
				}
			}
			switch {
			case fresh == 0:
				mode, heal = ModeRecord, false
			case fresh == len(stale):
				mode = ModeReplay
			default:
				// Mixed: replay the fresh segments and let the agent record the
				// stale or missing ones as repairs. Without an agent the replay
				// fails fast naming the segments.
				mode, heal = ModeReplay, canAgent
			}
		}
		if mode == ModeReplay && heal && !canAgent {
			if agent == AgentExternal {
				return nil, fmt.Errorf("%w: heal needs a launched agent, not %q", ErrInvalid, AgentExternal)
			}
			return nil, fmt.Errorf("%w: no agent launcher is configured; heal is unavailable", ErrNotImplemented)
		}
	}
	if mode == ModeRecord {
		heal = false
	}

	now := time.Now().UTC()
	run := &Run{
		ID: newRunID(now), CaseID: e.ID, Project: e.Project, Name: e.Name, DeviceID: req.DeviceID,
		Platform: platform, Mode: mode, Agent: agent, Params: params, Heal: heal,
		Status: RunRunning, StartedAt: now, Healed: []string{}, Steps: make([]StepRun, len(c.Steps)),
	}
	for i, st := range c.Steps {
		run.Steps[i] = StepRun{ID: st.ID, Title: st.Title, Type: st.Type, State: StepPending,
			Substeps: []Substep{}, Asserts: []AssertResult{}}
	}

	rt.mu.Lock()
	if other, busy := rt.locks[req.DeviceID]; busy {
		rt.mu.Unlock()
		return nil, fmt.Errorf("%w: run %s is already using device %q", ErrDeviceBusy, other, req.DeviceID)
	}
	l := &live{run: run, br: newBroker(), stepHashes: c.StepHashes(), caseHash: CaseHash(c)}
	rt.locks[req.DeviceID] = run.ID
	rt.live[run.ID] = l
	rt.trimLocked()
	if err := rt.store.Save(run); err != nil {
		delete(rt.locks, req.DeviceID)
		delete(rt.live, run.ID)
		rt.mu.Unlock()
		return nil, err
	}
	l.br.publish(EvRun, run.snapshot())
	rt.mu.Unlock()
	rt.store.Prune()
	events.Info(fmt.Sprintf("Flow run started: %s on %s (%s)", e.ID, req.DeviceID, mode))

	if mode == ModeReplay {
		s.startReplay(l, c, cache, stale, agent)
		return s.GetRun(run.ID)
	}
	if err := s.startAgent(l, c, agent); err != nil {
		_, _ = s.Finish(run.ID, "agent could not be started: "+err.Error())
		return nil, err
	}
	return s.GetRun(run.ID)
}

// startAgent launches the agent of a record run: the workflow prompt goes to
// the Launcher, output lines go to agent.log, and when the agent exits the run
// is finished (an open run fails). External agents are not launched.
func (s *Service) startAgent(l *live, c *Case, agent string) error {
	if agent == AgentExternal {
		return nil
	}
	if s.Launcher == nil {
		return fmt.Errorf("%w: no agent launcher is configured; use agent %q and drive the run through the workflow_* tools", ErrNotImplemented, AgentExternal)
	}
	runID := l.run.ID
	system, task := RecordPrompt(PromptInput{
		CaseID: l.run.CaseID, RunID: runID, DeviceID: l.run.DeviceID, Platform: l.run.Platform,
		Case: c, Params: l.run.Params,
	})
	id, err := s.Launcher.Start(AgentRequest{
		Agent: agent, DeviceID: l.run.DeviceID, SystemPrompt: system, Prompt: task,
		OnLine: func(stream, line string) { s.LogLine(runID, stream, line) },
		OnDone: func(err error) {
			reason := ""
			switch {
			case err == nil:
			case errors.Is(err, context.Canceled):
				reason = "agent was cancelled"
			default:
				reason = "agent exited with an error: " + err.Error()
			}
			_, _ = s.Finish(runID, reason)
		},
	})
	if err != nil {
		return err
	}
	rt := s.rt
	rt.mu.Lock()
	l.run.AgentRunID = id
	_ = rt.store.Save(l.run)
	rt.mu.Unlock()
	return nil
}

// trimLocked forgets old finished runs so the live map stays small; their
// data stays on disk.
func (rt *runtime) trimLocked() {
	const keep = 50
	if len(rt.live) <= keep {
		return
	}
	var fin []*live
	for _, l := range rt.live {
		if l.run.finished() {
			fin = append(fin, l)
		}
	}
	sort.Slice(fin, func(i, j int) bool { return fin[i].run.ID < fin[j].run.ID })
	for i := 0; i+keep < len(rt.live) && i < len(fin); i++ {
		delete(rt.live, fin[i].run.ID)
	}
}

// GetRun returns a snapshot of a run (live or from disk).
func (s *Service) GetRun(id string) (*Run, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	rt.mu.Lock()
	if l, ok := rt.live[id]; ok {
		snap := l.run.snapshot()
		rt.mu.Unlock()
		return snap, nil
	}
	rt.mu.Unlock()
	return rt.store.Get(id)
}

// ListRuns returns runs newest first, optionally only those of caseID.
func (s *Service) ListRuns(caseID string, limit int) ([]*Run, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	out := rt.store.List(caseID, limit)
	rt.mu.Lock()
	for i, r := range out { // live state is newer than the file
		if l, ok := rt.live[r.ID]; ok {
			out[i] = l.run.snapshot()
		}
	}
	rt.mu.Unlock()
	return out, nil
}

// ActiveRun resolves the single running run of a case.
func (s *Service) ActiveRun(project, name string) (string, error) {
	rt, err := s.runtime()
	if err != nil {
		return "", err
	}
	id := CaseID(project, name)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var ids []string
	for _, l := range rt.live {
		if l.run.CaseID == id && l.run.Status == RunRunning {
			ids = append(ids, l.run.ID)
		}
	}
	sort.Strings(ids)
	switch len(ids) {
	case 0:
		return "", stateErrf("no running run for case %q; start one first (POST /api/v1/flows/runs) or pass run_id", id)
	case 1:
		return ids[0], nil
	}
	return "", stateErrf("case %q has %d running runs (%s); pass run_id to pick one", id, len(ids), idList(ids))
}

// lookup returns the live state of an active run.
func (rt *runtime) lookupLocked(id string) (*live, error) {
	if l, ok := rt.live[id]; ok {
		return l, nil
	}
	if r, err := rt.store.Get(id); err == nil {
		return nil, stateErrf("run %s is not active (status %s)", id, r.Status)
	}
	return nil, fmt.Errorf("%w: run %q", ErrNotFound, id)
}

// finishLocked closes the run and releases the device. The caller holds rt.mu.
func (rt *runtime) finishLocked(l *live) {
	r := l.run
	if rt.locks[r.DeviceID] == r.ID {
		delete(rt.locks, r.DeviceID)
	}
	if r.Status != RunFailed || r.Mode != ModeRecord {
		l.cacheOpen = false
	}
	_ = rt.store.Save(r)
	l.br.publish(EvRun, r.snapshot())
	l.br.publish(EvDone, map[string]string{"status": r.Status, "error": r.Error})
	switch r.Status {
	case RunPassed:
		events.Success("Flow run passed: " + r.CaseID + runEventDetail(r))
	case RunCancelled:
		events.Info("Flow run cancelled: " + r.CaseID + runEventDetail(r))
	default:
		events.Errorf("Flow run failed: %s: %s%s", r.CaseID, r.Error, runEventDetail(r))
	}
}

// runEventDetail is the machine-readable tail of the flow-run event message
// (and so of a webhook payload): run id, status, failed step and healed ids.
func runEventDetail(r *Run) string {
	d := fmt.Sprintf(" [run=%s status=%s", r.ID, r.Status)
	for _, st := range r.Steps {
		if st.State == StepFailed {
			d += " failedStep=" + st.ID
			break
		}
	}
	if len(r.Healed) > 0 {
		d += " healed=" + strings.Join(r.Healed, ",")
	}
	return d + "]"
}

// mutate runs fn on the live run under the lock and persists the result.
func (s *Service) mutate(id string, fn func(l *live) error) (*Run, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	l, err := rt.lookupLocked(id)
	if err != nil {
		return nil, err
	}
	if err := fn(l); err != nil {
		return nil, err
	}
	_ = rt.store.Save(l.run)
	return l.run.snapshot(), nil
}

// guardHeal rejects agent calls for a step other than the one being repaired
// while a self-heal is in progress. The caller holds rt.mu.
func (l *live) guardHeal(stepID string) error {
	if l.healStep != "" && stepID != l.healStep {
		return stateErrf("run %s is repairing step %q; only that step can be started, recorded or finished until the repair is done (got %q)", l.run.ID, l.healStep, stepID)
	}
	return nil
}

// StepStart marks a pending step running.
func (s *Service) StepStart(runID, stepID string) (*Run, error) {
	return s.mutate(runID, func(l *live) error {
		if err := l.guardHeal(stepID); err != nil {
			return err
		}
		if err := l.run.stepStart(stepID, time.Now().UTC()); err != nil {
			return err
		}
		l.br.publish(EvStep, l.run.step(stepID).clone())
		return nil
	})
}

// Substep records a substep on the running step.
func (s *Service) Substep(runID, stepID, text string) (*Run, error) {
	return s.mutate(runID, func(l *live) error {
		if err := l.guardHeal(stepID); err != nil {
			return err
		}
		ss, err := l.run.substep(stepID, text, time.Now().UTC())
		if err != nil {
			return err
		}
		l.br.publish(EvSubstep, map[string]any{"stepId": stepID, "text": ss.Text, "at": ss.At})
		return nil
	})
}

// Assert records an assertion on the running step.
func (s *Service) Assert(runID, stepID string, a AssertResult) (*Run, error) {
	return s.mutate(runID, func(l *live) error {
		if err := l.guardHeal(stepID); err != nil {
			return err
		}
		if err := l.run.assert(stepID, a); err != nil {
			return err
		}
		l.br.publish(EvAssert, map[string]any{"stepId": stepID, "text": a.Text, "passed": a.Passed, "detail": a.Detail})
		return nil
	})
}

// StepDone finishes the running step and captures its screenshot. A failed
// step fails the run (remaining steps are skipped). When every step passed, a
// record run waits for the cache submission.
func (s *Service) StepDone(runID, stepID string, passed bool, notes, errText string) (*Run, error) {
	// The screenshot is taken before the state change so that the step, its
	// screenshot and a run that ends with it become visible together.
	png := s.shootFor(runID, stepID)
	_, err := s.mutate(runID, func(l *live) error {
		if err := l.guardHeal(stepID); err != nil {
			return err
		}
		st, runDone, err := l.run.stepDone(stepID, passed, notes, errText, time.Now().UTC())
		if err != nil {
			return err
		}
		l.br.publish(EvStep, st.clone())
		switch {
		case runDone:
			l.cacheOpen = l.run.Mode == ModeRecord && anyPassed(l.run)
		case l.run.allPassed():
			if l.run.Mode == ModeRecord {
				l.cacheOpen = true // wait for workflow_cache_script
			} else if l.healStep == "" { // a repair is finished by the replayer
				l.run.end(RunPassed, "", time.Now().UTC())
			}
		}
		if len(png) > 0 {
			if err := s.saveShotLocked(l, stepID, png); err != nil {
				l.br.publish(EvLog, map[string]string{"stream": "server", "text": "cannot save screenshot: " + err.Error()})
			}
		}
		if l.run.finished() {
			s.rt.finishLocked(l)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetRun(runID)
}

func anyPassed(r *Run) bool {
	for _, s := range r.Steps {
		if s.State == StepPassed || s.State == StepHealed {
			return true
		}
	}
	return false
}

func (s *Service) shoot(ctx context.Context, deviceID string) ([]byte, error) {
	if s.ScreenshotFunc != nil {
		return s.ScreenshotFunc(ctx, deviceID)
	}
	if s.Devices == nil {
		return nil, nil
	}
	dev, err := s.Devices(deviceID)
	if err != nil {
		return nil, err
	}
	shot, err := dev.Screenshot(ctx)
	return shot.PNG, err
}

// shootFor captures the screen of the device of runID for step stepID. A
// failure is logged on the event stream, never returned: a missing screenshot
// must not fail the run.
func (s *Service) shootFor(runID, stepID string) []byte {
	rt := s.rt
	rt.mu.Lock()
	l, ok := rt.live[runID]
	if !ok {
		rt.mu.Unlock()
		return nil
	}
	deviceID := l.run.DeviceID
	rt.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	png, err := s.shoot(ctx, deviceID)
	if err != nil {
		rt.mu.Lock()
		l.br.publish(EvLog, map[string]string{"stream": "server", "text": "screenshot of step " + stepID + " failed: " + err.Error()})
		rt.mu.Unlock()
		return nil
	}
	return png
}

// saveShotLocked stores png as step-<id>.png and points the step at it. The
// caller holds rt.mu.
func (s *Service) saveShotLocked(l *live, stepID string, png []byte) error {
	rt := s.rt
	name := "step-" + stepID + ".png"
	if err := rt.store.WriteArtifact(l.run.ID, name, png); err != nil {
		return err
	}
	if st := l.run.step(stepID); st != nil {
		st.Screenshot = name
	}
	_ = rt.store.Save(l.run)
	l.br.publish(EvScreenshot, map[string]string{"stepId": stepID, "file": name})
	return nil
}

// Finish closes a run that is still open: it is called when the agent exits.
// A run whose steps all passed but that never submitted a cache fails with "no
// cache submitted"; any other open run fails with reason (default "agent
// exited before finishing"). Finishing a finished or unknown run is a no-op.
func (s *Service) Finish(runID, reason string) (*Run, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	if reason == "" {
		reason = "agent exited before finishing"
	}
	rt.mu.Lock()
	l, ok := rt.live[runID]
	if !ok {
		rt.mu.Unlock()
		return s.GetRun(runID)
	}
	pre := ""
	if !l.run.finished() {
		if cur := l.run.runningStep(); cur != nil {
			pre = cur.ID
		}
	}
	rt.mu.Unlock()
	var png []byte
	if pre != "" {
		png = s.shootFor(runID, pre)
	}
	rt.mu.Lock()
	if !l.run.finished() {
		closedStep := ""
		if cur := l.run.runningStep(); cur != nil {
			closedStep = cur.ID
		}
		now := time.Now().UTC()
		switch {
		case l.run.Mode == ModeRecord && l.run.allPassed():
			l.run.closeOpen(RunFailed, "no cache submitted: the agent exited without calling workflow_cache_script", now)
		case l.run.allPassed():
			l.run.end(RunPassed, "", now)
		default:
			l.run.closeOpen(RunFailed, reason, now)
		}
		if closedStep != "" && closedStep == pre && len(png) > 0 {
			_ = s.saveShotLocked(l, closedStep, png)
		}
		rt.finishLocked(l)
	}
	l.cacheOpen = false
	rt.mu.Unlock()
	return s.GetRun(runID)
}

// Cancel stops a running run. Cancelling a finished run is a no-op.
func (s *Service) Cancel(runID string) (*Run, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	rt.mu.Lock()
	l, ok := rt.live[runID]
	if !ok {
		rt.mu.Unlock()
		return s.GetRun(runID)
	}
	agentID := l.run.AgentRunID
	if l.cancel != nil {
		l.cancel()
	}
	if !l.run.finished() {
		l.run.closeOpen(RunCancelled, "cancelled", time.Now().UTC())
		l.cacheOpen = false
		rt.finishLocked(l)
	}
	l.cacheOpen = false
	rt.mu.Unlock()
	if agentID != "" && s.Launcher != nil {
		s.Launcher.Cancel(agentID)
	}
	return s.GetRun(runID)
}

// CacheResult is the outcome of a cache submission.
type CacheResult struct {
	Cache     *Cache            `json:"cache"`
	Staleness map[string]string `json:"staleness"`
	Run       *Run              `json:"run"`
	// Notes lists checks that were skipped (for example secrets whose value is unavailable).
	Notes string `json:"notes,omitempty"`
}

// SubmitCache validates and stores the segments an agent recorded during run.
// Submitted segments replace the cached ones with the same id; other fresh
// segments of the existing cache are kept. A run whose steps all passed
// finishes passed; after a failed step only segments of passed steps are
// accepted.
func (s *Service) SubmitCache(runID string, segs []Segment) (*CacheResult, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	rt.mu.Lock()
	l, ok := rt.live[runID]
	if !ok {
		rt.mu.Unlock()
		return nil, stateErrf("run %s is not active; a cache can only be submitted by the run that recorded it", runID)
	}
	r := l.run
	healStep := ""
	if r.Mode == ModeReplay && l.healStep != "" {
		healStep = l.healStep
	}
	switch {
	case healStep != "":
		st := r.step(healStep)
		switch {
		case len(segs) != 1 || segs[0].ID != healStep:
			rt.mu.Unlock()
			return nil, stateErrf("run %s is repairing step %q: submit exactly one segment with id %q", runID, healStep, healStep)
		case l.healCached[healStep]:
			rt.mu.Unlock()
			return nil, stateErrf("the repaired segment of step %q was already submitted", healStep)
		case st == nil || st.State != StepHealed:
			rt.mu.Unlock()
			return nil, stateErrf("finish step %q with workflow_step_done (passed=true) before submitting its repaired segment", healStep)
		}
	case r.Mode != ModeRecord:
		rt.mu.Unlock()
		return nil, stateErrf("run %s is a %s run; only record runs (and the repair of a failing step in a replay) submit a cache", runID, r.Mode)
	case r.CacheSubmitted:
		rt.mu.Unlock()
		return nil, stateErrf("run %s already submitted its cache", runID)
	case !l.cacheOpen && r.finished():
		rt.mu.Unlock()
		return nil, stateErrf("run %s is %s; its cache can no longer be submitted", runID, r.Status)
	case !r.finished() && !r.allPassed():
		pending := append(r.stepIDs(StepPending), r.stepIDs(StepRunning)...)
		rt.mu.Unlock()
		return nil, stateErrf("steps are not finished yet (unfinished step ids: %s); submit the cache after the last workflow_step_done", idList(pending))
	}
	if healStep == "" {
		for _, sg := range segs {
			st := r.step(sg.ID)
			if st != nil && st.State != StepPassed && st.State != StepHealed {
				rt.mu.Unlock()
				return nil, stateErrf("segment %q belongs to a step that did not pass (state %s); cache only passed steps", sg.ID, st.State)
			}
		}
	}
	project, name, runPlatform := r.Project, r.Name, r.Platform
	snapHashes, snapCaseHash := l.stepHashes, l.caseHash
	rt.mu.Unlock()

	e, err := s.Store.Get(project, name)
	if err != nil {
		return nil, err
	}
	if e.Case == nil || len(e.Errors) > 0 {
		return nil, fmt.Errorf("%w: case %q is invalid", ErrInvalid, e.ID)
	}
	var note string
	if verrs, n := checkAgainstCase(e.Case, segs, s.Secrets); len(verrs) > 0 {
		return nil, verrs
	} else {
		note = n
	}
	var stamp *hashStamp
	if snapHashes != nil {
		stamp = &hashStamp{steps: map[string]string{}, caseHash: snapCaseHash}
		for _, sg := range segs { // only what was recorded by this run
			if h, ok := snapHashes[sg.ID]; ok {
				stamp.steps[sg.ID] = h
			}
		}
		cur := e.Case.StepHashes()
		var changed []string
		for _, sg := range segs {
			if h, ok := snapHashes[sg.ID]; ok && cur[sg.ID] != h {
				changed = append(changed, sg.ID)
			}
		}
		var parts []string
		if len(changed) > 0 {
			parts = append(parts, "the case changed since the run started for step(s) "+strings.Join(changed, ", ")+": their segments were stored with the hash of the case as the run saw it and show as stale")
		}
		if snapCaseHash != CaseHash(e.Case) {
			parts = append(parts, "the case's app, secrets or step order changed since the run started: the whole cache shows as stale")
		}
		if len(parts) > 0 {
			if note != "" {
				note += "; "
			}
			note += strings.Join(parts, "; ")
		}
	}
	platform := s.writeCachePlatform(project, name, e.Case, runPlatform)
	lookup := lookupCachePlatform(e.Case, runPlatform)
	segs = normalizeSegments(segs)
	if healStep != "" {
		healedAt := time.Now().UTC().Truncate(time.Millisecond)
		segs[0].HealedAt = &healedAt
		segs[0].RecordedAt = healedAt
	}
	merged := append([]Segment(nil), segs...)
	submitted := map[string]bool{}
	for _, sg := range segs {
		submitted[sg.ID] = true
	}
	if old, gerr := s.Store.GetCache(project, name, lookup); gerr == nil {
		stale := Staleness(e.Case, old)
		for _, sg := range old.Segments {
			if !submitted[sg.ID] && stale[sg.ID] == StateFresh {
				merged = append(merged, sg)
			}
		}
	}
	order := map[string]int{}
	for i, st := range e.Case.Steps {
		order[st.ID] = i
	}
	sort.SliceStable(merged, func(i, j int) bool { return order[merged[i].ID] < order[merged[j].ID] })
	cache, err := s.Store.putCache(project, name, platform, Cache{Segments: merged}, stamp)
	if err != nil {
		return nil, err
	}

	rt.mu.Lock()
	if healStep != "" {
		if l.healCached == nil {
			l.healCached = map[string]bool{}
		}
		l.healCached[healStep] = true
		snap := r.snapshot()
		rt.mu.Unlock()
		return &CacheResult{Cache: cache, Staleness: Staleness(e.Case, cache), Run: snap, Notes: note}, nil
	}
	r.CacheSubmitted = true
	l.cacheOpen = false
	if !r.finished() {
		r.end(RunPassed, "", time.Now().UTC())
		rt.finishLocked(l)
	} else {
		_ = rt.store.Save(r)
	}
	snap := r.snapshot()
	rt.mu.Unlock()
	return &CacheResult{Cache: cache, Staleness: Staleness(e.Case, cache), Run: snap, Notes: note}, nil
}

// subscribe returns the broker of a run, or a finished one synthesised from disk.
func (s *Service) broker(id string) (*broker, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	rt.mu.Lock()
	if l, ok := rt.live[id]; ok {
		rt.mu.Unlock()
		return l.br, nil
	}
	rt.mu.Unlock()
	r, err := rt.store.Get(id)
	if err != nil {
		return nil, err
	}
	b := newBroker()
	b.publish(EvRun, r)
	b.publish(EvDone, map[string]string{"status": r.Status, "error": r.Error})
	return b, nil
}

// LogLine appends an agent output line to agent.log and the event stream.
func (s *Service) LogLine(runID, stream, line string) {
	rt, err := s.runtime()
	if err != nil {
		return
	}
	_ = rt.store.AppendLog(runID, line)
	rt.mu.Lock()
	if l, ok := rt.live[runID]; ok {
		l.br.publish(EvLog, map[string]string{"stream": stream, "text": line})
	}
	rt.mu.Unlock()
}

// lookupCachePlatform is the platform used to find a case's cache: the one the
// case pins, else the run device's platform (GetCache then falls back to the
// default sidecar).
func lookupCachePlatform(c *Case, devicePlatform string) string {
	if p := casePlatform(c); p != "" {
		return p
	}
	if devicePlatform == "android" || devicePlatform == "ios" {
		return devicePlatform
	}
	return ""
}

// writeCachePlatform is the sidecar a record or heal writes to. A pinned case
// writes its own platform; a platform-"any" case writes the default sidecar
// unless a platform-specific one already exists for the device platform.
func (s *Service) writeCachePlatform(project, name string, c *Case, devicePlatform string) string {
	if p := casePlatform(c); p != "" {
		return p
	}
	p := lookupCachePlatform(c, devicePlatform)
	if p == "" {
		return ""
	}
	if cur, err := s.Store.GetCache(project, name, p); err == nil && cur.Platform == p {
		return p
	}
	return ""
}
