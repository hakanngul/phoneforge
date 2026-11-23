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

	once  sync.Once
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

// live is the in-memory side of a run.
type live struct {
	run *Run
	br  *broker
	// cacheOpen: the agent may still submit a cache for this run.
	cacheOpen bool
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

// StartRun creates a run and, for record runs with an agent, launches it.
// Replay and auto modes arrive with the replay engine (ErrNotImplemented).
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
	case ModeRecord:
	case "auto", ModeReplay:
		return nil, fmt.Errorf("%w: mode %q needs the replay engine; use mode \"record\"", ErrNotImplemented, mode)
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

	now := time.Now().UTC()
	run := &Run{
		ID: newRunID(now), CaseID: e.ID, Project: e.Project, Name: e.Name, DeviceID: req.DeviceID,
		Platform: platform, Mode: mode, Agent: agent, Params: params,
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
	l := &live{run: run, br: newBroker()}
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

	if err := s.startAgent(l, c, agent); err != nil {
		_, _ = s.Finish(run.ID, "agent could not be started: "+err.Error())
		return nil, err
	}
	return s.GetRun(run.ID)
}

// startAgent launches the agent of a record run. Slice 2 supports only
// external agents; the launcher arrives with slice 3.
func (s *Service) startAgent(l *live, c *Case, agent string) error {
	if agent == AgentExternal {
		return nil
	}
	return fmt.Errorf("%w: agent launch is not available yet; use agent %q", ErrNotImplemented, AgentExternal)
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
		events.Success(fmt.Sprintf("Flow run passed: %s", r.CaseID))
	case RunCancelled:
		events.Info(fmt.Sprintf("Flow run cancelled: %s", r.CaseID))
	default:
		events.Errorf("Flow run failed: %s: %s", r.CaseID, r.Error)
	}
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

// StepStart marks a pending step running.
func (s *Service) StepStart(runID, stepID string) (*Run, error) {
	return s.mutate(runID, func(l *live) error {
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
	needFinish := false
	_, err := s.mutate(runID, func(l *live) error {
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
			} else {
				l.run.end(RunPassed, "", time.Now().UTC())
			}
		}
		needFinish = l.run.finished()
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.capture(runID, stepID)
	if needFinish {
		s.rt.mu.Lock()
		if l, ok := s.rt.live[runID]; ok {
			s.rt.finishLocked(l)
		}
		s.rt.mu.Unlock()
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

// capture stores step-<id>.png for a step. Failures are logged on the event
// stream, never returned: a missing screenshot must not fail the run.
func (s *Service) capture(runID, stepID string) {
	rt := s.rt
	rt.mu.Lock()
	l, ok := rt.live[runID]
	if !ok {
		rt.mu.Unlock()
		return
	}
	deviceID := l.run.DeviceID
	rt.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	png, err := s.shoot(ctx, deviceID)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if err != nil {
		l.br.publish(EvLog, map[string]string{"stream": "server", "text": "screenshot of step " + stepID + " failed: " + err.Error()})
		return
	}
	if len(png) == 0 {
		return
	}
	name := "step-" + stepID + ".png"
	if err := rt.store.WriteArtifact(runID, name, png); err != nil {
		l.br.publish(EvLog, map[string]string{"stream": "server", "text": "cannot save screenshot: " + err.Error()})
		return
	}
	if st := l.run.step(stepID); st != nil {
		st.Screenshot = name
	}
	_ = rt.store.Save(l.run)
	l.br.publish(EvScreenshot, map[string]string{"stepId": stepID, "file": name})
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
	var closedStep string
	closed := false
	if !l.run.finished() {
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
		closed = true
	}
	l.cacheOpen = false
	rt.mu.Unlock()
	if closedStep != "" {
		s.capture(runID, closedStep)
	}
	if closed {
		rt.mu.Lock()
		rt.finishLocked(l)
		rt.mu.Unlock()
	}
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
	if !l.run.finished() {
		l.run.closeOpen(RunCancelled, "cancelled", time.Now().UTC())
		l.cacheOpen = false
		rt.finishLocked(l)
	}
	l.cacheOpen = false
	rt.mu.Unlock()
	return s.GetRun(runID)
}

// CacheResult is the outcome of a cache submission.
type CacheResult struct {
	Cache     *Cache            `json:"cache"`
	Staleness map[string]string `json:"staleness"`
	Run       *Run              `json:"run"`
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
	switch {
	case r.Mode != ModeRecord:
		rt.mu.Unlock()
		return nil, stateErrf("run %s is a %s run; only record runs submit a cache", runID, r.Mode)
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
	for _, sg := range segs {
		st := r.step(sg.ID)
		if st != nil && st.State != StepPassed && st.State != StepHealed {
			rt.mu.Unlock()
			return nil, stateErrf("segment %q belongs to a step that did not pass (state %s); cache only passed steps", sg.ID, st.State)
		}
	}
	project, name := r.Project, r.Name
	rt.mu.Unlock()

	e, err := s.Store.Get(project, name)
	if err != nil {
		return nil, err
	}
	if e.Case == nil || len(e.Errors) > 0 {
		return nil, fmt.Errorf("%w: case %q is invalid", ErrInvalid, e.ID)
	}
	platform := casePlatform(e.Case)
	merged := append([]Segment(nil), segs...)
	submitted := map[string]bool{}
	for _, sg := range segs {
		submitted[sg.ID] = true
	}
	if old, gerr := s.Store.GetCache(project, name, platform); gerr == nil {
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
	cache, err := s.Store.PutCache(project, name, platform, Cache{Segments: merged})
	if err != nil {
		return nil, err
	}

	rt.mu.Lock()
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
	return &CacheResult{Cache: cache, Staleness: Staleness(e.Case, cache), Run: snap}, nil
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
