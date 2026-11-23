// Package mobflow implements Pocketlab "flows": natural-language test cases
// (.mobflow, YAML) whose agent-recorded deterministic DSL is kept in a cache
// sidecar keyed to a hash of each step. See docs/plans/workflow-flows.md.
package mobflow

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Step types.
const (
	StepLogin  = "login"
	StepAct    = "act"
	StepAssert = "assert"
	StepNav    = "nav"
)

const maxCaseBytes = 1 << 20

var (
	stepIDRe  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	identRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	paramRef  = regexp.MustCompile(`\$\{([^}]*)\}`)
	platforms = map[string]bool{"": true, "any": true, "android": true, "ios": true}
)

// Case is a human-authored flow. It never contains generated DSL.
type Case struct {
	Version int              `yaml:"version" json:"version"`
	Name    string           `yaml:"name" json:"name"`
	App     App              `yaml:"app,omitempty" json:"app"`
	Device  Device           `yaml:"device,omitempty" json:"device"`
	Params  map[string]Param `yaml:"params,omitempty" json:"params,omitempty"`
	Secrets []string         `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Steps   []Step           `yaml:"steps" json:"steps"`
}

// App identifies the app under test.
type App struct {
	BundleID string `yaml:"bundleId,omitempty" json:"bundleId,omitempty"`
	Platform string `yaml:"platform,omitempty" json:"platform,omitempty"` // any|android|ios
}

// Device constrains the target device.
type Device struct {
	Platform string `yaml:"platform,omitempty" json:"platform,omitempty"` // any|android|ios
	MinOS    string `yaml:"minOS,omitempty" json:"minOS,omitempty"`
}

// Param is a declared ${name} parameter with a default value.
type Param struct {
	Default string `yaml:"default,omitempty" json:"default,omitempty"`
}

// Step is one natural-language step of a case.
type Step struct {
	ID     string `yaml:"id" json:"id"`
	Type   string `yaml:"type" json:"type"` // login|act|assert|nav
	Title  string `yaml:"title,omitempty" json:"title,omitempty"`
	Do     string `yaml:"do" json:"do"`
	Expect string `yaml:"expect,omitempty" json:"expect,omitempty"`
}

// ValidationError is one problem in a case. Step is empty for case-level
// problems; Field names the offending key ("steps", "do", ...).
type ValidationError struct {
	Step    string `json:"step,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (e ValidationError) Error() string {
	switch {
	case e.Step != "" && e.Field != "":
		return fmt.Sprintf("step %s: %s: %s", e.Step, e.Field, e.Message)
	case e.Step != "":
		return fmt.Sprintf("step %s: %s", e.Step, e.Message)
	case e.Field != "":
		return fmt.Sprintf("%s: %s", e.Field, e.Message)
	}
	return e.Message
}

// ValidationErrors is the full list of problems found; it is an error when
// non-empty.
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	parts := make([]string, len(e))
	for i, v := range e {
		parts[i] = v.Error()
	}
	return strings.Join(parts, "; ")
}

// Decode parses YAML (or JSON, which is a YAML subset) without validating.
// Unknown keys are rejected so typos surface.
func Decode(data []byte) (*Case, error) {
	if len(data) > maxCaseBytes {
		return nil, fmt.Errorf("case exceeds %d bytes", maxCaseBytes)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Case
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty document")
		}
		return nil, fmt.Errorf("yaml: %s", strings.TrimPrefix(err.Error(), "yaml: "))
	}
	return &c, nil
}

// Parse decodes and validates a case. When the document cannot be decoded the
// case is nil and the list holds a single error; otherwise the (possibly
// invalid) case is returned together with every validation error.
func Parse(data []byte) (*Case, ValidationErrors) {
	c, err := Decode(data)
	if err != nil {
		return nil, ValidationErrors{{Message: err.Error()}}
	}
	return c, c.Validate()
}

// Marshal renders a case as YAML.
func Marshal(c *Case) ([]byte, error) { return yaml.Marshal(c) }

// ParamRefs returns the sorted, de-duplicated ${name} references in s.
func ParamRefs(s string) []string {
	seen := map[string]bool{}
	for _, m := range paramRef.FindAllStringSubmatch(s, -1) {
		seen[strings.TrimSpace(m[1])] = true
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ParamNames returns the sorted declared parameter names.
func (c *Case) ParamNames() []string {
	out := make([]string, 0, len(c.Params))
	for n := range c.Params {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Step returns the step with the given id.
func (c *Case) Step(id string) (Step, bool) {
	for _, s := range c.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return Step{}, false
}

// Validate returns every problem in the case (nil when valid).
func (c *Case) Validate() ValidationErrors {
	var errs ValidationErrors
	add := func(step, field, msg string, a ...any) {
		errs = append(errs, ValidationError{Step: step, Field: field, Message: fmt.Sprintf(msg, a...)})
	}
	if c.Version != 1 {
		add("", "version", "must be 1 (got %d)", c.Version)
	}
	if strings.TrimSpace(c.Name) == "" {
		add("", "name", "is required")
	}
	if !platforms[c.App.Platform] {
		add("", "app.platform", "must be any, android or ios (got %q)", c.App.Platform)
	}
	if !platforms[c.Device.Platform] {
		add("", "device.platform", "must be any, android or ios (got %q)", c.Device.Platform)
	}
	for _, n := range c.ParamNames() {
		if !identRe.MatchString(n) {
			add("", "params", "parameter name %q is not a valid identifier", n)
		}
	}
	seenSecret := map[string]bool{}
	for _, id := range c.Secrets {
		switch {
		case !identRe.MatchString(id):
			add("", "secrets", "secret id %q is not a valid identifier", id)
		case seenSecret[id]:
			add("", "secrets", "duplicate secret id %q", id)
		}
		seenSecret[id] = true
	}
	if len(c.Steps) == 0 {
		add("", "steps", "at least one step is required")
	}
	seen := map[string]bool{}
	for i, s := range c.Steps {
		label := s.ID
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
		}
		switch {
		case s.ID == "":
			add(label, "id", "is required")
		case !stepIDRe.MatchString(s.ID):
			add(label, "id", "%q must match [A-Za-z0-9_-]+", s.ID)
		case seen[s.ID]:
			add(label, "id", "duplicate step id")
		}
		if s.ID != "" {
			seen[s.ID] = true
		}
		switch s.Type {
		case StepLogin, StepAct, StepAssert, StepNav:
		case "":
			add(label, "type", "is required (login, act, assert or nav)")
		default:
			add(label, "type", "unknown type %q (use login, act, assert or nav)", s.Type)
		}
		if strings.TrimSpace(s.Do) == "" {
			add(label, "do", "is required")
		}
		if s.Type == StepAssert && strings.TrimSpace(s.Expect) == "" {
			add(label, "expect", "is required for assert steps")
		}
		for _, f := range []struct{ field, text string }{{"do", s.Do}, {"expect", s.Expect}} {
			for _, ref := range ParamRefs(f.text) {
				if _, ok := c.Params[ref]; !ok {
					add(label, f.field, "references undeclared parameter ${%s}", ref)
				}
			}
		}
	}
	return errs
}
