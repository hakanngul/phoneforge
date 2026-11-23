package mobflow

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReplayHealRepairsSegmentOnce(t *testing.T) {
	s, _, pid := newReplaySvc(t, segLogin, segBroken, segCart)
	ha := &healAgent{svc: s}
	ha.act = fixS2(t)
	s.Launcher = ha
	run := startReplay(t, s, pid, func(r *StartRunRequest) { r.Heal = true })
	if run.Status != RunPassed || len(run.Healed) != 1 || run.Healed[0] != "s2" || !run.Heal {
		t.Fatalf("run: %+v", run)
	}
	if run.Steps[1].State != StepHealed || run.Steps[1].Error != "" || run.Steps[2].State != StepPassed {
		t.Fatalf("steps: %+v", run.Steps)
	}
	if ha.calls != 1 {
		t.Fatalf("agent called %d times", ha.calls)
	}
	p := ha.reqs[0].Prompt
	if !strings.Contains(p, "Nope") || !strings.Contains(p, "fail-s2.png") || !strings.Contains(p, "DSL step 1 (tap)") {
		t.Fatalf("heal prompt lacks failing DSL, screenshot or error:\n%s", p)
	}
	cache, err := s.Store.GetCache(pid, "checkout", "")
	if err != nil {
		t.Fatal(err)
	}
	if cache.Version != 2 {
		t.Fatalf("version %d", cache.Version)
	}
	var s2 Segment
	for _, sg := range cache.Segments {
		if sg.ID == "s2" {
			s2 = sg
		}
	}
	if s2.HealedAt == nil || !strings.Contains(string(s2.Steps[0]), "Add to cart") {
		t.Fatalf("segment: %+v", s2)
	}
	if len(s2.Steps) != 2 || !strings.Contains(string(s2.Steps[1]), "wait_stable") {
		t.Fatalf("heal submit was not normalised: %s", s2.Steps)
	}
	if h, _ := s.Store.ListHistory(pid, "checkout", ""); len(h) != 1 || h[0].Version != 1 {
		t.Fatalf("history: %+v", h)
	}
	if st := Staleness(mustCase(t, s, pid), cache); st["s1"] != StateFresh || st["s2"] != StateFresh || st["s3"] != StateFresh {
		t.Fatalf("staleness %v", st)
	}
	// the next replay needs no agent
	s.Launcher = forbidLauncher{t}
	if r := startReplay(t, s, pid, nil); r.Status != RunPassed {
		t.Fatalf("second replay: %+v", r)
	}
}

func TestReplayHealFailureKeepsCache(t *testing.T) {
	cases := map[string]func(s *Service, runID string, req AgentRequest) error{
		"agent reports failure": func(s *Service, runID string, _ AgentRequest) error {
			_, err := s.StepDone(runID, "s2", false, "", "button is gone")
			return err
		},
		"agent exits silently": func(*Service, string, AgentRequest) error { return nil },
		"agent crashes":        func(*Service, string, AgentRequest) error { return errors.New("boom") },
		"no cache submitted": func(s *Service, runID string, _ AgentRequest) error {
			_, err := s.StepDone(runID, "s2", true, "", "")
			return err
		},
		"invalid segment": func(s *Service, runID string, _ AgentRequest) error {
			if _, err := s.StepDone(runID, "s2", true, "", ""); err != nil {
				return err
			}
			_, err := s.SubmitCache(runID, []Segment{mkSeg(t, `{"id":"s2","steps":[{"action":"no_such_action"}]}`)})
			return err
		},
	}
	for name, act := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, pid := newReplaySvc(t, segLogin, segBroken, segCart)
			s.Launcher = &healAgent{svc: s, act: act}
			run := startReplay(t, s, pid, func(r *StartRunRequest) { r.Heal = true })
			if run.Status != RunFailed || len(run.Healed) != 0 {
				t.Fatalf("run: %+v", run)
			}
			if run.Steps[1].State != StepFailed || run.Steps[2].State != StepSkipped {
				t.Fatalf("steps: %+v", run.Steps)
			}
			cache, _ := s.Store.GetCache(pid, "checkout", "")
			if cache.Version != 1 || !strings.Contains(string(cache.Segments[1].Steps[0]), "Nope") || cache.Segments[1].HealedAt != nil {
				t.Fatalf("cache changed: %+v", cache)
			}
			if h, _ := s.Store.ListHistory(pid, "checkout", ""); len(h) != 0 {
				t.Fatalf("history: %+v", h)
			}
		})
	}
}

func TestReplayHealTimeout(t *testing.T) {
	s, _, pid := newReplaySvc(t, segLogin, segBroken, segCart)
	s.HealTimeout = 80 * time.Millisecond
	block := make(chan struct{})
	defer close(block)
	s.Launcher = &healAgent{svc: s, act: func(*Service, string, AgentRequest) error { <-block; return nil }}
	run := startReplay(t, s, pid, func(r *StartRunRequest) { r.Heal = true })
	if run.Status != RunFailed || !strings.Contains(run.Error, "did not finish") {
		t.Fatalf("run: %+v", run)
	}
}

func TestReplayHealNeedsLauncher(t *testing.T) {
	s, _, pid := newReplaySvc(t)
	_, err := s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "replay", Heal: true})
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("err %v", err)
	}
	s.Launcher = forbidLauncher{t}
	_, err = s.StartRun(StartRunRequest{Project: pid, Case: "checkout", DeviceID: "dev1", Mode: "replay", Heal: true, Agent: AgentExternal})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err %v", err)
	}
}

func mustCase(t *testing.T, s *Service, pid string) *Case {
	t.Helper()
	e, err := s.Store.Get(pid, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	return e.Case
}
