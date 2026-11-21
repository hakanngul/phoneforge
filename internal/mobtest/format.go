package mobtest

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"phoneforge/internal/dsl"
)

// Format renders a script as .mob text. Steps that cannot be expressed in
// .mob (or would not parse back to the same step) are written as a comment
// holding their JSON, so the output always parses.
func Format(s dsl.Script) []byte {
	var b strings.Builder
	names := make([]string, 0, len(s.Params))
	for n := range s.Params {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sp := s.Params[n]
		if sp.Has {
			fmt.Fprintf(&b, "# Param: %s = %s\n", n, paramValue(sp.Default))
		} else {
			fmt.Fprintf(&b, "# Param: %s\n", n)
		}
	}
	if s.Alerts != "" {
		fmt.Fprintf(&b, "# Alerts: %s\n", s.Alerts)
	}
	if b.Len() > 0 && len(s.Steps) > 0 {
		b.WriteString("\n")
	}
	plat := ""
	for _, st := range s.Steps {
		if st.Platform != plat {
			if plat != "" {
				b.WriteString("# end\n")
			}
			plat = st.Platform
			if plat != "" {
				fmt.Fprintf(&b, "# %s\n", plat)
			}
		}
		lines, ok := fmtStep(st, "")
		if ok {
			ok = verify(lines, st)
		}
		if !ok {
			j, _ := json.Marshal(st)
			lines = []string{"# [not representable in .mob] " + string(j)}
		}
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
	}
	if plat != "" {
		b.WriteString("# end\n")
	}
	return []byte(b.String())
}

func paramValue(v string) string {
	if v == "" || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\"\n") || strings.HasPrefix(v, "\"") {
		return q(v)
	}
	return v
}

// verify re-parses lines and checks they yield exactly st.
func verify(lines []string, st dsl.Step) bool {
	sc, errs := ParseWith([]byte(strings.Join(lines, "\n")), ParseOptions{IgnoreRun: true})
	if len(errs) > 0 || len(sc.Steps) != 1 {
		return false
	}
	return sameStep(sc.Steps[0], st)
}

func sameStep(a, b dsl.Step) bool {
	ja, _ := json.Marshal(normStep(a))
	jb, _ := json.Marshal(normStep(b))
	return string(ja) == string(jb)
}

func normStep(st dsl.Step) dsl.Step {
	if c, ok := dsl.CanonicalAction(st.Action); ok {
		st.Action = c
	}
	st.Platform = ""
	if st.Action == "wait_stable" {
		st.Action = "wait_for"
		st.Stable = true
	}
	for _, l := range []*[]dsl.Step{&st.Then, &st.Else, &st.Body} {
		if len(*l) == 0 {
			*l = nil
			continue
		}
		n := make([]dsl.Step, len(*l))
		for i, s := range *l {
			n[i] = normStep(s)
		}
		*l = n
	}
	return st
}

var bareRe = regexp.MustCompile(`^[A-Za-z0-9_.\-/]+$`)

func q(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

func bareOrQuoted(s string) string {
	if bareRe.MatchString(s) {
		return s
	}
	return q(s)
}

func fnum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func fmtCoords(c *dsl.Coords) (string, bool) {
	var x, y string
	switch {
	case c.X != nil && c.XPercent == nil:
		x = fnum(*c.X)
	case c.XPercent != nil && c.X == nil:
		x = fnum(*c.XPercent) + "%"
	default:
		return "", false
	}
	switch {
	case c.Y != nil && c.YPercent == nil:
		y = fnum(*c.Y)
	case c.YPercent != nil && c.Y == nil:
		y = fnum(*c.YPercent) + "%"
	default:
		return "", false
	}
	return x + "," + y, true
}

func fmtRegex(p string) string {
	ci := false
	if strings.HasPrefix(p, "(?i)") {
		ci = true
		p = p[4:]
	}
	out := `/"` + strings.ReplaceAll(p, `"`, `\"`) + `"/`
	if ci {
		out += "i"
	}
	return out
}

// anchor renders a selector that has exactly one of text/contains/regex/id.
func anchor(s dsl.Selector) (string, bool) {
	n := 0
	out := ""
	if s.Text != "" {
		n++
		out = q(s.Text)
	}
	if s.TextContains != "" {
		n++
		out = "~" + q(s.TextContains)
	}
	if s.TextRegex != "" {
		n++
		out = fmtRegex(s.TextRegex)
	}
	if s.AccessibilityID != "" && bareRe.MatchString(s.AccessibilityID) {
		n++
		out = "#" + s.AccessibilityID
	}
	probe := s
	probe.Text, probe.TextContains, probe.TextRegex = "", "", ""
	if bareRe.MatchString(s.AccessibilityID) {
		probe.AccessibilityID = ""
	}
	return out, n == 1 && reflect.DeepEqual(probe, dsl.Selector{})
}

func fmtSel(s dsl.Selector) ([]string, bool) {
	var out []string
	if s.Text != "" {
		out = append(out, q(s.Text))
	}
	if s.TextContains != "" {
		out = append(out, "~"+q(s.TextContains))
	}
	if s.TextRegex != "" {
		out = append(out, fmtRegex(s.TextRegex))
	}
	if s.AccessibilityID != "" {
		if bareRe.MatchString(s.AccessibilityID) {
			out = append(out, "#"+s.AccessibilityID)
		} else {
			out = append(out, "accessibility_id:"+q(s.AccessibilityID))
		}
	}
	kv := func(k, v string) {
		if v != "" {
			out = append(out, k+":"+bareOrQuoted(v))
		}
	}
	kv("type", s.Type)
	kv("role", s.Role)
	kv("resource_id", s.ResourceID)
	kv("label", s.Label)
	kv("label_contains", s.LabelContains)
	kv("text_starts_with", s.TextStartsWith)
	kv("value", s.Value)
	kv("value_contains", s.ValueContains)
	kv("bounds", s.BoundsHint)
	kv("css_selector", s.CSSSelector)
	for _, b := range []struct {
		k string
		v *bool
	}{{"enabled", s.Enabled}, {"visible", s.Visible}, {"selected", s.Selected}} {
		if b.v != nil {
			out = append(out, b.k+":"+strconv.FormatBool(*b.v))
		}
	}
	if s.ParentOf != nil {
		a, ok := anchor(*s.ParentOf)
		if !ok || s.ParentOf.AccessibilityID != "" {
			return nil, false
		}
		out = append(out, "parent_of:"+a)
	}
	if s.Near != nil {
		a, ok := anchor(s.Near.Selector)
		if !ok {
			return nil, false
		}
		out = append(out, "near", a)
		if s.Near.Direction != "" {
			out = append(out, "direction:"+s.Near.Direction)
		}
		if s.Near.MaxDistance != 0 {
			out = append(out, "max_distance:"+fnum(s.Near.MaxDistance))
		}
	}
	if s.Index != 0 {
		out = append(out, "["+strconv.Itoa(s.Index)+"]")
	}
	return out, true
}

type fb struct {
	parts []string
	used  map[string]bool
	bad   bool
}

func (f *fb) add(p ...string) { f.parts = append(f.parts, p...) }
func (f *fb) use(names ...string) {
	for _, n := range names {
		f.used[n] = true
	}
}
func (f *fb) sel(s *dsl.Selector) {
	if s == nil {
		return
	}
	p, ok := fmtSel(*s)
	if !ok {
		f.bad = true
	}
	f.add(p...)
}
func (f *fb) coords(c *dsl.Coords) {
	s, ok := fmtCoords(c)
	if !ok {
		f.bad = true
	}
	f.add(s)
}

func (f *fb) generic(st dsl.Step) {
	n := func(name string, v int) {
		if v != 0 && !f.used[name] {
			f.add(name + ":" + strconv.Itoa(v))
		}
	}
	s := func(name, v string) {
		if v != "" && !f.used[name] {
			f.add(name + ":" + bareOrQuoted(v))
		}
	}
	fl := func(name string, v *float64) {
		if v != nil && !f.used[name] {
			f.add(name + ":" + fnum(*v))
		}
	}
	n("timeout", st.TimeoutMs)
	n("duration", st.DurationMs)
	if st.OnFail != nil {
		s("on_fail", st.OnFail.Strategy)
		s("fallback", st.OnFail.FallbackStrategy)
		if st.OnFail.MaxRetries != nil || st.OnFail.RetryDelayMs != nil {
			f.bad = true
		}
	}
	if st.Retries != nil {
		f.add("retries:" + strconv.Itoa(*st.Retries))
	}
	if st.RetryDelayMs != nil {
		f.add("retry_delay:" + strconv.Itoa(*st.RetryDelayMs))
	}
	if st.SettleMs != nil {
		f.add("settle_ms:" + strconv.Itoa(*st.SettleMs))
	}
	n("max", st.MaxIterations)
	n("max_scrolls", st.MaxScrolls)
	n("max_swipes", st.MaxSwipes)
	n("press_duration", st.PressDurationMs)
	n("hold_duration", st.HoldDurationMs)
	n("poll_interval", st.PollIntervalMs)
	s("distance", st.Distance)
	s("amount", st.Amount)
	s("direction", st.Direction)
	fl("scale", st.Scale)
	fl("velocity", st.Velocity)
	fl("threshold", st.ThresholdPercent)
	if st.Expected != nil && !f.used["expected"] {
		f.add("expected:" + strconv.Itoa(*st.Expected))
	}
	s("property", st.Property)
	if st.ExpectedValue != nil {
		f.add("expected_value:" + bareOrQuoted(string(*st.ExpectedValue)))
	}
	s("message", st.Message)
	s("id", st.ID)
	s("store_as", st.StoreAs)
	s("sensor", st.Sensor)
	if st.FilePath != "" && !f.used["file_path"] {
		f.add("file_path:" + bareOrQuoted(st.FilePath))
	}
	for _, a := range st.Arguments {
		f.add("arg:" + q(a))
	}
	keys := make([]string, 0, len(st.Environment))
	for k := range st.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f.add("env:" + q(k+"="+st.Environment[k]))
	}
}

// fmtStep renders one step (and its nested blocks) at the given indent.
func fmtStep(st dsl.Step, indent string) ([]string, bool) {
	canon, ok := dsl.CanonicalAction(st.Action)
	if !ok {
		return nil, false
	}
	f := &fb{used: map[string]bool{}}
	kw := canon
	rename := map[string]string{"launch_app": "app", "stop_app": "kill_app", "open_url": "open_link",
		"dismiss_keyboard": "hide_keyboard", "wait": "delay", "eval_script": "eval", "run_script": "script"}
	if r, ok := rename[canon]; ok {
		kw = r
	}
	switch canon {
	case "launch_app", "stop_app", "uninstall_app":
		f.add(q(st.BundleID))
		if st.Fresh {
			f.add("fresh")
		}
	case "open_url":
		f.add(q(st.URL))
	case "tap", "double_tap", "long_press", "two_finger_tap", "pinch", "clear", "copy_text", "toggle",
		"assert_exists", "assert_not_exists", "assert_count", "assert_property", "scroll_until_visible":
		f.sel(st.Selector)
		if st.Coords != nil {
			f.coords(st.Coords)
		}
		if canon == "toggle" && st.State != "" {
			f.add(st.State)
			f.use("state")
		}
		if canon == "scroll_until_visible" && st.Direction != "" {
			f.add(st.Direction)
			f.use("direction")
		}
		if (canon == "assert_exists" || canon == "assert_not_exists" || canon == "assert_count" || canon == "assert_property") && st.Selector == nil {
			f.bad = true
		}
	case "wait_for":
		if st.Stable && st.Selector == nil {
			kw = "wait_stable"
		} else if st.Stable {
			f.add("stable")
		}
		f.sel(st.Selector)
	case "select_picker":
		f.sel(st.Selector)
		if st.Coords != nil {
			f.coords(st.Coords)
		}
		f.add("→", q(st.Value))
	case "type", "type_secret":
		val := st.Text
		if canon == "type_secret" {
			val = st.SecretID
		}
		if st.Selector != nil || st.Coords != nil {
			f.sel(st.Selector)
			if st.Coords != nil {
				f.coords(st.Coords)
			}
			f.add("→")
		} else if canon == "type_secret" {
			f.bad = true
		}
		f.add(q(val))
		if st.ClearFirst {
			f.add("clear")
		}
		if st.DismissKeyboard {
			f.add("dismiss")
		}
	case "press_key":
		f.add(bareOrQuoted(st.Key))
	case "navigate":
		f.add(bareOrQuoted(st.Target))
	case "dismiss_keyboard", "paste_text", "reset_location", "record_start", "record_stop", "audit",
		"metrics_start", "metrics_stop", "request_user_input", "assert_screen_changed":
	case "observe":
		if len(st.Include) > 0 {
			var inc []string
			if json.Unmarshal(st.Include, &inc) != nil {
				return nil, false
			}
			for _, w := range inc {
				if !bareRe.MatchString(w) {
					return nil, false
				}
				f.add(w)
			}
		}
	case "screenshot":
		if st.FilePath != "" {
			f.add(q(st.FilePath))
			f.use("file_path")
		}
	case "wait":
		if st.DurationMs <= 0 {
			return nil, false
		}
		f.add(strconv.Itoa(st.DurationMs))
		f.use("duration")
	case "checkpoint":
		f.add(q(st.Name))
	case "swipe":
		if st.Direction != "" {
			f.add(st.Direction)
			f.use("direction")
		}
		if st.From != nil {
			return nil, false
		}
		if st.FromCoords != nil {
			s, ok := fmtCoords(st.FromCoords)
			f.add("from:" + s)
			f.bad = f.bad || !ok
		}
		if st.ToCoords != nil {
			s, ok := fmtCoords(st.ToCoords)
			f.add("to:" + s)
			f.bad = f.bad || !ok
		}
	case "scroll":
		if st.Direction != "" {
			f.add(st.Direction)
			f.use("direction")
		}
		if st.ToElement != nil {
			f.add("to")
			f.sel(st.ToElement.Predicate)
			if st.ToElement.FullyVisible {
				f.add("fully_visible")
			}
		}
		if st.Selector != nil {
			f.add("in")
			f.sel(st.Selector)
		}
	case "drag":
		switch {
		case st.From != nil && st.From.Predicate != nil && !st.From.FullyVisible:
			f.sel(st.From.Predicate)
		case st.FromCoords != nil:
			f.coords(st.FromCoords)
		default:
			return nil, false
		}
		f.add("to")
		switch {
		case st.ToElement != nil && st.ToElement.Predicate != nil && !st.ToElement.FullyVisible:
			f.sel(st.ToElement.Predicate)
		case st.ToCoords != nil:
			f.coords(st.ToCoords)
		default:
			return nil, false
		}
	case "set_location":
		if st.Lat == nil || st.Lon == nil {
			return nil, false
		}
		f.add(fnum(*st.Lat) + "," + fnum(*st.Lon))
	case "set_permissions":
		f.add(q(st.BundleID))
		keys := make([]string, 0, len(st.Permissions))
		for k := range st.Permissions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !bareRe.MatchString(k) || !bareRe.MatchString(st.Permissions[k]) {
				return nil, false
			}
			f.add(k + "=" + st.Permissions[k])
		}
	case "biometrics":
		f.add(st.State)
		f.use("state")
		if st.Sensor != "" {
			f.add(st.Sensor)
			f.use("sensor")
		}
	case "fold":
		if st.Angle != nil {
			f.add(fnum(*st.Angle))
		} else {
			f.add(st.State)
		}
	case "siri":
		f.add(q(st.Prompt))
	case "eval_script":
		f.add(q(st.Script))
	case "run_script":
		if st.ScriptPath == "" {
			return nil, false
		}
		f.add(q(st.ScriptPath))
	case "extract":
		if !bareRe.MatchString(st.ExtractKey) {
			return nil, false
		}
		f.add(st.ExtractKey)
		switch st.ExtractKind {
		case "screenshot":
			f.add("screenshot")
		case "literal":
			f.add("=", q(st.Text))
		case "text", "":
			f.add("from")
			f.sel(st.Selector)
			if st.ExtractRegex != "" {
				f.add("regex:" + q(st.ExtractRegex))
			}
		default:
			return nil, false
		}
	case "if_exists":
		f.sel(st.Selector)
	case "repeat":
		switch {
		case st.Times != "":
			f.add(string(st.Times), "times")
		case st.While != nil:
			f.add("while")
			f.sel(st.While)
		case st.Until != nil:
			f.add("until")
			f.sel(st.Until)
		case st.Condition != "":
			f.add("while", "js:"+q(st.Condition))
		default:
			return nil, false
		}
	default:
		return nil, false
	}
	f.generic(st)
	if f.bad {
		return nil, false
	}
	head := indent + kw
	if len(f.parts) > 0 {
		head += " " + strings.Join(f.parts, " ")
	}
	if canon != "if_exists" && canon != "repeat" {
		return []string{head}, true
	}
	lines := []string{head + " {"}
	inner := func(steps []dsl.Step) bool {
		for _, s := range steps {
			l, ok := fmtStep(s, indent+"  ")
			if !ok {
				return false
			}
			lines = append(lines, l...)
		}
		return true
	}
	if canon == "repeat" {
		if !inner(st.Body) {
			return nil, false
		}
		return append(lines, indent+"}"), true
	}
	if !inner(st.Then) {
		return nil, false
	}
	if len(st.Else) > 0 {
		lines = append(lines, indent+"} else {")
		if !inner(st.Else) {
			return nil, false
		}
	}
	return append(lines, indent+"}"), true
}
