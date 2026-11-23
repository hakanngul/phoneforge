package mobflow

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeShots struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeShots) fn(_ context.Context, deviceID string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, deviceID)
	if f.err != nil {
		return nil, f.err
	}
	return []byte("\x89PNG-fake"), nil
}

// newSvc returns a service over a temp store with the sample case saved.
func newSvc(t *testing.T) (*Service, *fakeShots, string) {
	t.Helper()
	st, pid := newTestStore(t)
	if _, _, err := st.Save(pid, "checkout", []byte(sample)); err != nil {
		t.Fatal(err)
	}
	shots := &fakeShots{}
	return &Service{Store: st, ScreenshotFunc: shots.fn}, shots, pid
}

func startExternal(t *testing.T, s *Service, pid, dev string) *Run {
	t.Helper()
	r, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: dev, Mode: "record", Agent: AgentExternal})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunStateMachine(t *testing.T) {
	now := time.Now()
	mk := func() *Run {
		return &Run{ID: "r", Status: RunRunning, Mode: ModeRecord, Steps: []StepRun{
			{ID: "s1", State: StepPending}, {ID: "s2", State: StepPending}, {ID: "s3", State: StepPending}}}
	}
	wantErr := func(t *testing.T, err error, subs ...string) {
		t.Helper()
		if err == nil {
			t.Fatal("expected an error")
		}
		var se *StateError
		if !errors.As(err, &se) && !errors.Is(err, ErrInvalid) {
			t.Fatalf("not a state/invalid error: %T %v", err, err)
		}
		for _, s := range subs {
			if !strings.Contains(err.Error(), s) {
				t.Fatalf("error %q lacks %q", err, s)
			}
		}
	}

	t.Run("happy path", func(t *testing.T) {
		r := mk()
		for _, id := range []string{"s1", "s2", "s3"} {
			if err := r.stepStart(id, now); err != nil {
				t.Fatal(err)
			}
			if _, err := r.substep(id, "tap", now); err != nil {
				t.Fatal(err)
			}
			if err := r.assert(id, AssertResult{Text: "ok", Passed: true}); err != nil {
				t.Fatal(err)
			}
			if _, done, err := r.stepDone(id, true, "fine", "", now.Add(time.Second)); err != nil || done {
				t.Fatalf("done %s: %v %v", id, done, err)
			}
		}
		if !r.allPassed() || r.Status != RunRunning {
			t.Fatalf("record run must wait for the cache: %+v", r)
		}
		if r.Steps[0].DurationMs != 1000 || len(r.Steps[0].Substeps) != 1 || len(r.Steps[0].Asserts) != 1 {
			t.Fatalf("step data: %+v", r.Steps[0])
		}
	})
	t.Run("unknown step names valid ids", func(t *testing.T) {
		wantErr(t, mk().stepStart("nope", now), `unknown step "nope"`, "s1, s2, s3")
	})
	t.Run("out of order", func(t *testing.T) {
		wantErr(t, mk().stepStart("s2", now), "out of order", `next pending step is "s1"`)
	})
	t.Run("start while another runs", func(t *testing.T) {
		r := mk()
		_ = r.stepStart("s1", now)
		wantErr(t, r.stepStart("s2", now), `"s1" is still running`)
	})
	t.Run("substep and assert need the running step", func(t *testing.T) {
		r := mk()
		_, err := r.substep("s1", "x", now)
		wantErr(t, err, "not running", "workflow_step_start")
		_ = r.stepStart("s1", now)
		_, err = r.substep("s2", "x", now)
		wantErr(t, err, `running step is "s1"`)
		wantErr(t, r.assert("s2", AssertResult{Text: "x"}), `running step is "s1"`)
	})
	t.Run("double done", func(t *testing.T) {
		r := mk()
		_ = r.stepStart("s1", now)
		_, _, _ = r.stepDone("s1", true, "", "", now)
		_, _, err := r.stepDone("s1", true, "", "", now)
		wantErr(t, err, "already done")
		_, _, err = r.stepDone("s2", true, "", "", now)
		wantErr(t, err, "not running")
	})
	t.Run("passed=false fails the run", func(t *testing.T) {
		r := mk()
		_ = r.stepStart("s1", now)
		_, _, _ = r.stepDone("s1", true, "", "", now)
		_ = r.stepStart("s2", now)
		st, done, err := r.stepDone("s2", false, "button missing", "", now)
		if err != nil || !done || st.State != StepFailed || st.Error != "button missing" {
			t.Fatalf("%+v %v %v", st, done, err)
		}
		if r.Status != RunFailed || !strings.Contains(r.Error, "step s2 failed") || r.Steps[2].State != StepSkipped {
			t.Fatalf("run: %+v", r)
		}
		wantErr(t, r.stepStart("s3", now), "already failed")
	})
}

func TestServiceHappyPathAndScreenshots(t *testing.T) {
	s, shots, pid := newSvc(t)
	run := startExternal(t, s, pid, "dev1")
	if run.Status != RunRunning || len(run.Steps) != 3 || run.Params["email"] != "qa@example.com" {
		t.Fatalf("run: %+v", run)
	}
	for _, id := range []string{"s1", "s2", "s3"} {
		if _, err := s.StepStart(run.ID, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Substep(run.ID, id, "did "+id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.StepDone(run.ID, id, true, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.GetRun(run.ID)
	if got.Status != RunRunning {
		t.Fatalf("status %s, want running until the cache arrives", got.Status)
	}
	if len(shots.calls) != 3 || shots.calls[0] != "dev1" {
		t.Fatalf("screenshots: %v", shots.calls)
	}
	rt, _ := s.runtime()
	for _, st := range got.Steps {
		if st.Screenshot != "step-"+st.ID+".png" {
			t.Fatalf("step %s screenshot %q", st.ID, st.Screenshot)
		}
		b, err := os.ReadFile(filepath.Join(rt.store.Dir(run.ID), st.Screenshot))
		if err != nil || !strings.HasPrefix(string(b), "\x89PNG") {
			t.Fatalf("screenshot file: %v", err)
		}
	}
	// A finished run frees the device.
	if _, err := s.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "record", Agent: AgentExternal}); err != nil {
		t.Fatalf("device not released: %v", err)
	}
}

func TestScreenshotFailureDoesNotFailRun(t *testing.T) {
	s, shots, pid := newSvc(t)
	shots.err = errors.New("device gone")
	run := startExternal(t, s, pid, "dev1")
	_, _ = s.StepStart(run.ID, "s1")
	if _, err := s.StepDone(run.ID, "s1", true, "", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRun(run.ID)
	if got.Steps[0].State != StepPassed || got.Steps[0].Screenshot != "" {
		t.Fatalf("%+v", got.Steps[0])
	}
}

func TestFailureScreenshotAndSkips(t *testing.T) {
	s, shots, pid := newSvc(t)
	run := startExternal(t, s, pid, "dev1")
	_, _ = s.StepStart(run.ID, "s1")
	got, err := s.StepDone(run.ID, "s1", false, "no login button", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != RunFailed || got.Steps[0].Screenshot == "" || got.Steps[1].State != StepSkipped || got.EndedAt == nil {
		t.Fatalf("%+v", got)
	}
	if len(shots.calls) != 1 {
		t.Fatalf("calls %v", shots.calls)
	}
}

func TestFinishOpenRunIsIdempotent(t *testing.T) {
	s, _, pid := newSvc(t)
	run := startExternal(t, s, pid, "dev1")
	_, _ = s.StepStart(run.ID, "s1")
	got, err := s.Finish(run.ID, "")
	if err != nil || got.Status != RunFailed || got.Error != "agent exited before finishing" {
		t.Fatalf("%+v %v", got, err)
	}
	if got.Steps[0].State != StepFailed || got.Steps[1].State != StepSkipped {
		t.Fatalf("steps: %+v", got.Steps)
	}
	again, err := s.Finish(run.ID, "other reason")
	if err != nil || again.Error != got.Error || again.Status != RunFailed {
		t.Fatalf("second finish changed the run: %+v %v", again, err)
	}
	// persisted
	rt, _ := s.runtime()
	disk, err := rt.store.Get(run.ID)
	if err != nil || disk.Status != RunFailed {
		t.Fatalf("disk: %+v %v", disk, err)
	}
}

func TestFinishAllPassedWithoutCache(t *testing.T) {
	s, _, pid := newSvc(t)
	run := startExternal(t, s, pid, "dev1")
	for _, id := range []string{"s1", "s2", "s3"} {
		_, _ = s.StepStart(run.ID, id)
		_, _ = s.StepDone(run.ID, id, true, "", "")
	}
	got, _ := s.Finish(run.ID, "")
	if got.Status != RunFailed || !strings.Contains(got.Error, "no cache submitted") {
		t.Fatalf("%+v", got)
	}
}

func TestRunPersistenceListAndPrune(t *testing.T) {
	s, _, pid := newSvc(t)
	rt, _ := s.runtime()
	rt.store.cap = 3
	var ids []string
	for i := 0; i < 5; i++ {
		r := startExternal(t, s, pid, "dev1")
		ids = append(ids, r.ID)
		_, _ = s.Cancel(r.ID)
		time.Sleep(2 * time.Millisecond)
	}
	list, _ := s.ListRuns(pid+"/checkout", 10)
	if len(list) != 3 || list[0].ID != ids[4] {
		t.Fatalf("list after prune: %d first=%v", len(list), list[0].ID)
	}
	if _, err := rt.store.Get(ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oldest run should be pruned: %v", err)
	}
	if l, _ := s.ListRuns("nope/none", 10); len(l) != 0 {
		t.Fatal("case filter")
	}
}

func TestRoutesRunsAndLock(t *testing.T) {
	s, _, pid := newSvc(t)
	h := s.Routes()
	body := `{"project":"` + pid + `","case":"checkout","deviceId":"dev1","mode":"record","agent":"external"}`
	w := doReq(t, h, "POST", "/runs", body)
	if w.Code != 202 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	var out struct{ Run Run }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	id := out.Run.ID
	// second run on the same device: 409; another device is fine
	if w := doReq(t, h, "POST", "/runs", body); w.Code != 409 || !strings.Contains(w.Body.String(), "device_busy") {
		t.Fatalf("lock: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "POST", "/runs", strings.Replace(body, "dev1", "dev2", 1)); w.Code != 202 {
		t.Fatalf("other device: %d %s", w.Code, w.Body)
	}
	// replay is subject to the same device lock
	if w := doReq(t, h, "POST", "/runs", strings.Replace(body, `"record"`, `"replay"`, 1)); w.Code != 409 {
		t.Fatalf("replay: %d", w.Code)
	}
	// bad requests
	if w := doReq(t, h, "POST", "/runs", `{"project":"`+pid+`","case":"checkout"}`); w.Code != 400 {
		t.Fatalf("no device: %d", w.Code)
	}
	if w := doReq(t, h, "POST", "/runs", strings.Replace(body, `"agent"`, `"params":{"zzz":"1"},"agent"`, 1)); w.Code != 400 {
		t.Fatalf("unknown param: %d", w.Code)
	}
	// step endpoints by run id
	base := "/runs/" + id + "/steps/"
	if w := doReq(t, h, "POST", base+"s2/start", ""); w.Code != 409 || !strings.Contains(w.Body.String(), `next pending step is \"s1\"`) {
		t.Fatalf("out of order: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "POST", base+"s1/start", ""); w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "POST", base+"s1/assert", `{"expectation":"x"}`); w.Code != 400 {
		t.Fatalf("assert without passed: %d", w.Code)
	}
	if w := doReq(t, h, "POST", base+"s1/done", `{"passed":true}`); w.Code != 200 {
		t.Fatalf("done: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "POST", base+"s1/done", `{"passed":true}`); w.Code != 409 {
		t.Fatalf("double done: %d", w.Code)
	}
	// the case now has two running runs: the active resolver refuses
	if w := doReq(t, h, "POST", "/cases/"+pid+"/checkout/active/steps/s2/start", ""); w.Code != 409 || !strings.Contains(w.Body.String(), "2 running runs") {
		t.Fatalf("ambiguous: %d %s", w.Code, w.Body)
	}
	_ = doReq(t, h, "POST", "/runs/"+id+"/cancel", "")
	if w := doReq(t, h, "POST", "/cases/"+pid+"/checkout/active/steps/s1/start", ""); w.Code != 200 {
		t.Fatalf("active resolver: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "POST", "/cases/"+pid+"/other/active/steps/s1/start", ""); w.Code != 409 || !strings.Contains(w.Body.String(), "no running run") {
		t.Fatalf("no run: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "GET", "/runs?case="+pid+"/checkout&limit=1", ""); w.Code != 200 {
		t.Fatalf("list: %d", w.Code)
	}
	if w := doReq(t, h, "GET", "/runs/"+id, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"cancelled"`) {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "GET", "/runs/0000000000000000-00000000", ""); w.Code != 404 {
		t.Fatalf("get unknown: %d", w.Code)
	}
	// artifacts: served from the run dir only
	if w := doReq(t, h, "GET", "/runs/"+id+"/artifacts/step-s1.png", ""); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "\x89PNG") {
		t.Fatalf("artifact: %d", w.Code)
	}
	for _, bad := range []string{"..%2Frun.json", "%2e%2e", ".hidden", "run.json%00", "a%2Fb"} {
		if w := doReq(t, h, "GET", "/runs/"+id+"/artifacts/"+bad, ""); w.Code == 200 && !strings.Contains(bad, "run.json%00") {
			t.Fatalf("artifact %q served: %d", bad, w.Code)
		}
	}
	if w := doReq(t, h, "GET", "/runs/"+id+"/artifacts/missing.png", ""); w.Code != 404 {
		t.Fatalf("missing artifact: %d", w.Code)
	}
}

func TestSSEStream(t *testing.T) {
	s, _, pid := newSvc(t)
	srv := httptest.NewServer(s.Routes())
	defer srv.Close()
	run := startExternal(t, s, pid, "dev1")
	_, _ = s.StepStart(run.ID, "s1") // buffered before the subscriber connects

	resp, err := http.Get(srv.URL + "/runs/" + run.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = s.Substep(run.ID, "s1", "tapped")
		_, _ = s.Assert(run.ID, "s1", AssertResult{Text: "shown", Passed: true})
		_, _ = s.StepDone(run.ID, "s1", false, "boom", "")
	}()
	var types []string
	sc := bufio.NewScanner(resp.Body)
	deadline := time.AfterFunc(5*time.Second, func() { resp.Body.Close() })
	defer deadline.Stop()
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			types = append(types, v)
		}
	}
	got := strings.Join(types, ",")
	for _, want := range []string{"run", "step", "substep", "assert", "screenshot", "done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("events %q lack %q", got, want)
		}
	}
	if types[len(types)-1] != "done" {
		t.Fatalf("stream must end with done: %q", got)
	}
	// a finished run streams run + done from disk
	resp2, err := http.Get(srv.URL + "/runs/" + run.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	sc = bufio.NewScanner(resp2.Body)
	n := 0
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "event: ") {
			n++
		}
	}
	if n == 0 {
		t.Fatal("no replay for a finished run")
	}
}
