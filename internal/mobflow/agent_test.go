package mobflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	segLogin   = `{"id":"s1","steps":[{"action":"launch_app","bundleId":"com.shop.app"},{"action":"wait_stable"},{"action":"tap","selector":{"text":"Sign in"}}]}`
	segAdd     = `{"id":"s2","steps":[{"action":"tap","selector":{"text":"Add to cart"}},{"action":"wait_stable"}]}`
	segCart    = `{"id":"s3","steps":[{"action":"tap","selector":{"text":"Cart"}},{"action":"assert_exists","selector":{"textContains":"Total"}}]}`
	allSegs    = `{"segments":[` + segLogin + `,` + segAdd + `,` + segCart + `]}`
	fakeSecret = "hunter2-secret"
)

// fakeLauncher records the request and runs script in a goroutine, like an
// agent process that talks to the server.
type fakeLauncher struct {
	mu        sync.Mutex
	req       AgentRequest
	cancelled []string
	startErr  error
	script    func(s *Service, runID string, req AgentRequest) error
	svc       *Service
	runIDs    chan string
}

func (f *fakeLauncher) Start(req AgentRequest) (string, error) {
	if f.startErr != nil {
		return "", f.startErr
	}
	f.mu.Lock()
	f.req = req
	f.mu.Unlock()
	go func() {
		runID := <-f.runIDs
		req.OnLine("stdout", "agent started")
		err := f.script(f.svc, runID, req)
		req.OnDone(err)
	}()
	return "agent-1", nil
}

func (f *fakeLauncher) Cancel(id string) {
	f.mu.Lock()
	f.cancelled = append(f.cancelled, id)
	f.mu.Unlock()
}

func newAgentSvc(t *testing.T, script func(s *Service, runID string, req AgentRequest) error) (*Service, *fakeLauncher, string) {
	t.Helper()
	s, _, pid := newSvc(t)
	f := &fakeLauncher{script: script, svc: s, runIDs: make(chan string, 1)}
	s.Launcher = f
	s.Secrets = func(id string) (string, error) {
		if id == "shop_password" {
			return fakeSecret, nil
		}
		return "", errors.New("unknown secret")
	}
	return s, f, pid
}

// startRecorded starts a record run with the default agent and hands the id
// to the fake agent goroutine.
func startRecorded(t *testing.T, s *Service, f *fakeLauncher, pid string) *Run {
	t.Helper()
	run, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "record"})
	if err != nil {
		t.Fatal(err)
	}
	f.runIDs <- run.ID
	return run
}

func waitFinished(t *testing.T, s *Service, id string) *Run {
	t.Helper()
	for i := 0; i < 300; i++ {
		r, err := s.GetRun(id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != RunRunning {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not finish")
	return nil
}

func rawSegs(t *testing.T, body string) []Segment {
	t.Helper()
	var b cacheBody
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Fatal(err)
	}
	return b.Segments
}

func passSteps(t *testing.T, s *Service, runID string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := s.StepStart(runID, id); err != nil {
			t.Fatal(err)
		}
		_, _ = s.Substep(runID, id, "worked on "+id)
		if _, err := s.StepDone(runID, id, true, "", ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentRecordHappyPathWritesCache(t *testing.T) {
	s, f, pid := newAgentSvc(t, func(s *Service, id string, _ AgentRequest) error {
		passSteps(t, s, id, "s1", "s2", "s3")
		_, err := s.SubmitCache(id, rawSegs(t, allSegs))
		return err
	})
	run := startRecorded(t, s, f, pid)
	got := waitFinished(t, s, run.ID)
	if got.Status != RunPassed || !got.CacheSubmitted || got.AgentRunID != "agent-1" {
		t.Fatalf("run: %+v", got)
	}
	// prompt content: ids of the run reach the agent
	for _, w := range []string{run.CaseID, run.ID, "dev1"} {
		if !strings.Contains(f.req.SystemPrompt+f.req.Prompt, w) {
			t.Fatalf("prompt lacks %q", w)
		}
	}
	c, err := s.Store.GetCache(pid, "checkout", "")
	if err != nil || len(c.Segments) != 3 {
		t.Fatalf("cache: %+v %v", c, err)
	}
	e, _ := s.Store.Get(pid, "checkout")
	hashes := e.Case.StepHashes()
	for _, sg := range c.Segments {
		if sg.StepHash == "" || sg.StepHash != hashes[sg.ID] {
			t.Fatalf("segment %s hash %q want %q", sg.ID, sg.StepHash, hashes[sg.ID])
		}
	}
	// agent.log has the agent output
	rt, _ := s.runtime()
	b, err := os.ReadFile(filepath.Join(rt.store.Dir(run.ID), "agent.log"))
	if err != nil || !strings.Contains(string(b), "agent started") {
		t.Fatalf("agent.log: %q %v", b, err)
	}
	// editing one step's do stales only that segment
	edited := strings.Replace(sample, "Open the cart", "Open the shopping cart", 1)
	if _, _, err := s.Store.Save(pid, "checkout", []byte(edited)); err != nil {
		t.Fatal(err)
	}
	e, _ = s.Store.Get(pid, "checkout")
	st := Staleness(e.Case, c)
	if st["s1"] != StateFresh || st["s2"] != StateFresh || st["s3"] != StateStale {
		t.Fatalf("staleness: %v", st)
	}
}

func TestAgentCacheMergeKeepsFreshSegments(t *testing.T) {
	s, f, pid := newAgentSvc(t, func(s *Service, id string, _ AgentRequest) error {
		passSteps(t, s, id, "s1", "s2", "s3")
		_, err := s.SubmitCache(id, rawSegs(t, allSegs))
		return err
	})
	waitFinished(t, s, startRecorded(t, s, f, pid).ID)
	// Re-record only s2: the new s2 replaces the old one, s1 and s3 stay.
	newS2 := `{"segments":[{"id":"s2","steps":[{"action":"tap","selector":{"text":"Add"}}]}]}`
	f2 := &fakeLauncher{svc: s, runIDs: make(chan string, 1), script: func(s *Service, id string, _ AgentRequest) error {
		passSteps(t, s, id, "s1", "s2", "s3")
		_, err := s.SubmitCache(id, rawSegs(t, newS2))
		return err
	}}
	s.Launcher = f2
	waitFinished(t, s, startRecorded(t, s, f2, pid).ID)
	c, _ := s.Store.GetCache(pid, "checkout", "")
	if c.Version != 2 || len(c.Segments) != 3 || !strings.Contains(string(c.Segments[1].Steps[0]), `"Add"`) {
		t.Fatalf("merged cache: %+v", c)
	}
	if !strings.Contains(string(c.Segments[0].Steps[0]), "launch_app") || c.Segments[2].ID != "s3" {
		t.Fatalf("kept segments wrong: %+v", c.Segments)
	}
}

func TestSubmitCacheValidation(t *testing.T) {
	s, _, pid := newSvc(t)
	s.Secrets = func(id string) (string, error) { return fakeSecret, nil }
	h := s.Routes()
	run := startExternal(t, s, pid, "dev1")
	passSteps(t, s, run.ID, "s1", "s2", "s3")
	post := func(body string) (int, string) {
		w := doReq(t, h, "POST", "/runs/"+run.ID+"/cache", body)
		return w.Code, w.Body.String()
	}
	seg := func(id, steps string) string { return `{"segments":[{"id":"` + id + `","steps":` + steps + `}]}` }
	cases := []struct{ name, body, want string }{
		{"bad dsl", seg("s1", `[{"action":"zap"}]`), "unknown action"},
		{"not objects", seg("s1", `["tap"]`), "must be a JSON object"},
		{"unknown segment id", seg("zz", `[{"action":"wait_stable"}]`), "does not match any case step"},
		{"empty segment", seg("s1", `[]`), "no steps"},
		{"assert step without assertion", seg("s3", `[{"action":"tap","selector":{"text":"Cart"}}]`), "at least one assertion"},
		{"on_fail require_user", seg("s1", `[{"action":"tap","selector":{"text":"x"},"onFail":"require_user"}]`), "require_user"},
		{"on_fail replan object nested", seg("s1", `[{"action":"if_exists","selector":{"text":"x"},"then":[{"action":"tap","selector":{"text":"y"},"onFail":{"strategy":"retry","fallbackStrategy":"replan"}}]}]`), "replan"},
		{"literal secret", seg("s1", `[{"action":"type","text":"`+fakeSecret+`","selector":{"type":"input"}}]`), "literal secret"},
		{"undeclared secret", seg("s1", `[{"action":"type_secret","secretId":"other","selector":{"type":"input"}}]`), "does not declare"},
	}
	for _, c := range cases {
		code, body := post(c.body)
		if code != 400 || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %s (want %q)", c.name, code, body, c.want)
		}
		if strings.Contains(body, fakeSecret) {
			t.Errorf("%s: response leaks the secret", c.name)
		}
	}
	if _, err := s.Store.GetCache(pid, "checkout", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a rejected submission must write nothing: %v", err)
	}
	got, _ := s.GetRun(run.ID)
	if got.Status != RunRunning {
		t.Fatalf("a rejected cache must leave the run open: %s", got.Status)
	}
	ok := `{"segments":[` + segLogin + `,` + segCart + `]}`
	if code, body := post(ok); code != 200 || !strings.Contains(body, `"stepHash"`) {
		t.Fatalf("valid cache: %d %s", code, body)
	}
	if got, _ := s.GetRun(run.ID); got.Status != RunPassed {
		t.Fatalf("status %s", got.Status)
	}
	if code, body := post(ok); code != 409 || !strings.Contains(body, "not active") && !strings.Contains(body, "already") {
		t.Fatalf("second submit: %d %s", code, body)
	}
}

func TestSubmitCacheStateRules(t *testing.T) {
	s, _, pid := newSvc(t)
	run := startExternal(t, s, pid, "dev1")
	// steps pending: rejected with the ids
	if _, err := s.SubmitCache(run.ID, rawSegs(t, `{"segments":[`+segLogin+`]}`)); err == nil || !strings.Contains(err.Error(), "s1, s2, s3") {
		t.Fatalf("early submit: %v", err)
	}
	passSteps(t, s, run.ID, "s1")
	_, _ = s.StepStart(run.ID, "s2")
	if _, err := s.StepDone(run.ID, "s2", false, "no add button", ""); err != nil {
		t.Fatal(err)
	}
	// failed run: only passed steps may be cached
	if _, err := s.SubmitCache(run.ID, rawSegs(t, `{"segments":[`+segAdd+`]}`)); err == nil || !strings.Contains(err.Error(), "did not pass") {
		t.Fatalf("failed-step segment: %v", err)
	}
	res, err := s.SubmitCache(run.ID, rawSegs(t, `{"segments":[`+segLogin+`]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Run.Status != RunFailed || !res.Run.CacheSubmitted || res.Staleness["s1"] != StateFresh || res.Staleness["s2"] != StateMissing {
		t.Fatalf("%+v", res)
	}
	if _, err := s.SubmitCache(run.ID, rawSegs(t, `{"segments":[`+segLogin+`]}`)); err == nil {
		t.Fatal("second submission must fail")
	}
	// Replay-mode rules do not apply to external record runs only; cancelled runs refuse caches.
	run2 := startExternal(t, s, pid, "dev1")
	passSteps(t, s, run2.ID, "s1", "s2", "s3")
	_, _ = s.Cancel(run2.ID)
	if _, err := s.SubmitCache(run2.ID, rawSegs(t, allSegs)); err == nil {
		t.Fatal("a cancelled run cannot submit")
	}
}

func TestAgentExitsEarlyFailsRun(t *testing.T) {
	s, f, pid := newAgentSvc(t, func(s *Service, id string, _ AgentRequest) error {
		_, _ = s.StepStart(id, "s1")
		return nil // exits with a step still open
	})
	run := startRecorded(t, s, f, pid)
	got := waitFinished(t, s, run.ID)
	if got.Status != RunFailed || got.Error != "agent exited before finishing" || got.Steps[0].State != StepFailed || got.Steps[1].State != StepSkipped {
		t.Fatalf("%+v", got)
	}
	if _, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "record", Agent: AgentExternal}); err != nil {
		t.Fatalf("device must be released: %v", err)
	}
}

func TestAgentErrorAndNoCache(t *testing.T) {
	s, f, pid := newAgentSvc(t, func(s *Service, id string, _ AgentRequest) error {
		_, _ = s.StepStart(id, "s1")
		return errors.New("exit status 1")
	})
	got := waitFinished(t, s, startRecorded(t, s, f, pid).ID)
	if got.Status != RunFailed || !strings.Contains(got.Error, "agent exited with an error: exit status 1") {
		t.Fatalf("%+v", got)
	}

	s2, f2, pid2 := newAgentSvc(t, func(s *Service, id string, _ AgentRequest) error {
		passSteps(t, s, id, "s1", "s2", "s3")
		return nil // all steps passed but no cache submitted
	})
	got = waitFinished(t, s2, startRecorded(t, s2, f2, pid2).ID)
	if got.Status != RunFailed || !strings.Contains(got.Error, "no cache submitted") {
		t.Fatalf("%+v", got)
	}
	if _, err := s2.Store.GetCache(pid2, "checkout", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no cache expected: %v", err)
	}
}

func TestAgentOnDoneRunsOnceAndCancelStopsAgent(t *testing.T) {
	release := make(chan struct{})
	s, f, pid := newAgentSvc(t, func(s *Service, id string, _ AgentRequest) error {
		<-release
		return context.Canceled
	})
	run := startRecorded(t, s, f, pid)
	if _, err := s.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	c := append([]string(nil), f.cancelled...)
	f.mu.Unlock()
	if len(c) != 1 || c[0] != "agent-1" {
		t.Fatalf("agent cancel: %v", c)
	}
	close(release)
	time.Sleep(50 * time.Millisecond) // OnDone -> Finish must not change a cancelled run
	got, _ := s.GetRun(run.ID)
	if got.Status != RunCancelled || got.Error != "cancelled" {
		t.Fatalf("%+v", got)
	}
}

func TestAgentLaunchFailureFailsRunAndFreesDevice(t *testing.T) {
	s, f, pid := newAgentSvc(t, nil)
	f.startErr = errors.New("claude is not installed")
	_, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "record"})
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("err: %v", err)
	}
	runs, _ := s.ListRuns("", 5)
	if len(runs) != 1 || runs[0].Status != RunFailed || !strings.Contains(runs[0].Error, "not installed") {
		t.Fatalf("runs: %+v", runs)
	}
	f.startErr = nil
	if _, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "record", Agent: AgentExternal}); err != nil {
		t.Fatalf("device not released: %v", err)
	}
	// no launcher configured: clear error, not a panic
	s.Launcher = nil
	if _, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev2", Mode: "record"}); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("no launcher: %v", err)
	}
}

func TestRecordPromptContainsIdsAndRules(t *testing.T) {
	c := mustParse(t, sample)
	sys, task := RecordPrompt(PromptInput{CaseID: "shop/checkout", RunID: "RUN123", DeviceID: "DEV9", Platform: "ios", Case: c,
		Params: map[string]string{"email": "qa@example.com", "item": "Blue mug"}})
	for _, w := range []string{"shop/checkout", "RUN123", "DEV9", "workflow_get_case", "workflow_step_start", "workflow_substep",
		"workflow_assert", "workflow_step_done", "workflow_cache_script", "wait_stable", "type_secret", "${param}", "only the segments of the steps that passed"} {
		if !strings.Contains(sys+task, w) {
			t.Errorf("prompt lacks %q", w)
		}
	}
	if strings.Contains(sys, "{{") {
		t.Errorf("unfilled placeholder: %s", sys)
	}
	for _, w := range []string{"id=s1", "id=s3", "shop_password", `email = "qa@example.com"`, "expect: Cart badge shows 1"} {
		if !strings.Contains(task, w) {
			t.Errorf("task lacks %q", w)
		}
	}
	// the example segment in the prompt is valid, assert-aware DSL
	start := strings.Index(sys, "Example segment for a login step:\n") + len("Example segment for a login step:\n")
	end := strings.Index(sys[start:], "\n\nErrors from")
	var seg Segment
	if err := json.Unmarshal([]byte(sys[start:start+end]), &seg); err != nil {
		t.Fatalf("example is not JSON: %v", err)
	}
	if err := ValidateSegment(seg, Step{ID: "s1", Type: "login"}); err != nil {
		t.Fatalf("example segment rejected: %v", err)
	}
}

func TestHealPrompt(t *testing.T) {
	c := mustParse(t, sample)
	step, _ := c.Step("s2")
	sys, task := HealPrompt(HealInput{
		PromptInput: PromptInput{CaseID: "shop/checkout", RunID: "R1", DeviceID: "D1", Platform: "android", Case: c},
		Step:        step, SegmentDSL: []json.RawMessage{json.RawMessage(`{"action":"tap","selector":{"text":"Gone"}}`)},
		Error: "NO_MATCH: text=Gone", ScreenshotPath: "/tmp/run/step-s2.png", RecentSubsteps: []string{"tapped Gone"},
	})
	for _, w := range []string{"step s2", `"Gone"`, "NO_MATCH", "/tmp/run/step-s2.png", "tapped Gone"} {
		if !strings.Contains(sys+task, w) {
			t.Errorf("heal prompt lacks %q", w)
		}
	}
	if !strings.Contains(sys, "exactly one segment") || strings.Contains(sys, "{{") {
		t.Error(sys)
	}
}
