package mobflow

import (
	"encoding/json"
	"fmt"

	"pocketlab/internal/dsl"
)

// stabilizeStep is the DSL step injected after screen-changing actions.
var stabilizeStep = json.RawMessage(`{"action":"wait_stable"}`)

// waitsActions are canonical actions that already wait for the UI, so a
// stabilisation step right before them would be redundant.
var waitsActions = map[string]bool{
	"wait_for": true, "wait_stable": true, "wait": true,
	"assert_exists": true, "assert_not_exists": true, "assert_text_ocr": true, "web_wait": true,
}

func rawAction(raw json.RawMessage) string {
	var o struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return ""
	}
	canon, _ := dsl.CanonicalAction(o.Action)
	return canon
}

// NormalizeSegment injects `wait_stable` after every top-level screen-changing
// action (see dsl.ChangesUI) unless the next step already waits. It is
// idempotent: a normalised segment is returned unchanged. The input is not
// modified.
func NormalizeSegment(seg Segment) Segment {
	out := make([]json.RawMessage, 0, len(seg.Steps)+2)
	for i, raw := range seg.Steps {
		out = append(out, raw)
		if !dsl.ChangesUI(rawAction(raw)) {
			continue
		}
		if i+1 < len(seg.Steps) && waitsActions[rawAction(seg.Steps[i+1])] {
			continue
		}
		out = append(out, stabilizeStep)
	}
	seg.Steps = out
	return seg
}

func normalizeSegments(in []Segment) []Segment {
	out := make([]Segment, len(in))
	for i, sg := range in {
		out[i] = NormalizeSegment(sg)
	}
	return out
}

// Warning is a non-fatal finding of Lint.
type Warning struct {
	Step    string `json:"step"`
	Path    string `json:"path,omitempty"` // position inside the segment, e.g. "2" or "3.then.0"
	Message string `json:"message"`
}

func (w Warning) String() string {
	if w.Path != "" {
		return fmt.Sprintf("step %s [%s]: %s", w.Step, w.Path, w.Message)
	}
	return fmt.Sprintf("step %s: %s", w.Step, w.Message)
}

// maxWaitMs is the longest fixed sleep Lint accepts.
const maxWaitMs = 2000

var coordActions = map[string]bool{"tap": true, "long_press": true, "double_tap": true, "two_finger_tap": true}

// Lint returns style warnings about the recorded DSL of a case: taps on raw
// coordinates and fixed sleeps over 2 s. A nil cache has nothing to lint.
func Lint(c *Case, cache *Cache) []Warning {
	out := []Warning{}
	if c == nil || cache == nil {
		return out
	}
	for _, sg := range cache.Segments {
		if _, ok := c.Step(sg.ID); !ok {
			continue
		}
		script, err := parseSegment(sg)
		if err != nil {
			continue
		}
		walkDSL(script.Steps, func(path string, st dsl.Step) {
			canon, _ := dsl.CanonicalAction(st.Action)
			if coordActions[canon] && st.X != nil && st.Y != nil && selectorEmpty(st.Selector) {
				out = append(out, Warning{sg.ID, path, fmt.Sprintf("%s on raw coordinates (%d,%d); prefer a selector so it survives layout changes", canon, *st.X, *st.Y)})
			}
			if canon == "wait" {
				ms := st.Ms
				if ms <= 0 {
					ms = st.DurationMs
				}
				if ms > maxWaitMs {
					out = append(out, Warning{sg.ID, path, fmt.Sprintf("fixed wait of %d ms (over %d ms); use wait_for or wait_stable instead", ms, maxWaitMs)})
				}
			}
		})
	}
	return out
}

func selectorEmpty(s *dsl.Selector) bool {
	if s == nil {
		return true
	}
	b, err := json.Marshal(s)
	return err == nil && string(b) == "{}"
}
