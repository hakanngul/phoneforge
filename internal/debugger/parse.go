package debugger

import (
	"regexp"
	"strconv"
	"strings"
)

// Frame is one stack frame.
type Frame struct {
	Index    int    `json:"index"`
	Address  string `json:"address,omitempty"`
	Module   string `json:"module,omitempty"`
	Function string `json:"function,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Selected bool   `json:"selected,omitempty"`
	Raw      string `json:"raw"`
}

// Variable is one `frame variable` / `expression` result.
type Variable struct {
	Type  string `json:"type,omitempty"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Breakpoint is a breakpoint as reported by lldb.
type Breakpoint struct {
	ID        int    `json:"id"`
	Spec      string `json:"spec,omitempty"`
	Locations int    `json:"locations"`
	Resolved  bool   `json:"resolved"`
	Hits      int    `json:"hits"`
	Raw       string `json:"raw,omitempty"`
}

var (
	frameRe    = regexp.MustCompile(`^\s*(\*\s+)?frame #(\d+): (0x[0-9a-fA-F]+)(?:\s+(.*))?$`)
	frameLocRe = regexp.MustCompile(`^(.*?)(?: at (.+?):(\d+)(?::(\d+))?)?$`)
	bpSetRe    = regexp.MustCompile(`^Breakpoint (\d+): (.*)$`)
	bpListRe   = regexp.MustCompile(`^(\d+): (.*?), locations = (\d+)(?:, hit count = (\d+))?`)
	bpLocRe    = regexp.MustCompile(`^\s+\d+\.\d+: .*?hit count = (\d+)`)
	varRe      = regexp.MustCompile(`^\((.*?)\) (\S+) = (.*)$`)
	stopReason = regexp.MustCompile(`stop reason = (.*)$`)
	procLineRe = regexp.MustCompile(`^Process (\d+) (stopped|exited|detached|resuming|running)`)
	pidRe      = regexp.MustCompile(`Process (\d+)`)
	threadRe   = regexp.MustCompile(`^\s*(\*\s+)?thread #(\d+)`)
)

// ParseFrames extracts stack frames from `thread backtrace` output.
func ParseFrames(lines []string) []Frame {
	var out []Frame
	for _, l := range lines {
		if f, ok := ParseFrame(l); ok {
			out = append(out, f)
		}
	}
	return out
}

// ParseFrame parses one "frame #N: 0x... module`func at file:line:col" line.
func ParseFrame(line string) (Frame, bool) {
	m := frameRe.FindStringSubmatch(line)
	if m == nil {
		return Frame{}, false
	}
	idx, _ := strconv.Atoi(m[2])
	f := Frame{Index: idx, Address: m[3], Selected: m[1] != "", Raw: strings.TrimSpace(line)}
	rest := strings.TrimSpace(m[4])
	if rest == "" {
		return f, true
	}
	if lm := frameLocRe.FindStringSubmatch(rest); lm != nil {
		rest = lm[1]
		f.File = lm[2]
		f.Line, _ = strconv.Atoi(lm[3])
		f.Column, _ = strconv.Atoi(lm[4])
	}
	if mod, fn, ok := strings.Cut(rest, "`"); ok {
		f.Module, f.Function = mod, strings.TrimSpace(fn)
	} else {
		f.Function = rest
	}
	return f, true
}

// ParseBreakpointSet parses the reply to `breakpoint set`.
func ParseBreakpointSet(lines []string) (Breakpoint, bool) {
	for _, l := range lines {
		if m := bpSetRe.FindStringSubmatch(l); m != nil {
			id, _ := strconv.Atoi(m[1])
			b := Breakpoint{ID: id, Raw: l, Spec: m[2]}
			if strings.Contains(m[2], "no locations") {
				b.Resolved = false
			} else {
				b.Resolved = true
				b.Locations = 1
				if i := strings.Index(m[2], "locations = "); i >= 0 {
					n, _ := strconv.Atoi(strings.Fields(m[2][i+len("locations = "):])[0])
					b.Locations = n
				}
			}
			return b, true
		}
	}
	return Breakpoint{}, false
}

// ParseBreakpointList parses `breakpoint list` output.
func ParseBreakpointList(lines []string) []Breakpoint {
	var out []Breakpoint
	topHits := false // newer lldb prints the total on the breakpoint line
	for _, l := range lines {
		if m := bpListRe.FindStringSubmatch(l); m != nil {
			id, _ := strconv.Atoi(m[1])
			n, _ := strconv.Atoi(m[3])
			hits, _ := strconv.Atoi(m[4])
			topHits = m[4] != ""
			out = append(out, Breakpoint{ID: id, Spec: m[2], Locations: n, Hits: hits, Resolved: n > 0, Raw: l})
			continue
		}
		if len(out) == 0 || topHits {
			continue
		}
		if m := bpLocRe.FindStringSubmatch(l); m != nil {
			h, _ := strconv.Atoi(m[1])
			out[len(out)-1].Hits += h
		}
	}
	return out
}

// ParseVariables groups `frame variable` output; indented continuation lines
// (struct members) are folded into the preceding variable's value.
func ParseVariables(lines []string) []Variable {
	var out []Variable
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if m := varRe.FindStringSubmatch(l); m != nil && !strings.HasPrefix(l, " ") {
			out = append(out, Variable{Type: m[1], Name: m[2], Value: m[3]})
			continue
		}
		if len(out) > 0 && (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "}")) {
			out[len(out)-1].Value += "\n" + l
		}
	}
	return out
}

// ParseEval parses `expression` output. An "error:" line yields an error string.
func ParseEval(lines []string) (Variable, string) {
	var errs []string
	for i, l := range lines {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "error:") {
			errs = append(errs, strings.TrimSpace(strings.TrimPrefix(t, "error:")))
			continue
		}
		if m := varRe.FindStringSubmatch(l); m != nil {
			v := Variable{Type: m[1], Name: m[2], Value: m[3]}
			for _, c := range lines[i+1:] {
				if strings.HasPrefix(c, " ") || strings.HasPrefix(c, "}") {
					v.Value += "\n" + c
				} else {
					break
				}
			}
			return v, ""
		}
	}
	if len(errs) > 0 {
		return Variable{}, strings.Join(errs, "; ")
	}
	// Void expressions and some formats print nothing parseable.
	return Variable{Name: "", Value: strings.TrimSpace(strings.Join(lines, "\n"))}, ""
}

// ParseState interprets `process status` output (or any stop text).
// state is one of stopped, running, exited, detached, none.
func ParseState(lines []string) (state string, pid int, reason string, frame *Frame) {
	state = "none"
	for _, l := range lines {
		low := strings.ToLower(l)
		if m := pidRe.FindStringSubmatch(l); m != nil && pid == 0 {
			pid, _ = strconv.Atoi(m[1])
		}
		switch {
		case procLineRe.MatchString(l):
			switch procLineRe.FindStringSubmatch(l)[2] {
			case "stopped":
				state = "stopped"
			case "exited":
				state = "exited"
			case "detached":
				state = "detached"
			default:
				state = "running"
			}
		case strings.Contains(low, "is running"):
			state = "running"
		case strings.Contains(low, "invalid target"), strings.Contains(low, "requires a current process"), strings.Contains(low, "no process"):
			if state == "none" {
				state = "none"
			}
		}
		if m := stopReason.FindStringSubmatch(l); m != nil && reason == "" {
			reason = strings.TrimSpace(m[1])
		}
		if f, ok := ParseFrame(l); ok && frame == nil && f.Index == 0 {
			ff := f
			frame = &ff
		}
	}
	return
}

// ThreadCount counts thread lines in `thread list` output.
func ThreadCount(lines []string) int {
	n := 0
	for _, l := range lines {
		if threadRe.MatchString(l) {
			n++
		}
	}
	return n
}
