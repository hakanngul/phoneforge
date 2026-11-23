package mobflow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"pocketlab/internal/device"
	"pocketlab/internal/dsl"
)

// replayer runs a case from its cached segments without an agent. It reports
// through the Service state machine exactly like an agent would, so replay and
// record runs look the same in timelines, SSE and run.json.
type replayer struct {
	s     *Service
	l     *live
	c     *Case
	segs  map[string]Segment
	stale map[string]string
	agent string
	heal  bool
	dev   device.Device
}

// staleMessage names the segments that cannot be replayed.
func staleMessage(c *Case, stale map[string]string) string {
	var st, miss []string
	for _, step := range c.Steps {
		switch stale[step.ID] {
		case StateStale:
			st = append(st, step.ID)
		case StateMissing:
			miss = append(miss, step.ID)
		}
	}
	var parts []string
	if len(st) > 0 {
		parts = append(parts, "stale segments: "+strings.Join(st, ","))
	}
	if len(miss) > 0 {
		parts = append(parts, "missing segments: "+strings.Join(miss, ","))
	}
	return strings.Join(parts, "; ") + "; record or heal needed"
}

// startReplay launches the replay goroutine of a replay run.
func (s *Service) startReplay(l *live, c *Case, cache *Cache, stale map[string]string, agent string) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := s.rt
	rt.mu.Lock()
	l.cancel = cancel
	rt.mu.Unlock()
	rp := &replayer{s: s, l: l, c: c, segs: map[string]Segment{}, stale: stale, agent: agent, heal: l.run.Heal}
	if cache != nil {
		for _, sg := range cache.Segments {
			rp.segs[sg.ID] = sg
		}
	}
	runID := l.run.ID
	go func() {
		defer cancel()
		defer func() {
			if p := recover(); p != nil {
				_, _ = s.Finish(runID, fmt.Sprintf("replay crashed: %v", p))
			}
		}()
		rp.run(ctx)
	}()
}

func (rp *replayer) run(ctx context.Context) {
	s, runID := rp.s, rp.l.run.ID
	if s.Devices == nil {
		_, _ = s.Finish(runID, "replay needs a device resolver; none is configured")
		return
	}
	dev, err := s.Devices(rp.l.run.DeviceID)
	if err != nil {
		_, _ = s.Finish(runID, "device unavailable: "+err.Error())
		return
	}
	rp.dev = dev
	if !rp.heal {
		for _, st := range rp.c.Steps {
			if rp.stale[st.ID] != StateFresh {
				_, _ = s.Finish(runID, staleMessage(rp.c, rp.stale))
				return
			}
		}
	}
	for _, st := range rp.c.Steps {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.StepStart(runID, st.ID); err != nil {
			return
		}
		var fail *segFailure
		seg, have := rp.segs[st.ID]
		if state := rp.stale[st.ID]; state != StateFresh {
			what := "has no recorded segment"
			if state == StateStale {
				what = "recorded segment is stale (the step changed since it was recorded)"
			}
			fail = &segFailure{msg: "step " + what}
		} else {
			fail = rp.runSegment(ctx, st, seg)
		}
		if fail == nil {
			if _, err := s.StepDone(runID, st.ID, true, "", ""); err != nil {
				return
			}
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if !rp.heal {
			if len(fail.png) > 0 {
				s.setStepShot(rp.l, st.ID, fail.png)
			}
			_, _ = s.StepDone(runID, st.ID, false, "", fail.msg)
			return
		}
		if !rp.healStep(ctx, st, seg, have, fail) {
			return
		}
	}
	_, _ = s.Finish(runID, "")
}

// segFailure describes why a segment did not pass.
type segFailure struct {
	msg string
	png []byte // failure screenshot taken by the DSL engine, if any
}

func (rp *replayer) params() map[string]string {
	out := map[string]string{}
	for k, v := range rp.l.run.Params {
		out[k] = v
	}
	return out
}

// runSegment executes one cached segment deterministically and reports its
// substeps and assertions on the (running) step. nil means the segment passed.
func (rp *replayer) runSegment(ctx context.Context, st Step, seg Segment) *segFailure {
	s, runID := rp.s, rp.l.run.ID
	script, err := parseSegment(seg)
	if err != nil {
		return &segFailure{msg: "recorded segment is not valid DSL: " + err.Error()}
	}
	settle := true
	res, err := dsl.ExecuteScript(ctx, rp.dev, script, dsl.Options{
		Params: rp.params(), Secrets: s.Secrets, Mode: "deterministic", Settle: &settle,
	})
	if err != nil {
		return &segFailure{msg: "cannot run the recorded segment: " + err.Error()}
	}
	for _, sr := range res.Steps {
		var raw json.RawMessage
		if sr.Index >= 0 && sr.Index < len(seg.Steps) {
			raw = seg.Steps[sr.Index]
		}
		label := stepLabel(raw, sr.Action)
		text := label + " - " + sr.Status
		if sr.Status != dsl.StatusOK && sr.Error != "" {
			text += ": " + sr.Error
		}
		if _, err := s.Substep(runID, st.ID, text); err != nil {
			return &segFailure{msg: "run closed: " + err.Error()}
		}
		if canon, _ := dsl.CanonicalAction(sr.Action); assertActions[canon] && sr.Status != dsl.StatusSkipped {
			detail := ""
			if sr.Status != dsl.StatusOK {
				detail = sr.Error
			}
			_, _ = s.Assert(runID, st.ID, AssertResult{Text: label, Passed: sr.Status == dsl.StatusOK, Detail: detail})
		}
	}
	if res.Passed {
		return nil
	}
	f := &segFailure{msg: "recorded segment failed"}
	for _, sr := range res.Steps {
		if sr.Status == dsl.StatusFailed || sr.Status == dsl.StatusNeedsReplan || sr.Status == dsl.StatusNeedsUser {
			f.msg = fmt.Sprintf("DSL step %d (%s) %s", sr.Index+1, sr.Action, sr.Status)
			if sr.Error != "" {
				f.msg += ": " + sr.Error
			}
			if sr.Status != dsl.StatusFailed {
				f.msg += " (a replayed segment cannot ask for a new plan or a human)"
			}
			f.png = findShot(sr)
			break
		}
	}
	return f
}

func findShot(sr dsl.StepResult) []byte {
	if sr.Screenshot != "" {
		if b, err := base64.StdEncoding.DecodeString(sr.Screenshot); err == nil {
			return b
		}
	}
	for _, list := range [][]dsl.StepResult{sr.ThenResults, sr.ElseResults, sr.BodyResults} {
		for _, n := range list {
			if b := findShot(n); len(b) > 0 {
				return b
			}
		}
	}
	return nil
}

// stepLabel is a short human text of a DSL step: the action and its target.
// Typed text is never included.
func stepLabel(raw json.RawMessage, action string) string {
	var o struct {
		Selector map[string]any `json:"selector"`
		Text     string         `json:"text"`
		BundleID string         `json:"bundleId"`
		Key      string         `json:"key"`
	}
	if raw != nil {
		_ = json.Unmarshal(raw, &o)
	}
	canon, _ := dsl.CanonicalAction(action)
	target := ""
	keys := []string{"text", "textContains", "textStartsWith", "label", "labelContains", "accessibilityId", "resourceId", "textRegex", "value"}
	for _, k := range keys {
		if v, ok := o.Selector[k].(string); ok && v != "" {
			target = v
			break
		}
	}
	switch {
	case target != "":
	case canon == "ocr_tap" || canon == "assert_text_ocr":
		target = o.Text
	case canon == "launch_app":
		target = o.BundleID
	case canon == "press_key":
		target = o.Key
	}
	if target == "" {
		return action
	}
	return fmt.Sprintf("%s %q", action, target)
}

// failShot stores the failure screenshot as fail-<step>.png and returns its
// file path ("" when there is none).
func (s *Service) failShot(l *live, stepID string, png []byte) string {
	if len(png) == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		png, _ = s.shoot(ctx, l.run.DeviceID)
		cancel()
	}
	if len(png) == 0 {
		return ""
	}
	name := "fail-" + stepID + ".png"
	rt := s.rt
	if err := rt.store.WriteArtifact(l.run.ID, name, png); err != nil {
		return ""
	}
	p, err := rt.store.ArtifactPath(l.run.ID, name)
	if err != nil {
		return ""
	}
	return p
}

// setStepShot stores png as the step's screenshot.
func (s *Service) setStepShot(l *live, stepID string, png []byte) {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	_ = s.saveShotLocked(l, stepID, png)
}
