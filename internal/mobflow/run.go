package mobflow

import (
	"fmt"
	"strings"
	"time"
)

// StateError is an invalid call for the current run/step state (HTTP 409). Its
// message names the valid ids so an agent can correct itself.
type StateError struct{ Msg string }

func (e *StateError) Error() string { return e.Msg }

func stateErrf(format string, a ...any) error { return &StateError{Msg: fmt.Sprintf(format, a...)} }

// Run modes.
const (
	ModeRecord = "record"
	ModeReplay = "replay"
	ModeHeal   = "heal"
)

// Run statuses.
const (
	RunRunning   = "running"
	RunPassed    = "passed"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

// Step states of a run.
const (
	StepPending = "pending"
	StepRunning = "running"
	StepPassed  = "passed"
	StepFailed  = "failed"
	StepSkipped = "skipped"
	StepHealing = "healing"
	StepHealed  = "healed"
)

// Substep is one meaningful action of a step, as reported by the agent.
type Substep struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// AssertResult is one assertion made while a step ran.
type AssertResult struct {
	Text   string `json:"text"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// StepRun is the state of one case step inside a run.
type StepRun struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	Type       string         `json:"type"`
	State      string         `json:"state"`
	StartedAt  *time.Time     `json:"startedAt,omitempty"`
	EndedAt    *time.Time     `json:"endedAt,omitempty"`
	DurationMs int64          `json:"durationMs"`
	Substeps   []Substep      `json:"substeps"`
	Asserts    []AssertResult `json:"asserts"`
	Screenshot string         `json:"screenshot,omitempty"` // file name inside the run directory
	Result     string         `json:"result,omitempty"`     // the agent's notes
	Error      string         `json:"error,omitempty"`
}

// Run is one execution of a case.
type Run struct {
	ID         string            `json:"id"`
	CaseID     string            `json:"caseId"`
	Project    string            `json:"project"`
	Name       string            `json:"name"`
	DeviceID   string            `json:"deviceId"`
	Platform   string            `json:"platform,omitempty"`
	Mode       string            `json:"mode"`
	Agent      string            `json:"agent,omitempty"`
	AgentRunID string            `json:"agentRunId,omitempty"`
	Params     map[string]string `json:"params,omitempty"`
	Status     string            `json:"status"`
	StartedAt  time.Time         `json:"startedAt"`
	EndedAt    *time.Time        `json:"endedAt,omitempty"`
	Error      string            `json:"error,omitempty"`
	Healed     []string          `json:"healed"`
	// Heal is set when a replay may hand a failing segment to an agent.
	Heal bool `json:"heal,omitempty"`
	// CacheSubmitted is set once a cache submission was accepted (record mode).
	CacheSubmitted bool      `json:"cacheSubmitted,omitempty"`
	Steps          []StepRun `json:"steps"`
}

// finished reports whether the run reached a terminal status.
func (r *Run) finished() bool { return r.Status != RunRunning }

func (r *Run) step(id string) *StepRun {
	for i := range r.Steps {
		if r.Steps[i].ID == id {
			return &r.Steps[i]
		}
	}
	return nil
}

func (r *Run) stepIDs(state string) []string {
	var out []string
	for _, s := range r.Steps {
		if state == "" || s.State == state {
			out = append(out, s.ID)
		}
	}
	return out
}

func (r *Run) runningStep() *StepRun {
	for i := range r.Steps {
		if r.Steps[i].State == StepRunning || r.Steps[i].State == StepHealing {
			return &r.Steps[i]
		}
	}
	return nil
}

func (r *Run) allPassed() bool {
	for _, s := range r.Steps {
		if s.State != StepPassed && s.State != StepHealed {
			return false
		}
	}
	return len(r.Steps) > 0
}

func idList(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

// stepStart moves a pending step to running. Steps must start in case order.
func (r *Run) stepStart(id string, now time.Time) error {
	if r.finished() {
		return stateErrf("run %s is already %s; no more steps can be started", r.ID, r.Status)
	}
	st := r.step(id)
	if st == nil {
		return stateErrf("unknown step %q; valid step ids: %s", id, idList(r.stepIDs("")))
	}
	if st.State == StepHealing {
		return nil // the repair agent announcing the step it was asked to fix
	}
	if cur := r.runningStep(); cur != nil {
		return stateErrf("step %q is still running; call workflow_step_done for it before starting %q", cur.ID, id)
	}
	if st.State != StepPending {
		return stateErrf("step %q is already %s; pending step ids: %s", id, st.State, idList(r.stepIDs(StepPending)))
	}
	if next := r.stepIDs(StepPending)[0]; next != id {
		return stateErrf("step %q started out of order; the next pending step is %q (pending step ids in order: %s)",
			id, next, idList(r.stepIDs(StepPending)))
	}
	st.State = StepRunning
	st.StartedAt = &now
	return nil
}

// active returns the running step id must refer to.
func (r *Run) active(id, what string) (*StepRun, error) {
	if r.finished() {
		return nil, stateErrf("run %s is already %s; cannot record %s", r.ID, r.Status, what)
	}
	st := r.step(id)
	if st == nil {
		return nil, stateErrf("unknown step %q; valid step ids: %s", id, idList(r.stepIDs("")))
	}
	if st.State == StepRunning || st.State == StepHealing {
		return st, nil
	}
	if cur := r.runningStep(); cur != nil {
		return nil, stateErrf("step %q is %s, not running; the running step is %q", id, st.State, cur.ID)
	}
	return nil, stateErrf("step %q is %s, not running; call workflow_step_start first (pending step ids: %s)",
		id, st.State, idList(r.stepIDs(StepPending)))
}

func (r *Run) substep(id, text string, now time.Time) (Substep, error) {
	st, err := r.active(id, "a substep")
	if err != nil {
		return Substep{}, err
	}
	if strings.TrimSpace(text) == "" {
		return Substep{}, fmt.Errorf("%w: substep text is required", ErrInvalid)
	}
	ss := Substep{Text: text, At: now}
	st.Substeps = append(st.Substeps, ss)
	return ss, nil
}

func (r *Run) assert(id string, a AssertResult) error {
	st, err := r.active(id, "an assertion")
	if err != nil {
		return err
	}
	if strings.TrimSpace(a.Text) == "" {
		return fmt.Errorf("%w: assertion text (expectation) is required", ErrInvalid)
	}
	st.Asserts = append(st.Asserts, a)
	return nil
}

// stepDone finishes the running step. A failed step fails the run and skips
// the remaining steps. It returns the finished step; runDone is set when the
// run reached a terminal status because of this call.
func (r *Run) stepDone(id string, passed bool, notes, errText string, now time.Time) (st *StepRun, runDone bool, err error) {
	if r.finished() {
		return nil, false, stateErrf("run %s is already %s; step %q cannot be finished", r.ID, r.Status, id)
	}
	st = r.step(id)
	if st == nil {
		return nil, false, stateErrf("unknown step %q; valid step ids: %s", id, idList(r.stepIDs("")))
	}
	switch st.State {
	case StepRunning, StepHealing:
	case StepPassed, StepFailed, StepHealed, StepSkipped:
		return nil, false, stateErrf("step %q is already done (%s)", id, st.State)
	default:
		return nil, false, stateErrf("step %q is not running; call workflow_step_start first", id)
	}
	healing := st.State == StepHealing
	st.EndedAt = &now
	if st.StartedAt != nil {
		st.DurationMs = now.Sub(*st.StartedAt).Milliseconds()
	}
	st.Result = notes
	if passed {
		st.State = StepPassed
		if healing {
			st.Error = ""
			st.State = StepHealed
			r.Healed = append(r.Healed, st.ID)
		}
		return st, false, nil
	}
	st.State = StepFailed
	st.Error = errText
	if st.Error == "" {
		st.Error = notes
	}
	for i := range r.Steps {
		if r.Steps[i].State == StepPending {
			r.Steps[i].State = StepSkipped
		}
	}
	msg := fmt.Sprintf("step %s failed", id)
	if st.Error != "" {
		msg += ": " + st.Error
	}
	r.end(RunFailed, msg, now)
	return st, true, nil
}

func (r *Run) end(status, errText string, now time.Time) {
	r.Status = status
	r.Error = errText
	r.EndedAt = &now
}

// closeOpen terminates a run that is still open (agent exit or cancel).
// reason is used for the run error and any step still running.
func (r *Run) closeOpen(status, reason string, now time.Time) {
	for i := range r.Steps {
		s := &r.Steps[i]
		switch s.State {
		case StepRunning, StepHealing:
			s.State = StepFailed
			s.Error = reason
			s.EndedAt = &now
			if s.StartedAt != nil {
				s.DurationMs = now.Sub(*s.StartedAt).Milliseconds()
			}
		case StepPending:
			s.State = StepSkipped
		}
	}
	r.end(status, reason, now)
}

// clone returns a copy that shares no slices or pointers with st, safe to hand
// to the event stream while the state machine keeps mutating the original.
func (st *StepRun) clone() StepRun {
	c := *st
	c.Substeps = append([]Substep(nil), st.Substeps...)
	c.Asserts = append([]AssertResult(nil), st.Asserts...)
	if st.StartedAt != nil {
		t := *st.StartedAt
		c.StartedAt = &t
	}
	if st.EndedAt != nil {
		t := *st.EndedAt
		c.EndedAt = &t
	}
	return c
}
