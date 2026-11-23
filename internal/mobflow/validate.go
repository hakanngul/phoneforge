package mobflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"pocketlab/internal/dsl"
)

// assertActions are the canonical DSL actions that assert something.
var assertActions = map[string]bool{
	"assert_exists": true, "assert_not_exists": true, "assert_count": true,
	"assert_property": true, "assert_screen_changed": true,
	"assert_text_ocr": true, "web_assert_text": true,
}

// minSecretCheckLen is the shortest secret value that is searched for in a
// segment: shorter values would match ordinary text everywhere.
const minSecretCheckLen = 4

// parseSegment decodes a segment's raw steps as a deterministic DSL script.
func parseSegment(seg Segment) (dsl.Script, error) {
	b, err := json.Marshal(map[string]any{"mode": "deterministic", "steps": seg.Steps})
	if err != nil {
		return dsl.Script{}, err
	}
	return dsl.Parse(b)
}

func walkDSL(steps []dsl.Step, fn func(path string, st dsl.Step)) {
	var rec func(steps []dsl.Step, prefix string)
	rec = func(steps []dsl.Step, prefix string) {
		for i, st := range steps {
			p := fmt.Sprintf("%s%d", prefix, i)
			fn(p, st)
			rec(st.Then, p+".then.")
			rec(st.Else, p+".else.")
			rec(st.Body, p+".body.")
		}
	}
	rec(steps, "")
}

// ValidateSegment is the Store.Validator of the flows cache: the segment must
// be a valid deterministic DSL script that can replay without an agent or a
// human, and an assert step's segment must contain an assertion.
func ValidateSegment(seg Segment, step Step) error {
	script, err := parseSegment(seg)
	if err != nil {
		return fmt.Errorf("invalid DSL: %w", err)
	}
	var problems []string
	hasAssert := false
	walkDSL(script.Steps, func(path string, st dsl.Step) {
		canon, _ := dsl.CanonicalAction(st.Action)
		if assertActions[canon] {
			hasAssert = true
		}
		if canon == "request_user_input" {
			problems = append(problems, fmt.Sprintf("step %s: request_user_input needs a human and cannot be replayed", path))
		}
		if st.OnFail != nil {
			for _, v := range []string{st.OnFail.Strategy, st.OnFail.FallbackStrategy} {
				if v == dsl.OnFailReplan || v == dsl.OnFailRequireUser {
					problems = append(problems, fmt.Sprintf("step %s: on_fail %q is not allowed in a cached script (use abort, skip or retry)", path, v))
				}
			}
		}
	})
	if step.Type == "assert" && !hasAssert {
		problems = append(problems, fmt.Sprintf("step %q is an assert step; its segment must contain at least one assertion action (%s)", step.ID, assertNames()))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

func assertNames() string {
	var n []string
	for k := range assertActions {
		n = append(n, k)
	}
	sort.Strings(n)
	return strings.Join(n, ", ")
}

// checkAgainstCase runs the checks that need the case: type_secret may only
// use secrets the case declares, and no segment may contain the plaintext of a
// declared secret (resolved with secrets; nil or failing resolution skips that
// part, reported in note).
func checkAgainstCase(c *Case, segs []Segment, secrets func(id string) (string, error)) (errs ValidationErrors, note string) {
	declared := map[string]bool{}
	for _, id := range c.Secrets {
		declared[id] = true
	}
	var values []string
	skipped := 0
	for _, id := range c.Secrets {
		if secrets == nil {
			skipped++
			continue
		}
		v, err := secrets(id)
		if err != nil || len(v) < minSecretCheckLen {
			skipped++
			continue
		}
		values = append(values, v)
	}
	if skipped > 0 {
		note = fmt.Sprintf("literal-secret check skipped for %d of %d secrets (value unavailable or shorter than %d characters)", skipped, len(c.Secrets), minSecretCheckLen)
	}
	add := func(id, msg string) {
		errs = append(errs, ValidationError{Step: id, Field: "segments", Message: msg})
	}
	for _, sg := range segs {
		if script, err := parseSegment(sg); err == nil {
			walkDSL(script.Steps, func(path string, st dsl.Step) {
				if canon, _ := dsl.CanonicalAction(st.Action); canon == "type_secret" && !declared[st.SecretID] {
					add(sg.ID, fmt.Sprintf("step %s: type_secret uses secret %q which the case does not declare (declared: %s)", path, st.SecretID, idList(c.Secrets)))
				}
			})
		}
		if len(values) == 0 {
			continue
		}
		if containsSecret(sg.Steps, values) {
			add(sg.ID, "segment contains a literal secret value; use type_secret with the secret id instead")
		}
	}
	return errs, note
}

func containsSecret(steps []json.RawMessage, values []string) bool {
	var found bool
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			for _, s := range values {
				if strings.Contains(t, s) {
					found = true
				}
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for k, e := range t {
				walk(k)
				walk(e)
			}
		}
	}
	for _, raw := range steps {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			walk(v)
		}
	}
	return found
}
