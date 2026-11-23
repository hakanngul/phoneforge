package mobflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"pocketlab/internal/device"
)

// replayDev is a static screen: every selector used by the test segments that
// is not "Nope" matches an element.
type replayDev struct {
	mu    sync.Mutex
	taps  []string
	typed []string
}

func (d *replayDev) Info() device.Info {
	return device.Info{ID: "dev1", Platform: device.PlatformAndroid}
}
func (d *replayDev) Screenshot(context.Context) (device.Screenshot, error) {
	return device.Screenshot{PNG: []byte("\x89PNG-dev")}, nil
}
func (d *replayDev) UITree(context.Context) (device.Element, error) {
	el := func(text string, y int) device.Element {
		return device.Element{Role: "Button", Text: text, Bounds: device.Rect{X: 10, Y: y, W: 200, H: 50}}
	}
	return device.Element{Role: "FrameLayout", Bounds: device.Rect{W: 1080, H: 1920}, Children: []device.Element{
		el("Sign in", 10), el("Add to cart", 100), el("Cart", 200), el("Total: 9.99", 300),
		{Role: "EditText", Text: "", Label: "Email", Bounds: device.Rect{X: 10, Y: 400, W: 200, H: 50}},
	}}, nil
}
func (d *replayDev) Tap(context.Context, int, int) error                  { return nil }
func (d *replayDev) LongPress(context.Context, int, int, int) error       { return nil }
func (d *replayDev) Swipe(context.Context, int, int, int, int, int) error { return nil }
func (d *replayDev) TypeText(_ context.Context, s string) error {
	d.mu.Lock()
	d.typed = append(d.typed, s)
	d.mu.Unlock()
	return nil
}
func (d *replayDev) PressKey(context.Context, device.Key) error         { return nil }
func (d *replayDev) ListApps(context.Context) ([]device.AppInfo, error) { return nil, nil }
func (d *replayDev) LaunchApp(context.Context, string) error            { return nil }
func (d *replayDev) StopApp(context.Context, string) error              { return nil }

const (
	segBroken = `{"id":"s2","steps":[{"action":"tap","selector":{"text":"Nope"},"timeoutMs":100}]}`
	segFixed  = `{"id":"s2","steps":[{"action":"tap","selector":{"text":"Add to cart"},"settleMs":0}]}`
)

func mkSeg(t *testing.T, js string) Segment {
	t.Helper()
	var s Segment
	if err := json.Unmarshal([]byte(js), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// newReplaySvc returns a service with the sample case, a fresh cache of the
// three default segments, a fake device and no launcher.
func newReplaySvc(t *testing.T, segs ...string) (*Service, *replayDev, string) {
	t.Helper()
	s, _, pid := newSvc(t)
	dev := &replayDev{}
	s.Devices = func(string) (device.Device, error) { return dev, nil }
	s.Secrets = func(id string) (string, error) {
		if id == "shop_password" {
			return fakeSecret, nil
		}
		return "", errors.New("unknown secret")
	}
	if len(segs) == 0 {
		segs = []string{segLogin, segAdd, segCart}
	}
	putCache(t, s, pid, segs...)
	return s, dev, pid
}

func putCache(t *testing.T, s *Service, pid string, segs ...string) *Cache {
	t.Helper()
	var in []Segment
	for _, js := range segs {
		in = append(in, mkSeg(t, js))
	}
	c, err := s.Store.PutCache(pid, "checkout", "", Cache{Segments: in})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func startReplay(t *testing.T, s *Service, pid string, mut func(*StartRunRequest)) *Run {
	t.Helper()
	req := StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "replay"}
	if mut != nil {
		mut(&req)
	}
	r, err := s.StartRun(req)
	if err != nil {
		t.Fatal(err)
	}
	return waitFinished(t, s, r.ID)
}

// forbidLauncher fails the test when an agent is contacted.
type forbidLauncher struct{ t *testing.T }

func (f forbidLauncher) Start(AgentRequest) (string, error) {
	f.t.Error("the agent launcher was contacted")
	return "", errors.New("forbidden")
}
func (f forbidLauncher) Cancel(string) {}

func TestReplayFreshCachePassesWithoutAgent(t *testing.T) {
	s, _, pid := newReplaySvc(t)
	s.Launcher = forbidLauncher{t}
	run := startReplay(t, s, pid, nil)
	if run.Status != RunPassed || run.Mode != ModeReplay || len(run.Healed) != 0 {
		t.Fatalf("run: %+v", run)
	}
	for _, st := range run.Steps {
		if st.State != StepPassed || len(st.Substeps) == 0 || st.Screenshot == "" {
			t.Fatalf("step %+v", st)
		}
	}
	s3 := run.Steps[2]
	if len(s3.Asserts) != 1 || !s3.Asserts[0].Passed || !strings.Contains(s3.Asserts[0].Text, "Total") {
		t.Fatalf("asserts: %+v", s3.Asserts)
	}
	if !strings.Contains(run.Steps[0].Substeps[0].Text, "launch_app") {
		t.Fatalf("substeps: %+v", run.Steps[0].Substeps)
	}
	// the device lock was released
	if _, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "replay"}); err != nil {
		t.Fatal(err)
	}
}

func TestReplayParamsAndSecrets(t *testing.T) {
	login := `{"id":"s1","steps":[{"action":"type","selector":{"label":"Email"},"text":"${email}"},{"action":"type_secret","secretId":"shop_password","selector":{"label":"Email"}}]}`
	s, dev, pid := newReplaySvc(t, login, segAdd, segCart)
	run := startReplay(t, s, pid, func(r *StartRunRequest) { r.Params = map[string]string{"email": "who@example.com"} })
	if run.Status != RunPassed {
		t.Fatalf("run: %+v", run)
	}
	dev.mu.Lock()
	defer dev.mu.Unlock()
	if len(dev.typed) != 2 || dev.typed[0] != "who@example.com" || dev.typed[1] != fakeSecret {
		t.Fatalf("typed %q", dev.typed)
	}
	b, _ := json.Marshal(run)
	if strings.Contains(string(b), fakeSecret) {
		t.Fatal("secret leaked into the run")
	}
}

func TestReplayStaleOrMissingWithoutHeal(t *testing.T) {
	s, _, pid := newReplaySvc(t)
	s.Launcher = forbidLauncher{t}
	edited := strings.Replace(sample, "tap Add to cart", "tap Add to basket", 1)
	if _, _, err := s.Store.Save(pid, "checkout", []byte(edited)); err != nil {
		t.Fatal(err)
	}
	run := startReplay(t, s, pid, nil)
	if run.Status != RunFailed || !strings.Contains(run.Error, "stale segments: s2; record or heal needed") {
		t.Fatalf("run: %+v", run)
	}
	for _, st := range run.Steps {
		if st.State != StepSkipped {
			t.Fatalf("nothing should run: %+v", run.Steps)
		}
	}

	s2, _, pid2 := newReplaySvc(t, segLogin, segCart)
	run = startReplay(t, s2, pid2, nil)
	if run.Status != RunFailed || !strings.Contains(run.Error, "missing segments: s2") {
		t.Fatalf("missing: %+v", run)
	}
}

func TestReplayBrokenSegmentFailsWithStepAndScreenshot(t *testing.T) {
	s, _, pid := newReplaySvc(t, segLogin, segBroken, segCart)
	s.Launcher = forbidLauncher{t}
	run := startReplay(t, s, pid, nil)
	if run.Status != RunFailed || !strings.Contains(run.Error, "step s2 failed") {
		t.Fatalf("run: %+v", run)
	}
	want := []string{StepPassed, StepFailed, StepSkipped}
	for i, st := range run.Steps {
		if st.State != want[i] {
			t.Fatalf("step %s state %s want %s", st.ID, st.State, want[i])
		}
	}
	if !strings.Contains(run.Steps[1].Error, "DSL step 1 (tap)") {
		t.Fatalf("error: %q", run.Steps[1].Error)
	}
	if run.Steps[1].Screenshot == "" {
		t.Fatal("no failure screenshot")
	}
	p, err := s.rt.store.ArtifactPath(run.ID, run.Steps[1].Screenshot)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); len(b) == 0 {
		t.Fatal("empty screenshot")
	}
}

func TestReplayNeedsReplanCountsAsFailure(t *testing.T) {
	bad := `{"id":"s2","steps":[{"action":"tap","selector":{"text":"Nope"},"timeoutMs":100,"onFail":"replan"}]}`
	s, _, pid := newReplaySvc(t, segLogin, segAdd, segCart)
	// ValidateSegment rejects replan, so bypass the store to model an old or hand-edited file.
	cache, _ := s.Store.GetCache(pid, "checkout", "")
	cache.Segments[1] = mkSeg(t, bad)
	cache.Segments[1].StepHash = Staleness0(t, s, pid)["s2"]
	writeRawCache(t, s, pid, cache)
	run := startReplay(t, s, pid, nil)
	if run.Status != RunFailed || run.Steps[1].State != StepFailed || !strings.Contains(run.Steps[1].Error, "needs_replan") {
		t.Fatalf("run: %+v", run)
	}
}

// Staleness0 returns the current step hashes keyed by id.
func Staleness0(t *testing.T, s *Service, pid string) map[string]string {
	t.Helper()
	e, err := s.Store.Get(pid, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	return e.Case.StepHashes()
}

func writeRawCache(t *testing.T, s *Service, pid string, c *Cache) {
	t.Helper()
	dir, _ := s.Store.projectDir(pid)
	if err := writeCache(cachePath(dir, "checkout", ""), c); err != nil {
		t.Fatal(err)
	}
}

func TestReplayCancelMidRun(t *testing.T) {
	slow := `{"id":"s2","steps":[{"action":"wait","ms":30000}]}`
	s, _, pid := newReplaySvc(t, segLogin, slow, segCart)
	run, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "replay"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		r, _ := s.GetRun(run.ID)
		if r.Steps[1].State == StepRunning {
			break
		}
		if i > 300 {
			t.Fatalf("never reached s2: %+v", r)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := s.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	got := waitFinished(t, s, run.ID)
	if got.Status != RunCancelled {
		t.Fatalf("status %s", got.Status)
	}
	time.Sleep(100 * time.Millisecond) // the goroutine must not revive or fail the run
	got, _ = s.GetRun(run.ID)
	if got.Status != RunCancelled || got.Steps[2].State != StepSkipped {
		t.Fatalf("after cancel: %+v", got)
	}
	if _, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "replay"}); err != nil {
		t.Fatalf("device lock not released: %v", err)
	}
}

var runIDInPrompt = regexp.MustCompile(`\(run ([0-9a-f]{16}-[0-9a-f]{8})\)`)

// healAgent is a fake launcher for heal runs: act runs as the agent process.
type healAgent struct {
	mu    sync.Mutex
	calls int
	svc   *Service
	act   func(s *Service, runID string, req AgentRequest) error
	reqs  []AgentRequest
}

func (h *healAgent) Start(req AgentRequest) (string, error) {
	h.mu.Lock()
	h.calls++
	h.reqs = append(h.reqs, req)
	h.mu.Unlock()
	m := runIDInPrompt.FindStringSubmatch(req.Prompt)
	if m == nil {
		return "", errors.New("no run id in prompt")
	}
	go func() { req.OnDone(h.act(h.svc, m[1], req)) }()
	return "heal-agent", nil
}
func (h *healAgent) Cancel(string) {}

func fixS2(t *testing.T) func(s *Service, runID string, req AgentRequest) error {
	return func(s *Service, runID string, _ AgentRequest) error {
		if _, err := s.StepStart(runID, "s2"); err != nil { // announcing the healing step is allowed
			return err
		}
		_, _ = s.Substep(runID, "s2", "Tapped Add to cart")
		_, _ = s.Assert(runID, "s2", AssertResult{Text: "Cart badge", Passed: true})
		if _, err := s.StepDone(runID, "s2", true, "fixed", ""); err != nil {
			return err
		}
		_, err := s.SubmitCache(runID, []Segment{mkSeg(t, segFixed)})
		return err
	}
}

func TestAutoMode(t *testing.T) {
	// all fresh -> replay, no agent
	s, _, pid := newReplaySvc(t)
	s.Launcher = forbidLauncher{t}
	r, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1"})
	if err != nil || r.Mode != ModeReplay || r.Heal {
		t.Fatalf("auto fresh: %+v %v", r, err)
	}
	if got := waitFinished(t, s, r.ID); got.Status != RunPassed {
		t.Fatalf("%+v", got)
	}

	// mixed without an agent -> replay that fails naming the segments
	s2, _, pid2 := newReplaySvc(t, segLogin, segCart)
	r, err = s2.StartRun(StartRunRequest{Project: pid2, Case: "checkout", DeviceID: "dev1"})
	if err != nil || r.Mode != ModeReplay {
		t.Fatalf("auto mixed: %+v %v", r, err)
	}
	if got := waitFinished(t, s2, r.ID); got.Status != RunFailed || !strings.Contains(got.Error, "missing segments: s2") {
		t.Fatalf("%+v", got)
	}

	// mixed with an agent -> the agent records only the missing segment
	s3, _, pid3 := newReplaySvc(t, segLogin, segCart)
	ha := &healAgent{svc: s3, act: fixS2(t)}
	s3.Launcher = ha
	r, err = s3.StartRun(StartRunRequest{Project: pid3, Case: "checkout", DeviceID: "dev1"})
	if err != nil || r.Mode != ModeReplay || !r.Heal {
		t.Fatalf("auto mixed+agent: %+v %v", r, err)
	}
	if got := waitFinished(t, s3, r.ID); got.Status != RunPassed || len(got.Healed) != 1 || ha.calls != 1 {
		t.Fatalf("%+v calls=%d", got, ha.calls)
	}

	// nothing fresh -> record
	s4, _, pid4 := newSvc(t)
	r, err = s4.StartRun(StartRunRequest{Project: pid4, Case: "checkout", DeviceID: "dev1", Agent: AgentExternal})
	if err != nil || r.Mode != ModeRecord {
		t.Fatalf("auto none: %+v %v", r, err)
	}
}

func TestNormalizeIdempotent(t *testing.T) {
	in := mkSeg(t, `{"id":"s1","steps":[
		{"action":"launch_app","bundleId":"com.x"},
		{"action":"tap","selector":{"text":"A"}},
		{"action":"wait_stable"},
		{"action":"type","text":"x"},
		{"action":"assert_exists","selector":{"text":"B"}},
		{"action":"observe"},
		{"action":"swipe"}]}`)
	once := NormalizeSegment(in)
	var acts []string
	for _, r := range once.Steps {
		acts = append(acts, rawAction(r))
	}
	want := "launch_app wait_stable tap wait_stable type assert_exists observe swipe wait_stable"
	if strings.Join(acts, " ") != want {
		t.Fatalf("got %s\nwant %s", strings.Join(acts, " "), want)
	}
	twice := NormalizeSegment(once)
	a, _ := json.Marshal(once.Steps)
	b, _ := json.Marshal(twice.Steps)
	if string(a) != string(b) {
		t.Fatalf("not idempotent:\n%s\n%s", a, b)
	}
	if len(in.Steps) != 7 {
		t.Fatal("input modified")
	}
}

func TestSubmitCacheNormalizes(t *testing.T) {
	s, _, pid := newSvc(t)
	r := startExternal(t, s, pid, "dev1")
	passSteps(t, s, r.ID, "s1", "s2", "s3")
	res, err := s.SubmitCache(r.ID, []Segment{mkSeg(t, segLogin), mkSeg(t, segAdd), mkSeg(t, segCart)})
	if err != nil {
		t.Fatal(err)
	}
	// s1 ends with a tap: one wait_stable is appended; s2 already waits; s3's
	// tap is followed by an assertion, which waits itself.
	for i, want := range []int{4, 2, 2} {
		if n := len(res.Cache.Segments[i].Steps); n != want {
			t.Fatalf("segment %d has %d steps, want %d: %s", i, n, want, res.Cache.Segments[i].Steps)
		}
	}
}

func TestLint(t *testing.T) {
	c := mustParse(t, sample)
	cache := &Cache{Segments: []Segment{
		mkSeg(t, `{"id":"s1","steps":[{"action":"tap","x":10,"y":20},{"action":"tap","selector":{"text":"ok"}},{"action":"wait","ms":2000}]}`),
		mkSeg(t, `{"id":"s2","steps":[{"action":"sleep","ms":2500},{"action":"long_press","x":1,"y":2,"selector":{"text":"a"}}]}`),
	}}
	w := Lint(c, cache)
	if len(w) != 2 || w[0].Step != "s1" || w[0].Path != "0" || !strings.Contains(w[0].Message, "raw coordinates") ||
		w[1].Step != "s2" || w[1].Path != "0" || !strings.Contains(w[1].Message, "2500") {
		t.Fatalf("warnings: %+v", w)
	}
	if got := Lint(c, nil); len(got) != 0 {
		t.Fatalf("nil cache: %+v", got)
	}
}

func TestLintRoute(t *testing.T) {
	s, _, pid := newReplaySvc(t, `{"id":"s1","steps":[{"action":"tap","x":1,"y":2},{"action":"wait_stable"}]}`)
	h := s.Routes()
	w := doReq(t, h, "GET", "/cases/"+pid+"/checkout/lint", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) || !strings.Contains(w.Body.String(), "raw coordinates") {
		t.Fatalf("lint: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "GET", "/cases/"+pid+"/nope/lint", ""); w.Code != 404 {
		t.Fatalf("missing: %d", w.Code)
	}
}
