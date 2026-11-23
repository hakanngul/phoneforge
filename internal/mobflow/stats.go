package mobflow

// DefaultStatsRuns is the number of most recent finished runs a case's
// flakiness stats cover.
const DefaultStatsRuns = 20

// Stats summarises the recent run history of a case.
type Stats struct {
	CaseID string `json:"caseId"`
	// Window is the maximum number of runs considered.
	Window int `json:"window"`
	// Runs is the number of finished (passed or failed) runs counted.
	Runs   int `json:"runs"`
	Passed int `json:"passed"`
	Failed int `json:"failed"`
	// PassRate is passed/runs in [0,1]; 0 when there are no runs.
	PassRate float64 `json:"passRate"`
	// Heals is the number of healed steps across the counted runs.
	Heals int `json:"heals"`
	// StepFailures counts failed steps by step id.
	StepFailures map[string]int `json:"stepFailures"`
}

// Stats computes flakiness stats from the last limit finished runs of a case
// (limit <= 0 means DefaultStatsRuns). Running and cancelled runs are ignored.
func (s *Service) Stats(caseID string, limit int) (*Stats, error) {
	if limit <= 0 {
		limit = DefaultStatsRuns
	}
	runs, err := s.ListRuns(caseID, MaxRuns)
	if err != nil {
		return nil, err
	}
	st := &Stats{CaseID: caseID, Window: limit, StepFailures: map[string]int{}}
	for _, r := range runs { // newest first
		if st.Runs >= limit {
			break
		}
		if r.Status != RunPassed && r.Status != RunFailed {
			continue
		}
		st.Runs++
		if r.Status == RunPassed {
			st.Passed++
		} else {
			st.Failed++
		}
		st.Heals += len(r.Healed)
		for _, sr := range r.Steps {
			if sr.State == StepFailed {
				st.StepFailures[sr.ID]++
			}
		}
	}
	if st.Runs > 0 {
		st.PassRate = float64(st.Passed) / float64(st.Runs)
	}
	return st, nil
}
