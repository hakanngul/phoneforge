package dsl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// normKey makes key and action names forgiving: case, underscores, hyphens
// and spaces are ignored ("on_fail", "onFail" and "ON-FAIL" are the same key).
func normKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '_', '-', ' ':
			continue
		}
		b.WriteString(strings.ToLower(string(r)))
	}
	return b.String()
}

// canonTable maps normalized key -> canonical JSON tag for the top-level
// fields of t, plus the given aliases (alias -> canonical tag).
func canonTable(t reflect.Type, aliases map[string]string) map[string]string {
	m := map[string]string{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		m[normKey(tag)] = tag
	}
	for a, c := range aliases {
		m[normKey(a)] = c
	}
	return m
}

func validKeys(table map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range table {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// decodeStrict decodes a JSON object into dst after rewriting every key to
// its canonical spelling. Unknown keys are rejected.
func decodeStrict(data []byte, table map[string]string, dst any) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]json.RawMessage, len(raw))
	for _, k := range keys {
		c, ok := table[normKey(k)]
		if !ok {
			return fmt.Errorf("unknown field %q (valid: %s)", k, strings.Join(validKeys(table), ", "))
		}
		if _, dup := out[c]; dup {
			return fmt.Errorf("field %q given more than once", c)
		}
		out[c] = raw[k]
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

var (
	stepKeys = canonTable(reflect.TypeOf(Step{}), map[string]string{
		"predicate": "selector", "retry_delay": "retryDelayMs", "timeout": "timeoutMs",
		"duration": "durationMs", "app": "bundleId",
	})
	selectorKeys = canonTable(reflect.TypeOf(Selector{}), map[string]string{
		"regex": "textRegex", "accessibility_id": "accessibilityId", "id": "accessibilityId",
	})
	scriptKeys   = canonTable(reflect.TypeOf(Script{}), nil)
	defaultsKeys = canonTable(reflect.TypeOf(Defaults{}), map[string]string{"timeout": "timeoutMs"})
	policyKeys   = canonTable(reflect.TypeOf(FailPolicy{}), map[string]string{
		"type": "strategy", "action": "strategy", "mode": "strategy", "fallback": "fallbackStrategy",
		"retries": "maxRetries", "retry_delay": "retryDelayMs",
	})
	coordsKeys = canonTable(reflect.TypeOf(Coords{}), nil)
	refKeys    = canonTable(reflect.TypeOf(ElementRef{}), map[string]string{"selector": "predicate"})
)

// UnmarshalJSON accepts every documented spelling of the step keys
// (snake_case, camelCase, ...) and rejects unknown keys.
func (s *Step) UnmarshalJSON(data []byte) error {
	type plain Step
	var p plain
	if err := decodeStrict(data, stepKeys, &p); err != nil {
		return err
	}
	*s = Step(p)
	return nil
}

// UnmarshalJSON accepts documented top-level keys in any spelling.
func (s *Script) UnmarshalJSON(data []byte) error {
	type plain Script
	var p plain
	if err := decodeStrict(data, scriptKeys, &p); err != nil {
		return err
	}
	*s = Script(p)
	return nil
}

// UnmarshalJSON accepts timeout_ms / timeoutMs etc.
func (d *Defaults) UnmarshalJSON(data []byte) error {
	type plain Defaults
	var p plain
	if err := decodeStrict(data, defaultsKeys, &p); err != nil {
		return err
	}
	*d = Defaults(p)
	return nil
}

// UnmarshalJSON accepts every documented predicate key; an unknown key is a
// validation error.
func (s *Selector) UnmarshalJSON(data []byte) error {
	// A bare string is a CSS selector (used by the web_* actions).
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '"' {
		var css string
		if err := json.Unmarshal(t, &css); err != nil {
			return err
		}
		*s = Selector{CSSSelector: css}
		return nil
	}
	type plain Selector
	var p plain
	if err := decodeStrict(data, selectorKeys, &p); err != nil {
		return err
	}
	*s = Selector(p)
	return nil
}

// FlexString decodes a JSON string, number or boolean into its text form.
type FlexString string

// UnmarshalJSON implements json.Unmarshaler.
func (f *FlexString) UnmarshalJSON(b []byte) error {
	t := strings.TrimSpace(string(b))
	if t == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = FlexString(s)
		return nil
	}
	if t == "true" || t == "false" {
		*f = FlexString(t)
		return nil
	}
	if _, err := strconv.ParseFloat(t, 64); err == nil {
		*f = FlexString(t)
		return nil
	}
	return fmt.Errorf("expected string, number or boolean, got %s", t)
}

// MarshalJSON emits the text form as a JSON string.
func (f FlexString) MarshalJSON() ([]byte, error) { return json.Marshal(string(f)) }

// Coords is a screen position: absolute (x, y), percentages (x_percent,
// y_percent) or a mix; a bare [x, y] array is also accepted.
type Coords struct {
	X        *float64 `json:"x,omitempty"`
	Y        *float64 `json:"y,omitempty"`
	XPercent *float64 `json:"xPercent,omitempty"`
	YPercent *float64 `json:"yPercent,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler.
func (c *Coords) UnmarshalJSON(data []byte) error {
	t := bytes.TrimSpace(data)
	if len(t) > 0 && t[0] == '[' {
		var a []float64
		if err := json.Unmarshal(t, &a); err != nil || len(a) != 2 {
			return fmt.Errorf("coordinates array must be [x, y]")
		}
		*c = Coords{X: &a[0], Y: &a[1]}
		return nil
	}
	type plain Coords
	var p plain
	if err := decodeStrict(data, coordsKeys, &p); err != nil {
		return err
	}
	*c = Coords(p)
	return nil
}

// ElementRef points at an element via a predicate (swipe from, drag from /
// to_element, scroll to_element). A bare predicate object is also accepted.
type ElementRef struct {
	Predicate    *Selector `json:"predicate,omitempty"`
	FullyVisible bool      `json:"fullyVisible,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler.
func (e *ElementRef) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k := range raw {
		if n := normKey(k); n == "predicate" || n == "selector" || n == "fullyvisible" {
			type plain ElementRef
			var p plain
			if err := decodeStrict(data, refKeys, &p); err != nil {
				return err
			}
			*e = ElementRef(p)
			return nil
		}
	}
	var sel Selector
	if err := json.Unmarshal(data, &sel); err != nil {
		return err
	}
	*e = ElementRef{Predicate: &sel}
	return nil
}

// UnmarshalJSON accepts a bare strategy name ("skip") or an object.
func (p *FailPolicy) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		*p = FailPolicy{Strategy: s}
		return nil
	}
	type plain FailPolicy
	var q plain
	if err := decodeStrict(data, policyKeys, &q); err != nil {
		return err
	}
	*p = FailPolicy(q)
	return nil
}

// UnmarshalJSON accepts a scalar default or an object {"default": ...}.
func (p *ParamSpec) UnmarshalJSON(data []byte) error {
	t := bytes.TrimSpace(data)
	if string(t) == "null" {
		*p = ParamSpec{}
		return nil
	}
	if len(t) > 0 && t[0] == '{' {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(t, &m); err != nil {
			return err
		}
		d, ok := m["default"]
		if !ok || string(bytes.TrimSpace(d)) == "null" {
			*p = ParamSpec{}
			return nil
		}
		t = d
	}
	var f FlexString
	if err := json.Unmarshal(t, &f); err != nil {
		return fmt.Errorf("param value: %w", err)
	}
	*p = ParamSpec{Default: string(f), Has: true}
	return nil
}

// MarshalJSON emits the default as a string (null when the param is required).
func (p ParamSpec) MarshalJSON() ([]byte, error) {
	if !p.Has {
		return []byte("null"), nil
	}
	return json.Marshal(p.Default)
}

var pointKeys = canonTable(reflect.TypeOf(PathPoint{}), nil)

// UnmarshalJSON accepts duration_ms / durationMs.
func (p *PathPoint) UnmarshalJSON(data []byte) error {
	type plain PathPoint
	var q plain
	if err := decodeStrict(data, pointKeys, &q); err != nil {
		return err
	}
	*p = PathPoint(q)
	return nil
}
