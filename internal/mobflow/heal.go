package mobflow

import (
	"context"
	"fmt"
	"time"
)

// defaultHealTimeout bounds one self-heal agent run.
const defaultHealTimeout = 10 * time.Minute

// healStep hands the failing segment of st to an agent. The step moves to
// healing, the agent finishes it through the workflow_* tools on the same run
// and submits only that segment. It returns true when the step was healed and
// the replay may continue; otherwise the run has been failed or cancelled.
func (rp *replayer) healStep(ctx context.Context, st Step, seg Segment, haveSeg bool, fail *segFailure) bool {
	s, l := rp.s, rp.l
	runID := l.run.ID
	rt := s.rt

	_, _ = s.Substep(runID, st.ID, "Replay failed: "+fail.msg+" - asking the agent to repair this step")
	shot := s.failShot(l, st.ID, fail.png)

	var recent []string
	if r, err := s.GetRun(runID); err == nil {
		if sr := r.step(st.ID); sr != nil {
			for _, ss := range sr.Substeps {
				recent = append(recent, ss.Text)
			}
		}
	}
	if len(recent) > 8 {
		recent = recent[len(recent)-8:]
	}

	rt.mu.Lock()
	if l.run.finished() {
		rt.mu.Unlock()
		return false
	}
	if sr := l.run.step(st.ID); sr != nil {
		sr.State = StepHealing
		l.br.publish(EvStep, sr.clone())
	}
	l.healStep = st.ID
	_ = rt.store.Save(l.run)
	rt.mu.Unlock()

	in := HealInput{
		PromptInput: PromptInput{CaseID: l.run.CaseID, RunID: runID, DeviceID: l.run.DeviceID, Platform: l.run.Platform, Case: rp.c, Params: l.run.Params},
		Step:        st, Error: fail.msg, ScreenshotPath: shot, RecentSubsteps: recent,
	}
	if haveSeg {
		in.SegmentDSL = seg.Steps
	}
	system, task := HealPrompt(in)
	done := make(chan error, 1)
	agentID, err := s.Launcher.Start(AgentRequest{
		Agent: rp.agent, DeviceID: l.run.DeviceID, SystemPrompt: system, Prompt: task,
		OnLine: func(stream, line string) { s.LogLine(runID, stream, line) },
		OnDone: func(err error) {
			select {
			case done <- err:
			default:
			}
		},
	})
	if err != nil {
		s.failHeal(l, st.ID, fail.msg, "the agent could not be started: "+err.Error())
		return false
	}
	rt.mu.Lock()
	l.run.AgentRunID = agentID
	_ = rt.store.Save(l.run)
	rt.mu.Unlock()

	timeout := s.HealTimeout
	if timeout <= 0 {
		timeout = defaultHealTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var agentErr error
	select {
	case agentErr = <-done:
	case <-ctx.Done():
		s.Launcher.Cancel(agentID)
		return false
	case <-timer.C:
		s.Launcher.Cancel(agentID)
		s.failHeal(l, st.ID, fail.msg, fmt.Sprintf("the agent did not finish within %s", timeout))
		return false
	}
	if ctx.Err() != nil {
		return false
	}

	rt.mu.Lock()
	finished := l.run.finished()
	state := ""
	if sr := l.run.step(st.ID); sr != nil {
		state = sr.State
	}
	cached := l.healCached[st.ID]
	rt.mu.Unlock()
	switch {
	case finished:
		return false // the agent reported the step failed; the run is closed
	case state == StepHealed && cached:
		rt.mu.Lock()
		l.healStep = ""
		rt.mu.Unlock()
		return true
	case state == StepHealed:
		s.failHeal(l, st.ID, fail.msg, "the agent repaired the step but did not submit the repaired segment with workflow_cache_script")
	default:
		reason := "the agent exited before finishing the step"
		if agentErr != nil {
			reason = "the agent exited with an error: " + agentErr.Error()
		}
		s.failHeal(l, st.ID, fail.msg, reason)
	}
	return false
}

// failHeal fails the run because the repair of stepID did not succeed. The
// cache was not touched (a repaired segment is only stored once accepted).
func (s *Service) failHeal(l *live, stepID, replayErr, reason string) {
	rt := s.rt
	runID := l.run.ID
	msg := fmt.Sprintf("step %s failed: %s; self-heal failed: %s", stepID, replayErr, reason)
	png := s.shootFor(runID, stepID)
	rt.mu.Lock()
	if l.run.finished() {
		rt.mu.Unlock()
		return
	}
	if sr := l.run.step(stepID); sr != nil && sr.State == StepHealed {
		sr.State = StepHealing
		var keep []string
		for _, id := range l.run.Healed {
			if id != stepID {
				keep = append(keep, id)
			}
		}
		l.run.Healed = keep
	}
	if l.run.Healed == nil {
		l.run.Healed = []string{}
	}
	l.healStep = ""
	l.run.closeOpen(RunFailed, msg, time.Now().UTC())
	if len(png) > 0 {
		_ = s.saveShotLocked(l, stepID, png)
	}
	rt.finishLocked(l)
	rt.mu.Unlock()
}
