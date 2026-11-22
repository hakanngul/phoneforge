// Package mobtest implements Pocketlab's test projects: .mob script
// parsing and formatting, on-disk projects, a runner built on the dsl
// engine, run history and the HTTP routes that expose them.
package mobtest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"pocketlab/internal/dsl"
)

// ParseError is a problem found in a .mob script. Line is 1-based (0 when
// the problem is not tied to a line).
type ParseError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

func (e ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Message)
	}
	return e.Message
}

// ParseErrors is a list of ParseError usable as an error.
type ParseErrors []ParseError

func (e ParseErrors) Error() string {
	parts := make([]string, len(e))
	for i, p := range e {
		parts[i] = p.Error()
	}
	return strings.Join(parts, "; ")
}

// ParseOptions tune ParseWith.
type ParseOptions struct {
	// ReadFile returns the source of a sibling script for `run "./x.mob"`.
	// The name has no directory part. Nil makes `run` an error unless IgnoreRun is set.
	ReadFile func(name string) ([]byte, error)
	// IgnoreRun accepts `run` lines without resolving them (syntax checks only).
	IgnoreRun bool
}

const maxIncludeDepth = 4

// Parse parses a .mob script. It collects every error it can find.
func Parse(src []byte) (dsl.Script, []ParseError) { return ParseWith(src, ParseOptions{}) }

// ParseWith is Parse with options (see ParseOptions).
func ParseWith(src []byte, opts ParseOptions) (dsl.Script, []ParseError) {
	return parseDepth(src, opts, nil)
}

func parseDepth(src []byte, opts ParseOptions, stack []string) (dsl.Script, []ParseError) {
	text := strings.TrimPrefix(string(src), "\xef\xbb\xbf")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	p := &parser{lines: strings.Split(text, "\n"), opts: opts, stack: stack}
	p.script.Version = dsl.Version
	steps := p.parseSteps(false, 0)
	if p.platform != "" {
		p.err(p.platLine, "missing '# end' for the '# %s' block", p.platform)
	}
	p.script.Steps = steps
	if p.script.Steps == nil {
		p.script.Steps = []dsl.Step{}
	}
	return p.script, p.errs
}

type parser struct {
	lines     []string
	i         int
	errs      []ParseError
	opts      ParseOptions
	script    dsl.Script
	platform  string
	platLine  int
	stack     []string
	closeRest []token
}

func (p *parser) err(line int, format string, args ...any) {
	p.errs = append(p.errs, ParseError{Line: line, Message: fmt.Sprintf(format, args...)})
}

var (
	paramRe    = regexp.MustCompile(`(?i)^#\s*param\s*:\s*([A-Za-z_][A-Za-z0-9_.\-]*)\s*(?:=\s*(.*))?$`)
	alertsRe   = regexp.MustCompile(`(?i)^#\s*alerts\s*:\s*(\S*)\s*$`)
	platformRe = regexp.MustCompile(`(?i)^#\s*\[?\s*(ios|android|end)\s*\]?\s*$`)
)

func (p *parser) header(ln int, line string) {
	if m := paramRe.FindStringSubmatch(line); m != nil {
		if p.script.Params == nil {
			p.script.Params = map[string]dsl.ParamSpec{}
		}
		spec := dsl.ParamSpec{}
		if strings.Contains(line, "=") {
			spec.Has = true
			v := strings.TrimSpace(m[2])
			if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
				v = unescapeString(v[1 : len(v)-1])
			}
			spec.Default = v
		}
		p.script.Params[m[1]] = spec
		return
	}
	if m := alertsRe.FindStringSubmatch(line); m != nil {
		v := strings.ToLower(m[1])
		if v != "accept" && v != "dismiss" {
			p.err(ln, "# Alerts must be accept or dismiss, got %q", m[1])
			return
		}
		p.script.Alerts = v
		return
	}
	if m := platformRe.FindStringSubmatch(line); m != nil {
		switch w := strings.ToLower(m[1]); w {
		case "end":
			if p.platform == "" {
				p.err(ln, "'# end' without an open platform block")
			}
			p.platform = ""
		default:
			if p.platform != "" {
				p.err(ln, "platform blocks cannot be nested (still inside '# %s')", p.platform)
			}
			p.platform, p.platLine = w, ln
		}
	}
	// any other # line is a comment
}

// parseSteps parses lines until EOF or (inside a block) a closing brace.
func (p *parser) parseSteps(inBlock bool, blockLine int) []dsl.Step {
	var steps []dsl.Step
	p.closeRest = nil
	closed := false
	for p.i < len(p.lines) {
		ln := p.i + 1
		line := strings.TrimSpace(p.lines[p.i])
		p.i++
		if line == "" {
			continue
		}
		if line[0] == '#' {
			p.header(ln, line)
			continue
		}
		toks, err := scan(line)
		if err != nil {
			p.err(ln, "%v", err)
			if strings.HasSuffix(line, "{") {
				p.parseSteps(true, ln) // keep brace structure in sync
			}
			continue
		}
		if toks[0].kind == tClose {
			if !inBlock {
				p.err(ln, "unexpected '}'")
				continue
			}
			p.closeRest = toks[1:]
			closed = true
			break
		}
		opens := false
		if toks[len(toks)-1].kind == tOpen {
			opens = true
			toks = toks[:len(toks)-1]
		}
		before := len(p.errs)
		sts, err := p.build(ln, toks, opens)
		if err == nil {
			for _, st := range sts {
				chk := st
				chk.Then, chk.Else, chk.Body = nil, nil, nil
				if verr := dsl.Validate(dsl.Script{Steps: []dsl.Step{chk}}); verr != nil {
					err = fmt.Errorf("%s", strings.TrimPrefix(strings.TrimPrefix(verr.Error(), "dsl: "), "step 0: "))
				}
			}
		}
		if err != nil {
			p.err(ln, "%v", err)
		}
		if opens {
			p.finishBlock(ln, sts, err == nil || len(p.errs) == before)
		}
		for _, st := range sts {
			if p.platform != "" && st.Platform == "" {
				st.Platform = p.platform
			}
			steps = append(steps, st)
		}
	}
	if inBlock && !closed {
		p.err(blockLine, "block opened here is missing its closing '}'")
	}
	return steps
}

// finishBlock reads the block(s) that follow an if_exists / repeat line.
// sts holds the step built from the header line (empty on error).
func (p *parser) finishBlock(ln int, sts []dsl.Step, ok bool) {
	var st *dsl.Step
	if len(sts) == 1 {
		st = &sts[0]
	}
	body := p.parseSteps(true, ln)
	rest := p.closeRest
	if st != nil && st.Action == "repeat" {
		st.Body = body
	} else if st != nil {
		st.Then = body
	}
	if len(rest) == 0 {
		return
	}
	if st != nil && st.Action == "if_exists" && len(rest) == 2 && rest[0].kind == tWord && strings.EqualFold(rest[0].s, "else") && rest[1].kind == tOpen {
		els := p.parseSteps(true, ln)
		st.Else = els
		if len(p.closeRest) > 0 {
			p.err(p.i, "unexpected text after '}'")
		}
		return
	}
	if st != nil {
		p.err(p.i, "unexpected text after '}'")
	}
}

// ---------- tokens ----------

type tkind int

const (
	tWord tkind = iota
	tString
	tContains
	tRegex
	tID
	tIndex
	tKV
	tArrow
	tOpen
	tClose
)

type token struct {
	kind  tkind
	s     string
	key   string // tKV
	vkind tkind  // tKV: tString, tContains, tRegex or tWord
	ci    bool   // regex flagged case-insensitive
	raw   string
}

var (
	kvRe    = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*):(.+)$`)
	indexRe = regexp.MustCompile(`^\[(\d+)\]$`)
)

func scan(line string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(line) {
		if line[i] == ' ' || line[i] == '\t' {
			i++
			continue
		}
		if strings.HasPrefix(line[i:], "→") {
			toks = append(toks, token{kind: tArrow, raw: "→"})
			i += len("→")
			continue
		}
		if strings.HasPrefix(line[i:], "->") {
			toks = append(toks, token{kind: tArrow, raw: "->"})
			i += 2
			continue
		}
		j, inQuote := i, false
	loop:
		for j < len(line) {
			c := line[j]
			if inQuote {
				switch c {
				case '\\':
					j += 2
					continue
				case '"':
					inQuote = false
					j++
					rest := line[j:]
					if strings.HasPrefix(rest, "→") || strings.HasPrefix(rest, "->") {
						break loop
					}
					continue
				}
				j++
				continue
			}
			if c == '"' {
				inQuote = true
				j++
				continue
			}
			if c == ' ' || c == '\t' {
				break
			}
			j++
		}
		if inQuote || j > len(line) {
			return nil, fmt.Errorf("unterminated string")
		}
		t, err := classify(line[i:j])
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		i = j
	}
	if len(toks) == 0 {
		return nil, fmt.Errorf("empty line")
	}
	return toks, nil
}

// quotedEnd returns the index of the closing quote of the string starting at s[0]=='"'.
func quotedEnd(s string) int {
	for k := 1; k < len(s); k++ {
		switch s[k] {
		case '\\':
			k++
		case '"':
			return k
		}
	}
	return -1
}

func unescapeString(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for k := 0; k < len(s); k++ {
		if s[k] == '\\' && k+1 < len(s) {
			switch s[k+1] {
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte('\\')
				b.WriteByte(s[k+1])
			}
			k++
			continue
		}
		b.WriteByte(s[k])
	}
	return b.String()
}

// parseValue classifies a quoted/prefixed value: "x", ~"x", /"x"/[i] or bare.
func parseValue(raw string) (kind tkind, s string, ci bool, err error) {
	switch {
	case strings.HasPrefix(raw, `~"`):
		end := quotedEnd(raw[1:])
		if end < 0 || end != len(raw)-2 {
			return 0, "", false, fmt.Errorf("malformed ~\"...\" in %s", raw)
		}
		return tContains, unescapeString(raw[2 : len(raw)-1]), false, nil
	case strings.HasPrefix(raw, `/"`):
		end := quotedEnd(raw[1:])
		if end < 0 {
			return 0, "", false, fmt.Errorf("malformed regex literal %s", raw)
		}
		tail := raw[1+end+1:]
		pat := raw[2 : 1+end]
		if tail != "/" && tail != "/i" {
			return 0, "", false, fmt.Errorf("malformed regex literal %s (use /\"pattern\"/ or /\"pattern\"/i)", raw)
		}
		pat = strings.ReplaceAll(pat, `\"`, `"`)
		return tRegex, pat, tail == "/i", nil
	case strings.HasPrefix(raw, `"`):
		end := quotedEnd(raw)
		if end != len(raw)-1 {
			return 0, "", false, fmt.Errorf("unexpected text after string: %s", raw)
		}
		return tString, unescapeString(raw[1 : len(raw)-1]), false, nil
	}
	return tWord, raw, false, nil
}

func classify(raw string) (token, error) {
	switch raw {
	case "{":
		return token{kind: tOpen, raw: raw}, nil
	case "}":
		return token{kind: tClose, raw: raw}, nil
	}
	if strings.HasPrefix(raw, `"`) || strings.HasPrefix(raw, `~"`) || strings.HasPrefix(raw, `/"`) {
		k, s, ci, err := parseValue(raw)
		if err != nil {
			return token{}, err
		}
		return token{kind: k, s: s, ci: ci, raw: raw}, nil
	}
	if len(raw) > 1 && raw[0] == '#' {
		return token{kind: tID, s: raw[1:], raw: raw}, nil
	}
	if m := indexRe.FindStringSubmatch(raw); m != nil {
		return token{kind: tIndex, s: m[1], raw: raw}, nil
	}
	if m := kvRe.FindStringSubmatch(raw); m != nil && !strings.HasPrefix(m[2], "//") {
		k, s, ci, err := parseValue(m[2])
		if err != nil {
			return token{}, err
		}
		return token{kind: tKV, key: strings.ToLower(m[1]), s: s, vkind: k, ci: ci, raw: raw}, nil
	}
	return token{kind: tWord, s: raw, raw: raw}, nil
}

// ---------- step building ----------

type cursor struct {
	t []token
	i int
}

func (c *cursor) more() bool    { return c.i < len(c.t) }
func (c *cursor) peek() token   { return c.t[c.i] }
func (c *cursor) take() token   { t := c.t[c.i]; c.i++; return t }
func (c *cursor) rest() []token { return c.t[c.i:] }

var predKeys = map[string]bool{
	"text": true, "text_contains": true, "text_starts_with": true, "text_regex": true, "regex": true,
	"value": true, "value_contains": true, "resource_id": true, "accessibility_id": true, "label": true,
	"label_contains": true, "role": true, "type": true, "enabled": true, "visible": true, "selected": true,
	"bounds": true, "bounds_hint": true, "parent_of": true, "index": true, "css_selector": true,
}

func isPredToken(t token) bool {
	switch t.kind {
	case tString, tContains, tRegex, tID, tIndex:
		return true
	case tKV:
		return predKeys[t.key]
	case tWord:
		return t.s == "near"
	}
	return false
}

func regexOf(t token) string {
	if t.ci {
		return "(?i)" + t.s
	}
	return t.s
}

func selFromToken(sel *dsl.Selector, t token) error {
	switch t.kind {
	case tString:
		if sel.Text != "" {
			return fmt.Errorf("more than one quoted text in a predicate (use ~\"...\" for contains)")
		}
		sel.Text = t.s
	case tContains:
		sel.TextContains = t.s
	case tRegex:
		sel.TextRegex = regexOf(t)
	case tID:
		sel.AccessibilityID = t.s
	default:
		return fmt.Errorf("unexpected token %s", t.raw)
	}
	return nil
}

func boolVal(t token) (*bool, error) {
	switch strings.ToLower(t.s) {
	case "true":
		v := true
		return &v, nil
	case "false":
		v := false
		return &v, nil
	}
	return nil, fmt.Errorf("%s must be true or false", t.key)
}

// valueSelector turns a quoted/~/regex value into a one-field selector.
func valueSelector(t token) dsl.Selector {
	switch t.vkind {
	case tContains:
		return dsl.Selector{TextContains: t.s}
	case tRegex:
		return dsl.Selector{TextRegex: regexOf(t)}
	}
	return dsl.Selector{Text: t.s}
}

func setPredKV(sel *dsl.Selector, t token) error {
	switch t.key {
	case "text":
		v := valueSelector(t)
		sel.Text, sel.TextContains, sel.TextRegex = orStr(sel.Text, v.Text), orStr(sel.TextContains, v.TextContains), orStr(sel.TextRegex, v.TextRegex)
	case "text_contains":
		sel.TextContains = t.s
	case "text_starts_with":
		sel.TextStartsWith = t.s
	case "text_regex", "regex":
		sel.TextRegex = regexOf(t)
	case "value":
		sel.Value = t.s
	case "value_contains":
		sel.ValueContains = t.s
	case "resource_id":
		sel.ResourceID = t.s
	case "accessibility_id":
		sel.AccessibilityID = t.s
	case "label":
		sel.Label = t.s
	case "label_contains":
		sel.LabelContains = t.s
	case "role":
		sel.Role = t.s
	case "type":
		sel.Type = t.s
	case "bounds", "bounds_hint":
		sel.BoundsHint = t.s
	case "css_selector":
		sel.CSSSelector = t.s
	case "parent_of":
		inner := valueSelector(t)
		sel.ParentOf = &inner
	case "enabled", "visible", "selected":
		b, err := boolVal(t)
		if err != nil {
			return err
		}
		switch t.key {
		case "enabled":
			sel.Enabled = b
		case "visible":
			sel.Visible = b
		default:
			sel.Selected = b
		}
	case "index":
		n, err := strconv.Atoi(t.s)
		if err != nil || n < 0 {
			return fmt.Errorf("index must be a non-negative integer")
		}
		sel.Index = n
	default:
		return fmt.Errorf("unknown predicate %s:", t.key)
	}
	return nil
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// pred consumes one predicate token (plus a near group) into sel.
func pred(c *cursor, sel *dsl.Selector) error {
	t := c.take()
	switch t.kind {
	case tString, tContains, tRegex, tID:
		return selFromToken(sel, t)
	case tIndex:
		n, _ := strconv.Atoi(t.s)
		sel.Index = n
		return nil
	case tKV:
		return setPredKV(sel, t)
	case tWord: // near
		if !c.more() {
			return fmt.Errorf("near needs a target, e.g. near \"Email\"")
		}
		a := c.peek()
		if a.kind != tString && a.kind != tContains && a.kind != tRegex && a.kind != tID {
			return fmt.Errorf("near needs a quoted target, e.g. near \"Email\"")
		}
		c.take()
		near := &dsl.Near{}
		if err := selFromToken(&near.Selector, a); err != nil {
			return err
		}
		for c.more() && c.peek().kind == tKV && (c.peek().key == "direction" || c.peek().key == "max_distance") {
			kv := c.take()
			if kv.key == "direction" {
				near.Direction = strings.ToLower(kv.s)
			} else {
				f, err := strconv.ParseFloat(kv.s, 64)
				if err != nil {
					return fmt.Errorf("max_distance must be a number")
				}
				near.MaxDistance = f
			}
		}
		sel.Near = near
	}
	return nil
}

var (
	coordRe  = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)(%?),(-?\d+(?:\.\d+)?)(%?)$`)
	failSet  = map[string]bool{"abort": true, "skip": true, "retry": true, "replan": true, "require_user": true}
	dirWords = map[string]bool{"up": true, "down": true, "left": true, "right": true}
)

func parseCoords(s string) (*dsl.Coords, bool) {
	m := coordRe.FindStringSubmatch(s)
	if m == nil {
		return nil, false
	}
	x, _ := strconv.ParseFloat(m[1], 64)
	y, _ := strconv.ParseFloat(m[3], 64)
	c := &dsl.Coords{}
	if m[2] == "%" {
		c.XPercent = &x
	} else {
		c.X = &x
	}
	if m[4] == "%" {
		c.YPercent = &y
	} else {
		c.Y = &y
	}
	return c, true
}

func policy(st *dsl.Step) *dsl.FailPolicy {
	if st.OnFail == nil {
		st.OnFail = &dsl.FailPolicy{}
	}
	return st.OnFail
}

func applySetting(st *dsl.Step, t token) error {
	num := func() (int, error) {
		n, err := strconv.Atoi(t.s)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer, got %q", t.key, t.s)
		}
		return n, nil
	}
	flt := func() (*float64, error) {
		f, err := strconv.ParseFloat(t.s, 64)
		if err != nil {
			return nil, fmt.Errorf("%s must be a number, got %q", t.key, t.s)
		}
		return &f, nil
	}
	var err error
	switch t.key {
	case "timeout", "timeout_ms":
		st.TimeoutMs, err = num()
	case "duration", "duration_ms":
		st.DurationMs, err = num()
	case "on_fail":
		if !failSet[t.s] {
			return fmt.Errorf("on_fail must be abort, skip, retry, replan or require_user, got %q", t.s)
		}
		policy(st).Strategy = t.s
	case "fallback", "fallback_strategy":
		if !failSet[t.s] {
			return fmt.Errorf("fallback must be abort, skip, replan or require_user, got %q", t.s)
		}
		policy(st).FallbackStrategy = t.s
	case "retries", "max_retries":
		var n int
		if n, err = num(); err == nil {
			st.Retries = &n
		}
	case "retry_delay", "retry_delay_ms":
		var n int
		if n, err = num(); err == nil {
			st.RetryDelayMs = &n
		}
	case "max", "max_iterations":
		st.MaxIterations, err = num()
	case "max_scrolls":
		st.MaxScrolls, err = num()
	case "max_swipes":
		st.MaxSwipes, err = num()
	case "settle_ms":
		var n int
		if n, err = num(); err == nil {
			st.SettleMs = &n
		}
	case "poll_interval", "poll_interval_ms":
		st.PollIntervalMs, err = num()
	case "press_duration", "press_duration_ms":
		st.PressDurationMs, err = num()
	case "hold_duration", "hold_duration_ms":
		st.HoldDurationMs, err = num()
	case "distance":
		st.Distance = t.s
	case "direction":
		st.Direction = strings.ToLower(t.s)
	case "amount":
		st.Amount = t.s
	case "scale":
		st.Scale, err = flt()
	case "velocity":
		st.Velocity, err = flt()
	case "lat":
		st.Lat, err = flt()
	case "lon":
		st.Lon, err = flt()
	case "angle":
		st.Angle, err = flt()
	case "threshold", "threshold_percent":
		st.ThresholdPercent, err = flt()
	case "expected":
		var n int
		if n, err = num(); err == nil {
			st.Expected = &n
		}
	case "property":
		st.Property = t.s
	case "expected_value":
		v := dsl.FlexString(t.s)
		st.ExpectedValue = &v
	case "message":
		st.Message = t.s
	case "id":
		st.ID = t.s
	case "store_as":
		st.StoreAs = t.s
	case "key":
		st.Key = t.s
	case "state":
		st.State = t.s
	case "sensor":
		st.Sensor = t.s
	case "name":
		st.Name = t.s
	case "file_path":
		st.FilePath = t.s
	case "js", "condition":
		st.Condition = t.s
	case "extract_regex":
		st.ExtractRegex = t.s
	case "platform":
		if t.s != "ios" && t.s != "android" {
			return fmt.Errorf("platform must be ios or android")
		}
		st.Platform = t.s
	case "arg":
		st.Arguments = append(st.Arguments, t.s)
	case "env":
		k, v, ok := strings.Cut(t.s, "=")
		if !ok {
			return fmt.Errorf("env must be KEY=VALUE")
		}
		if st.Environment == nil {
			st.Environment = map[string]string{}
		}
		st.Environment[k] = v
	case "include":
		list := strings.Split(t.s, ",")
		b, _ := json.Marshal(list)
		st.Include = b
	case "from", "to":
		c, ok := parseCoords(t.s)
		if !ok {
			return fmt.Errorf("%s: expected coordinates like 200,700, got %q", t.key, t.s)
		}
		if t.key == "from" {
			st.FromCoords = c
		} else {
			st.ToCoords = c
		}
	default:
		return fmt.Errorf("unknown setting %s:", t.key)
	}
	return err
}

// argState tracks the predicate currently being filled.
type argState struct {
	sel    dsl.Selector
	used   bool
	coords *dsl.Coords
}

// scanArgs walks the remaining tokens: predicate tokens fill as.sel, settings
// apply to st, anything else goes to word (nil = not allowed).
func scanArgs(c *cursor, st *dsl.Step, as **argState, word func(t token) (bool, error)) error {
	for c.more() {
		t := c.peek()
		switch {
		case isPredToken(t):
			if err := pred(c, &(*as).sel); err != nil {
				return err
			}
			(*as).used = true
		case t.kind == tKV:
			c.take()
			if err := applySetting(st, t); err != nil {
				return err
			}
		case t.kind == tWord && coordRe.MatchString(t.s):
			c.take()
			(*as).coords, _ = parseCoords(t.s)
		default:
			if word != nil {
				c.take()
				ok, err := word(t)
				if err != nil {
					return err
				}
				if ok {
					continue
				}
			}
			return fmt.Errorf("unexpected %q", t.raw)
		}
	}
	return nil
}

func (a *argState) selector() *dsl.Selector {
	if !a.used {
		return nil
	}
	s := a.sel
	return &s
}

// mobName is the JSON action name emitted for a canonical action.
var mobName = map[string]string{
	"launch_app": "open_app", "stop_app": "kill_app", "open_url": "open_link",
	"dismiss_keyboard": "hide_keyboard", "wait": "delay", "wait_stable": "wait_for",
}

var blockActions = map[string]bool{"if_exists": true, "repeat": true}

func (p *parser) build(ln int, toks []token, opens bool) ([]dsl.Step, error) {
	if toks[0].kind != tWord {
		return nil, fmt.Errorf("a line must start with an action keyword, got %s", toks[0].raw)
	}
	kw := strings.ToLower(toks[0].s)
	if kw == "else" {
		return nil, fmt.Errorf("'else' must follow a closing brace: } else {")
	}
	c := &cursor{t: toks[1:]}
	if kw == "run" {
		if opens {
			return nil, fmt.Errorf("run does not take a block")
		}
		return p.buildRun(ln, c)
	}
	canon, ok := dsl.CanonicalAction(kw)
	if !ok {
		return nil, fmt.Errorf("unknown action %q", toks[0].s)
	}
	if opens && !blockActions[canon] {
		return nil, fmt.Errorf("%s does not take a block", kw)
	}
	if blockActions[canon] && !opens {
		return nil, fmt.Errorf("%s needs a block: end the line with {", kw)
	}
	st := dsl.Step{Action: canon}
	if n, ok := mobName[canon]; ok {
		st.Action = n
	}
	as := &argState{}
	if err := p.fill(canon, kw, &st, c, &as); err != nil {
		return nil, err
	}
	return []dsl.Step{st}, nil
}

// firstText consumes the next token as a literal string or bare word.
func firstText(c *cursor, what string) (string, error) {
	if !c.more() {
		return "", fmt.Errorf("%s is missing", what)
	}
	t := c.peek()
	if t.kind != tString && t.kind != tWord {
		return "", fmt.Errorf("%s must be a quoted string or word, got %s", what, t.raw)
	}
	c.take()
	return t.s, nil
}

func (p *parser) fill(canon, kw string, st *dsl.Step, c *cursor, as **argState) error {
	settingsOnly := func() error { return scanArgs(c, st, as, nil) }
	switch canon {
	case "launch_app", "stop_app", "uninstall_app":
		id, err := firstText(c, "bundle id")
		if err != nil {
			return err
		}
		st.BundleID = id
		return scanArgs(c, st, as, func(t token) (bool, error) {
			if t.kind == tWord && strings.ToLower(t.s) == "fresh" && canon == "launch_app" {
				st.Fresh = true
				return true, nil
			}
			return false, nil
		})
	case "open_url":
		u, err := firstText(c, "url")
		if err != nil {
			return err
		}
		st.URL = u
		return settingsOnly()
	case "tap", "double_tap", "long_press", "two_finger_tap", "pinch", "clear", "copy_text", "toggle",
		"assert_exists", "assert_not_exists", "assert_count", "assert_property", "wait_for", "wait_stable",
		"scroll_until_visible", "select_picker":
		if canon == "wait_stable" {
			st.Stable = true
		}
		if canon == "select_picker" {
			return p.fillArrow(c, st, as, func(v string) { st.Value = v })
		}
		err := scanArgs(c, st, as, func(t token) (bool, error) {
			lw := strings.ToLower(t.s)
			switch {
			case canon == "toggle" && t.kind == tWord && (lw == "on" || lw == "off"):
				st.State = lw
			case canon == "wait_for" && t.kind == tWord && lw == "stable":
				st.Stable = true
			case canon == "scroll_until_visible" && t.kind == tWord && dirWords[lw]:
				st.Direction = lw
			default:
				return false, nil
			}
			return true, nil
		})
		if err != nil {
			return err
		}
		if canon == "wait_stable" {
			st.Action = "wait_for"
		}
		return finishTarget(canon, st, *as)
	case "type", "type_secret":
		return p.fillType(canon, c, st, as)
	case "press_key":
		k, err := firstText(c, "key")
		if err != nil {
			return err
		}
		st.Key = strings.ToLower(k)
		return settingsOnly()
	case "navigate":
		t, err := firstText(c, "navigate target")
		if err != nil {
			return err
		}
		st.Target = t
		return settingsOnly()
	case "dismiss_keyboard", "paste_text", "reset_location", "record_start", "record_stop", "audit",
		"metrics_start", "metrics_stop", "request_user_input", "assert_screen_changed":
		return settingsOnly()
	case "observe":
		var inc []string
		err := scanArgs(c, st, as, func(t token) (bool, error) {
			if t.kind == tWord {
				inc = append(inc, strings.ToLower(t.s))
				return true, nil
			}
			return false, nil
		})
		if len(inc) > 0 {
			b, _ := json.Marshal(inc)
			st.Include = b
		}
		return err
	case "screenshot":
		if c.more() && (c.peek().kind == tString) {
			st.FilePath = c.take().s
		}
		return settingsOnly()
	case "wait":
		if !c.more() || c.peek().kind != tWord {
			return fmt.Errorf("%s needs a duration in milliseconds, e.g. delay 1000", kw)
		}
		n, err := strconv.Atoi(c.take().s)
		if err != nil || n <= 0 {
			return fmt.Errorf("%s duration must be a positive integer (milliseconds)", kw)
		}
		st.DurationMs = n
		return settingsOnly()
	case "checkpoint":
		n, err := firstText(c, "checkpoint name")
		if err != nil {
			return err
		}
		st.Name = n
		return settingsOnly()
	case "swipe":
		return scanArgs(c, st, as, func(t token) (bool, error) {
			if lw := strings.ToLower(t.s); t.kind == tWord && dirWords[lw] {
				st.Direction = lw
				return true, nil
			}
			return false, nil
		})
	case "scroll":
		return p.fillScroll(c, st, as)
	case "drag":
		return p.fillDrag(c, st, as)
	case "set_location":
		return fillLocation(c, st)
	case "set_permissions":
		id, err := firstText(c, "bundle id")
		if err != nil {
			return err
		}
		st.BundleID = id
		st.Permissions = map[string]string{}
		for c.more() && c.peek().kind == tWord {
			k, v, ok := strings.Cut(c.take().s, "=")
			if !ok || k == "" {
				return fmt.Errorf("permissions look like camera=allow")
			}
			st.Permissions[k] = strings.ToLower(v)
		}
		if len(st.Permissions) == 0 {
			return fmt.Errorf("set_permissions needs name=allow|deny|unset pairs")
		}
		return settingsOnly()
	case "biometrics":
		if !c.more() || c.peek().kind != tWord {
			return fmt.Errorf("biometrics needs a state: match, nomatch, enroll or unenroll")
		}
		st.State = strings.ToLower(c.take().s)
		if c.more() && c.peek().kind == tWord {
			st.Sensor = strings.ToLower(c.take().s)
		}
		return settingsOnly()
	case "fold":
		if !c.more() || c.peek().kind != tWord {
			return fmt.Errorf("fold needs closed, book, open or an angle")
		}
		w := c.take().s
		if f, err := strconv.ParseFloat(w, 64); err == nil {
			st.Angle = &f
		} else {
			st.State = strings.ToLower(w)
		}
		return settingsOnly()
	case "siri":
		s, err := firstText(c, "prompt")
		if err != nil {
			return err
		}
		st.Prompt = s
		return settingsOnly()
	case "eval_script":
		s, err := firstText(c, "script")
		if err != nil {
			return err
		}
		st.Script = s
		return settingsOnly()
	case "run_script":
		s, err := firstText(c, "script path")
		if err != nil {
			return err
		}
		st.ScriptPath = s
		return settingsOnly()
	case "extract":
		return p.fillExtract(c, st, as)
	case "if_exists":
		if err := scanArgs(c, st, as, nil); err != nil {
			return err
		}
		return finishTarget(canon, st, *as)
	case "repeat":
		return p.fillRepeat(c, st, as)
	}
	return fmt.Errorf("%s is not supported in .mob scripts (use a JSON script)", kw)
}

// finishTarget stores the collected predicate / coordinates on the step.
func finishTarget(canon string, st *dsl.Step, as *argState) error {
	if as.used {
		st.Selector = as.selector()
	}
	if as.coords != nil {
		if as.used {
			return fmt.Errorf("give either a predicate or coordinates, not both")
		}
		st.Coords = as.coords
	}
	return nil
}

func (p *parser) fillArrow(c *cursor, st *dsl.Step, as **argState, set func(string)) error {
	var left, right []token
	seen := false
	for _, t := range c.rest() {
		if t.kind == tArrow && !seen {
			seen = true
			continue
		}
		if seen {
			right = append(right, t)
		} else {
			left = append(left, t)
		}
	}
	if !seen {
		return fmt.Errorf("expected '→' (or '->') followed by a quoted value")
	}
	if err := scanArgs(&cursor{t: left}, st, as, nil); err != nil {
		return err
	}
	rc := &cursor{t: right}
	if !rc.more() || rc.peek().kind != tString {
		return fmt.Errorf("a quoted value must follow the arrow")
	}
	set(rc.take().s)
	dummy := &argState{}
	if err := scanArgs(rc, st, &dummy, nil); err != nil {
		return err
	}
	return finishTarget("", st, *as)
}

func (p *parser) fillType(canon string, c *cursor, st *dsl.Step, as **argState) error {
	set := func(v string) {
		if canon == "type" {
			st.Text = v
		} else {
			st.SecretID = v
		}
	}
	hasArrow := false
	for _, t := range c.rest() {
		if t.kind == tArrow {
			hasArrow = true
		}
	}
	if !hasArrow {
		// type "text"  (into the focused field)
		if c.more() && c.peek().kind == tString {
			set(c.take().s)
			rest := &argState{}
			if err := p.typeFlags(c, st, &rest); err != nil {
				return err
			}
			return nil
		}
		return fmt.Errorf("expected: type <field> → \"text\"  or  type \"text\"")
	}
	var left, right []token
	seen := false
	for _, t := range c.rest() {
		if t.kind == tArrow && !seen {
			seen = true
			continue
		}
		if seen {
			right = append(right, t)
		} else {
			left = append(left, t)
		}
	}
	if err := scanArgs(&cursor{t: left}, st, as, nil); err != nil {
		return err
	}
	rc := &cursor{t: right}
	if !rc.more() || rc.peek().kind != tString {
		return fmt.Errorf("a quoted value must follow the arrow")
	}
	set(rc.take().s)
	if err := p.typeFlags(rc, st, as); err != nil {
		return err
	}
	return finishTarget(canon, st, *as)
}

func (p *parser) typeFlags(c *cursor, st *dsl.Step, as **argState) error {
	return scanArgs(c, st, as, func(t token) (bool, error) {
		if t.kind != tWord {
			return false, nil
		}
		switch strings.ToLower(t.s) {
		case "clear":
			st.ClearFirst = true
		case "dismiss", "dismiss_keyboard", "hide_keyboard":
			st.DismissKeyboard = true
		default:
			return false, nil
		}
		return true, nil
	})
}

func (p *parser) fillScroll(c *cursor, st *dsl.Step, as **argState) error {
	target := &argState{}
	view := &argState{}
	cur := as
	*as = view
	fully := false
	var toUsed bool
	err := scanArgs(c, st, cur, func(t token) (bool, error) {
		if t.kind != tWord {
			return false, nil
		}
		switch lw := strings.ToLower(t.s); {
		case dirWords[lw]:
			st.Direction = lw
		case lw == "to":
			toUsed = true
			*cur = target
		case lw == "in":
			*cur = view
		case lw == "fully_visible":
			fully = true
		default:
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	if toUsed {
		if !target.used {
			return fmt.Errorf("scroll ... to needs a predicate, e.g. scroll down to \"Privacy\"")
		}
		st.ToElement = &dsl.ElementRef{Predicate: target.selector(), FullyVisible: fully}
	}
	st.Selector = view.selector()
	if view.coords != nil || target.coords != nil {
		return fmt.Errorf("scroll does not take coordinates")
	}
	return nil
}

func (p *parser) fillDrag(c *cursor, st *dsl.Step, as **argState) error {
	src := &argState{}
	dst := &argState{}
	*as = src
	err := scanArgs(c, st, as, func(t token) (bool, error) {
		if t.kind == tWord && strings.ToLower(t.s) == "to" {
			*as = dst
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	if src.used {
		st.From = &dsl.ElementRef{Predicate: src.selector()}
	}
	if src.coords != nil {
		st.FromCoords = src.coords
	}
	if dst.used {
		st.ToElement = &dsl.ElementRef{Predicate: dst.selector()}
	}
	if dst.coords != nil {
		st.ToCoords = dst.coords
	}
	if st.From == nil && st.FromCoords == nil || st.ToElement == nil && st.ToCoords == nil {
		return fmt.Errorf("drag needs a source and a destination: drag \"A\" to \"B\"")
	}
	return nil
}

func fillLocation(c *cursor, st *dsl.Step) error {
	var parts []string
	for c.more() && c.peek().kind == tWord {
		parts = append(parts, strings.Split(c.take().s, ",")...)
	}
	if len(parts) != 2 {
		return fmt.Errorf("set_location needs lat,lon, e.g. set_location 37.77,-122.42")
	}
	la, err1 := strconv.ParseFloat(parts[0], 64)
	lo, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("set_location needs numeric lat,lon")
	}
	st.Lat, st.Lon = &la, &lo
	dummy := &argState{}
	return scanArgs(c, st, &dummy, nil)
}

func (p *parser) fillExtract(c *cursor, st *dsl.Step, as **argState) error {
	key, err := firstText(c, "extract key")
	if err != nil {
		return err
	}
	st.ExtractKey = key
	for i := c.i; i < len(c.t); i++ { // regex: is the capture regex here, not a predicate
		if c.t[i].kind == tKV && c.t[i].key == "regex" {
			c.t[i].key = "extract_regex"
		}
	}
	if !c.more() {
		return fmt.Errorf("extract needs: from <predicate>, screenshot, or = \"value\"")
	}
	switch t := c.peek(); {
	case t.kind == tWord && t.s == "screenshot":
		c.take()
		st.ExtractKind = "screenshot"
		return scanArgs(c, st, as, nil)
	case t.kind == tWord && t.s == "=":
		c.take()
		if !c.more() || c.peek().kind != tString {
			return fmt.Errorf("a quoted value must follow '='")
		}
		st.ExtractKind = "literal"
		st.Text = c.take().s
		return scanArgs(c, st, as, nil)
	case t.kind == tWord && t.s == "from":
		c.take()
		st.ExtractKind = "text"
		if err := scanArgs(c, st, as, nil); err != nil {
			return err
		}
		return finishTarget("extract", st, *as)
	}
	return fmt.Errorf("extract needs: from <predicate>, screenshot, or = \"value\"")
}

func (p *parser) fillRepeat(c *cursor, st *dsl.Step, as **argState) error {
	if !c.more() {
		return fmt.Errorf("repeat needs: N times | while <predicate> | until <predicate>")
	}
	first := c.peek()
	if first.kind == tWord {
		lw := strings.ToLower(first.s)
		switch lw {
		case "while", "until":
			c.take()
			if err := scanArgs(c, st, as, nil); err != nil {
				return err
			}
			if st.Condition != "" { // repeat while js:"..."
				return nil
			}
			if !(*as).used {
				return fmt.Errorf("repeat %s needs a predicate", lw)
			}
			if lw == "while" {
				st.While = (*as).selector()
			} else {
				st.Until = (*as).selector()
			}
			return nil
		}
		// N times
		if n, err := strconv.Atoi(first.s); err == nil || strings.Contains(first.s, "${") {
			if err == nil && n < 0 {
				return fmt.Errorf("repeat count must not be negative")
			}
			c.take()
			if c.more() && c.peek().kind == tWord && strings.ToLower(c.peek().s) == "times" {
				c.take()
			} else {
				return fmt.Errorf("expected: repeat %s times {", first.s)
			}
			st.Times = dsl.FlexString(first.s)
			return scanArgs(c, st, as, nil)
		}
	}
	return fmt.Errorf("repeat needs: N times | while <predicate> | until <predicate>")
}

// ---------- run (include) ----------

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func (p *parser) buildRun(ln int, c *cursor) ([]dsl.Step, error) {
	if !c.more() || c.peek().kind != tString {
		return nil, fmt.Errorf(`run needs a quoted path, e.g. run "./login.mob"`)
	}
	path := c.take().s
	args := map[string]string{}
	for c.more() {
		t := c.take()
		var k, v string
		switch t.kind {
		case tKV:
			k, v = t.key, t.s
		case tWord:
			kk, rest, found := strings.Cut(t.s, "=")
			if !found {
				return nil, fmt.Errorf("unexpected %q (parameters look like email=\"a@b.c\")", t.raw)
			}
			k = kk
			_, s, _, err := parseValue(rest)
			if err != nil {
				return nil, err
			}
			v = s
		default:
			return nil, fmt.Errorf("unexpected %q (parameters look like email=\"a@b.c\")", t.raw)
		}
		args[k] = v
	}
	if p.opts.IgnoreRun && p.opts.ReadFile == nil {
		return nil, nil
	}
	name := strings.TrimPrefix(path, "./")
	if !nameRe.MatchString(name) || name == "." || name == ".." || strings.Contains(name, "..") {
		return nil, fmt.Errorf("run path %q must be a file in the same project (no directories)", path)
	}
	if p.opts.ReadFile == nil {
		return nil, fmt.Errorf("run is only available inside a test project")
	}
	for _, s := range p.stack {
		if s == name {
			return nil, fmt.Errorf("run %q is recursive", name)
		}
	}
	if len(p.stack) >= maxIncludeDepth {
		return nil, fmt.Errorf("run nesting is too deep (max %d)", maxIncludeDepth)
	}
	src, err := p.opts.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("run %q: %v", name, err)
	}
	sub, errs := parseDepth(src, p.opts, append(append([]string{}, p.stack...), name))
	if len(errs) > 0 {
		return nil, fmt.Errorf("in %s: %s", name, ParseErrors(errs).Error())
	}
	for pn, spec := range sub.Params {
		if _, given := args[pn]; given {
			continue
		}
		if p.script.Params == nil {
			p.script.Params = map[string]dsl.ParamSpec{}
		}
		if _, exists := p.script.Params[pn]; !exists {
			p.script.Params[pn] = spec
		}
	}
	steps := make([]dsl.Step, 0, len(sub.Steps))
	for _, st := range sub.Steps {
		s, err := substTree(st, args)
		if err != nil {
			return nil, err
		}
		steps = append(steps, s)
	}
	return steps, nil
}

func substTree(st dsl.Step, args map[string]string) (dsl.Step, error) {
	out, err := dsl.Substitute(st, args, false)
	if err != nil {
		return st, err
	}
	for _, l := range []*[]dsl.Step{&out.Then, &out.Else, &out.Body} {
		list := *l
		if len(list) == 0 {
			continue
		}
		nl := make([]dsl.Step, len(list))
		for i, s := range list {
			if nl[i], err = substTree(s, args); err != nil {
				return st, err
			}
		}
		*l = nl
	}
	return out, nil
}
