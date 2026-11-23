package mobflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pocketlab/internal/events"
)

func TestDefaultAgentAliasAndRunEvent(t *testing.T) {
	s, f, pid := newAgentSvc(t, func(s *Service, id string, _ AgentRequest) error {
		if _, err := s.StepStart(id, "s1"); err != nil {
			return err
		}
		_, err := s.StepDone(id, "s1", false, "", "login screen never appeared")
		return err
	})
	ch, unsub := events.Subscribe()
	defer unsub()
	run, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "record", Agent: "default"})
	if err != nil {
		t.Fatal(err)
	}
	f.runIDs <- run.ID
	if run.Agent != "" {
		t.Fatalf("agent %q, want the empty default", run.Agent)
	}
	f.mu.Lock()
	got := f.req.Agent
	f.mu.Unlock()
	if got != "" {
		t.Fatalf("launcher got agent %q", got)
	}
	done := waitFinished(t, s, run.ID)
	if done.Status != RunFailed {
		t.Fatalf("status %s", done.Status)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-ch:
			if !strings.Contains(ev.Message, run.ID) {
				continue
			}
			if ev.Level != "error" || !strings.Contains(ev.Message, "Flow run failed: "+run.CaseID) ||
				!strings.Contains(ev.Message, "status=failed") || !strings.Contains(ev.Message, "failedStep=s1") {
				t.Fatalf("event %+v", ev)
			}
			return
		case <-deadline:
			t.Fatal("no flow run event")
		}
	}
}

func TestRunEventDetailPassedHealed(t *testing.T) {
	r := &Run{ID: "abc", Status: RunPassed, Healed: []string{"s2", "s3"}, Steps: []StepRun{{ID: "s1", State: StepPassed}}}
	if d := runEventDetail(r); d != " [run=abc status=passed healed=s2,s3]" {
		t.Fatalf("detail %q", d)
	}
}

func TestCachePlatformForAnyCase(t *testing.T) {
	s, _, pid := newReplaySvc(t) // android device, default sidecar present
	c := mustParse(t, sample)    // platform any
	if p := lookupCachePlatform(c, "android"); p != "android" {
		t.Fatalf("lookup %q", p)
	}
	if p := s.writeCachePlatform(pid, "checkout", c, "android"); p != "" {
		t.Fatalf("no android sidecar yet: write platform %q, want default", p)
	}
	record := func() {
		t.Helper()
		r, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "record", Agent: AgentExternal})
		if err != nil {
			t.Fatal(err)
		}
		if r.Platform != "android" {
			t.Fatalf("run platform %q", r.Platform)
		}
		passSteps(t, s, r.ID, "s1", "s2", "s3")
		if _, err := s.SubmitCache(r.ID, rawSegs(t, allSegs)); err != nil {
			t.Fatal(err)
		}
	}
	record()
	if _, err := s.Store.GetCache(pid, "checkout", "android"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.Store.GetCache(pid, "checkout", "android"); cur.Platform != "" || cur.Version != 2 {
		t.Fatalf("record should have updated the default sidecar: %+v", cur)
	}
	// Once an android sidecar exists, records go there and replay reads it.
	putPlatform := func() {
		var in []Segment
		for _, js := range []string{segLogin, segAdd, segCart} {
			in = append(in, mkSeg(t, js))
		}
		if _, err := s.Store.PutCache(pid, "checkout", "android", Cache{Segments: in}); err != nil {
			t.Fatal(err)
		}
	}
	putPlatform()
	record()
	a, _ := s.Store.GetCache(pid, "checkout", "android")
	d, _ := s.Store.GetCache(pid, "checkout", "")
	if a.Platform != "android" || a.Version != 2 || d.Version != 2 {
		t.Fatalf("android v%d (%q), default v%d", a.Version, a.Platform, d.Version)
	}
}

func TestStatsAndHistoryEntryRoute(t *testing.T) {
	s, _, pid := newSvc(t)
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	id := pid + "/checkout"
	base := time.Now().UTC()
	save := func(i int, status string, steps []StepRun, healed []string) {
		r := &Run{ID: newRunID(base.Add(time.Duration(i) * time.Second)), CaseID: id, Project: pid, Name: "checkout",
			Mode: ModeReplay, Status: status, StartedAt: base, Healed: healed, Steps: steps}
		if err := rt.store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	save(1, RunPassed, []StepRun{{ID: "s1", State: StepPassed}}, nil)
	save(2, RunFailed, []StepRun{{ID: "s1", State: StepPassed}, {ID: "s2", State: StepFailed}}, nil)
	save(3, RunPassed, []StepRun{{ID: "s2", State: StepHealed}}, []string{"s2"})
	save(4, RunCancelled, nil, nil)
	st, err := s.Stats(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Runs != 3 || st.Passed != 2 || st.Failed != 1 || st.Heals != 1 || st.StepFailures["s2"] != 1 || st.PassRate < 0.66 || st.PassRate > 0.67 {
		t.Fatalf("stats %+v", st)
	}
	if st, _ := s.Stats(id, 1); st.Runs != 1 || st.Passed != 1 {
		t.Fatalf("limit 1: %+v", st)
	}
	h := s.Routes()
	w := doReq(t, h, "GET", "/cases/"+id+"/stats?limit=2", "")
	var got Stats
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Runs != 2 {
		t.Fatalf("stats route: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "GET", "/cases/"+id+"/stats?limit=x", ""); w.Code != 400 {
		t.Fatalf("bad limit: %d", w.Code)
	}

	// history entry content
	var segs []Segment
	for _, js := range []string{segLogin, segAdd, segCart} {
		segs = append(segs, mkSeg(t, js))
	}
	if _, err := s.Store.PutCache(pid, "checkout", "", Cache{Segments: segs}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.PutCache(pid, "checkout", "", Cache{Segments: segs[:2]}); err != nil {
		t.Fatal(err)
	}
	hist, err := s.Store.ListHistory(pid, "checkout", "")
	if err != nil || len(hist) != 1 {
		t.Fatalf("history %v %v", hist, err)
	}
	w = doReq(t, h, "GET", "/cases/"+id+"/cache/history/"+hist[0].ID, "")
	var hv struct{ Cache *Cache }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &hv) != nil || hv.Cache == nil || len(hv.Cache.Segments) != 3 {
		t.Fatalf("history entry: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "GET", "/cases/"+id+"/cache/history/nope", ""); w.Code != 400 && w.Code != 404 {
		t.Fatalf("bad entry: %d", w.Code)
	}
}
