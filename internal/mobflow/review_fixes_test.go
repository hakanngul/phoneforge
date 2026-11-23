package mobflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// During a heal every agent step call for another step is refused, naming the
// step being repaired; the repair itself still works.
func TestHealRestrictsAgentToHealingStep(t *testing.T) {
	s, _, pid := newReplaySvc(t, segLogin, segBroken, segCart)
	var errs []error
	s.Launcher = &healAgent{svc: s, act: func(s *Service, runID string, req AgentRequest) error {
		_, e1 := s.StepStart(runID, "s3")
		_, e2 := s.Substep(runID, "s3", "x")
		_, e3 := s.Assert(runID, "s3", AssertResult{Text: "x", Passed: true})
		_, e4 := s.StepDone(runID, "s3", true, "", "")
		_, e5 := s.StepStart(runID, "s1")
		_, e6 := s.SubmitCache(runID, []Segment{mkSeg(t, segCart)})
		errs = []error{e1, e2, e3, e4, e5, e6}
		return fixS2(t)(s, runID, req)
	}}
	run := startReplay(t, s, pid, func(r *StartRunRequest) { r.Heal = true })
	if len(errs) != 6 {
		t.Fatalf("agent did not run: %v", errs)
	}
	for i, err := range errs {
		var se *StateError
		if !asStateErr(err, &se) || !strings.Contains(err.Error(), `"s2"`) {
			t.Errorf("call %d: want a StateError naming s2, got %v", i+1, err)
		}
	}
	if run.Status != RunPassed || run.Steps[2].State != StepPassed || run.Steps[0].State != StepPassed {
		t.Fatalf("run: %+v", run)
	}
}

func asStateErr(err error, target **StateError) bool {
	se, ok := err.(*StateError)
	if ok {
		*target = se
	}
	return ok
}

// An unexpected state-machine error inside the replay loop fails the run and
// releases the device instead of leaving it running.
func TestReplayLoopErrorFinishesRun(t *testing.T) {
	slow := `{"id":"s2","steps":[{"action":"wait","ms":300}]}`
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
		time.Sleep(5 * time.Millisecond)
	}
	// Someone else finishes the step the replayer is running.
	if _, err := s.StepDone(run.ID, "s2", true, "", ""); err != nil {
		t.Fatal(err)
	}
	got := waitFinished(t, s, run.ID)
	if got.Status != RunFailed || !strings.Contains(got.Error, "replay stopped") {
		t.Fatalf("run: %+v", got)
	}
	if _, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "replay"}); err != nil {
		t.Fatalf("device lock not released: %v", err)
	}
}

// PUT /cache applies the same secret checks as an agent submission.
func TestPutCacheRouteChecksSecrets(t *testing.T) {
	s, _, pid := newSvc(t)
	s.Secrets = func(string) (string, error) { return fakeSecret, nil }
	h := s.Routes()
	base := "/cases/" + pid + "/checkout/cache"
	seg := func(steps string) string { return `{"segments":[{"id":"s1","steps":` + steps + `}]}` }
	for name, body := range map[string]string{
		"literal":    seg(`[{"action":"type","text":"` + fakeSecret + `","selector":{"type":"input"}}]`),
		"undeclared": seg(`[{"action":"type_secret","secretId":"other","selector":{"type":"input"}}]`),
	} {
		w := doReq(t, h, "PUT", base, body)
		if w.Code != 400 || strings.Contains(w.Body.String(), fakeSecret) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if _, err := s.Store.GetCache(pid, "checkout", ""); err == nil {
		t.Fatal("rejected PUT wrote a cache")
	}
	ok := seg(`[{"action":"type_secret","secretId":"shop_password","selector":{"type":"input"}}]`)
	if w := doReq(t, h, "PUT", base, ok); w.Code != 200 {
		t.Fatalf("valid: %d %s", w.Code, w.Body)
	}
}

// A case edited while a run was going: the submitted segment keeps the hash of
// the case the run started with, so the edited step shows as stale.
func TestSubmitCacheStampsSnapshotHashes(t *testing.T) {
	s, _, pid := newSvc(t)
	run := startExternal(t, s, pid, "dev1")
	passSteps(t, s, run.ID, "s1", "s2", "s3")
	edited := strings.Replace(sample, "Search for ${item}", "Look for ${item}", 1)
	if _, _, err := s.Store.Save(pid, "checkout", []byte(edited)); err != nil {
		t.Fatal(err)
	}
	res, err := s.SubmitCache(run.ID, rawSegs(t, allSegs))
	if err != nil {
		t.Fatal(err)
	}
	if res.Staleness["s2"] != StateStale || res.Staleness["s1"] != StateFresh || res.Staleness["s3"] != StateFresh {
		t.Fatalf("staleness %v", res.Staleness)
	}
	if !strings.Contains(res.Notes, "s2") {
		t.Fatalf("notes %q", res.Notes)
	}
	if len(res.Cache.Segments) != 3 {
		t.Fatalf("segment must still be written: %+v", res.Cache)
	}
}

func TestHealSubmitStampsSnapshotHash(t *testing.T) {
	s, _, pid := newReplaySvc(t, segLogin, segBroken, segCart)
	edited := strings.Replace(sample, "Search for ${item}", "Look for ${item}", 1)
	s.Launcher = &healAgent{svc: s, act: func(s *Service, runID string, req AgentRequest) error {
		if _, _, err := s.Store.Save(pid, "checkout", []byte(edited)); err != nil {
			return err
		}
		return fixS2(t)(s, runID, req)
	}}
	run := startReplay(t, s, pid, func(r *StartRunRequest) { r.Heal = true })
	if run.Status != RunPassed {
		t.Fatalf("run: %+v", run)
	}
	cache, err := s.Store.GetCache(pid, "checkout", "")
	if err != nil {
		t.Fatal(err)
	}
	st := Staleness(mustCase(t, s, pid), cache)
	if st["s2"] != StateStale {
		t.Fatalf("healed segment of an edited step must be stale: %v", st)
	}
}

// Without ?platform= the case's pinned platform selects the sidecar.
func TestCacheRoutesDefaultToCasePlatform(t *testing.T) {
	st, pid := newTestStore(t)
	android := strings.Replace(strings.Replace(sample, "platform: any}\ndevice", "platform: android}\ndevice", 1), "device: {platform: any", "device: {platform: any", 1)
	if _, _, err := st.Save(pid, "andr", []byte(android)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Save(pid, "anycase", []byte(sample)); err != nil {
		t.Fatal(err)
	}
	seg := []Segment{mkSeg(t, `{"id":"s1","steps":[{"action":"tap","selector":{"text":"x"}}]}`)}
	if _, err := st.PutCache(pid, "andr", "android", Cache{Segments: seg}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCache(pid, "anycase", "", Cache{Segments: seg}); err != nil {
		t.Fatal(err)
	}
	h := (&Service{Store: st}).Routes()
	get := func(path string) (cv struct {
		Cache *Cache `json:"cache"`
	}, code int) {
		w := doReq(t, h, "GET", path, "")
		_ = json.Unmarshal(w.Body.Bytes(), &cv)
		return cv, w.Code
	}
	cv, code := get("/cases/" + pid + "/andr/cache")
	if code != 200 || cv.Cache == nil || cv.Cache.Platform != "android" {
		t.Fatalf("android case: %d %+v", code, cv.Cache)
	}
	if hh := doReq(t, h, "GET", "/cases/"+pid+"/andr/cache/history", ""); hh.Code != 200 {
		t.Fatalf("history: %d", hh.Code)
	}
	cv, code = get("/cases/" + pid + "/anycase/cache")
	if code != 200 || cv.Cache == nil || cv.Cache.Platform != "" {
		t.Fatalf("any case: %d %+v", code, cv.Cache)
	}
	if w := doReq(t, h, "DELETE", "/cases/"+pid+"/andr/cache", ""); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	if _, err := st.GetCache(pid, "andr", "android"); err == nil {
		t.Fatal("android sidecar should be gone")
	}
}
