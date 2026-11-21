// Package debugger runs lldb sessions against iOS apps (simulator processes
// and, through CoreDevice, real devices): attach or launch-and-attach, set
// breakpoints, step, inspect and evaluate.
package debugger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNoSession means the session id is unknown.
var ErrNoSession = errors.New("debugger: no such session")

const (
	maxSessions    = 8
	defaultStepMs  = 8000
	stopSettleTime = 250 * time.Millisecond
)

// Timeouts (variables so tests can shorten them).
var (
	attachTimeout = 60 * time.Second
	cmdTimeout    = 30 * time.Second
)

// Manager owns debug sessions.
type Manager struct {
	// NewCommand builds the lldb process; nil uses `lldb --no-lldbinit`.
	NewCommand func() *exec.Cmd
	// Run executes xcrun/simctl/devicectl; nil uses ExecRunner.
	Run Runner

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewManager returns a Manager using the system lldb.
func NewManager() *Manager { return &Manager{sessions: map[string]*Session{}} }

func (m *Manager) newCommand() *exec.Cmd {
	if m.NewCommand != nil {
		return m.NewCommand()
	}
	return exec.Command("lldb", "--no-lldbinit")
}

func (m *Manager) runner() Runner {
	if m.Run != nil {
		return m.Run
	}
	return ExecRunner
}

// Session is one lldb session.
type Session struct {
	ID       string    `json:"id"`
	Target   Target    `json:"target"`
	PID      int       `json:"pid,omitempty"`
	Created  time.Time `json:"created"`
	Note     string    `json:"note,omitempty"`
	proc     *lldbProc
	mgr      *Manager
	closeOne sync.Once
}

// Info is the JSON view of a session.
type Info struct {
	ID      string    `json:"id"`
	Target  Target    `json:"target"`
	PID     int       `json:"pid,omitempty"`
	State   string    `json:"state"`
	Created time.Time `json:"created"`
	Note    string    `json:"note,omitempty"`
}

func (s *Session) Info() Info {
	st := s.proc.getState()
	if s.proc.isDead() {
		st = "dead"
	}
	return Info{ID: s.ID, Target: s.Target, PID: s.PID, State: st, Created: s.Created, Note: s.Note}
}

// Create starts lldb and attaches to (or launches) the target.
func (m *Manager) Create(ctx context.Context, t Target) (*Session, error) {
	m.mu.Lock()
	if m.sessions == nil {
		m.sessions = map[string]*Session{}
	}
	if len(m.sessions) >= maxSessions {
		m.mu.Unlock()
		return nil, fmt.Errorf("debugger: too many sessions (max %d); detach one first", maxSessions)
	}
	m.mu.Unlock()

	pctx, cancel := context.WithTimeout(ctx, attachTimeout)
	defer cancel()
	pl, err := planFor(pctx, m.runner(), t)
	if err != nil {
		return nil, err
	}
	proc, err := startLLDB(m.newCommand())
	if err != nil {
		return nil, fmt.Errorf("debugger: cannot start lldb (is Xcode installed?): %w", err)
	}
	for _, c := range []string{
		`settings set prompt "` + promptTag + `"`,
		"settings set auto-confirm true",
		"settings set interpreter.prompt-on-quit false",
		"settings set interpreter.echo-commands false",
	} {
		if _, err := proc.exec(pctx, c, 10*time.Second); err != nil {
			proc.close()
			return nil, fmt.Errorf("debugger: lldb setup: %w", err)
		}
	}
	var out []string
	for _, c := range pl.commands {
		out, err = proc.exec(pctx, c, attachTimeout)
		if err == nil {
			if bad := errorLine(out); bad != "" {
				err = errors.New(bad)
			}
		}
		if err != nil {
			proc.close()
			return nil, attachError(t, c, err)
		}
	}
	id := newID()
	s := &Session{ID: id, Target: t, PID: pl.pid, Created: time.Now(), Note: pl.note, proc: proc, mgr: m}
	if state, pid, _, _ := ParseState(out); state != "none" {
		proc.setState(state)
		if s.PID == 0 {
			s.PID = pid
		}
	} else {
		proc.setState("stopped") // attach leaves the process stopped
	}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	return s, nil
}

func attachError(t Target, cmd string, err error) error {
	hint := ""
	low := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, ErrTimeout) && t.Platform == "simulator":
		hint = " (if macOS shows a Developer Tools authorization prompt, approve it; `DevToolsSecurity -status` must report Developer mode enabled)"
	case strings.Contains(low, "get-task-allow") || strings.Contains(low, "not debuggable") || strings.Contains(low, "unable to attach"):
		hint = " (real devices only allow attaching to development-signed apps with get-task-allow)"
	}
	return fmt.Errorf("debugger: %q failed: %w%s", cmd, err, hint)
}

// errorLine returns the first "error:" line of lldb output.
func errorLine(lines []string) string {
	for _, l := range lines {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "error:") {
			return t
		}
	}
	return ""
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "dbg-" + hex.EncodeToString(b)
}

// Get returns a session by id.
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, ErrNoSession
	}
	return s, nil
}

// List returns all sessions, oldest first.
func (m *Manager) List() []Info {
	m.mu.Lock()
	ss := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		ss = append(ss, s)
	}
	m.mu.Unlock()
	sort.Slice(ss, func(i, j int) bool { return ss[i].Created.Before(ss[j].Created) })
	out := make([]Info, len(ss))
	for i, s := range ss {
		out[i] = s.Info()
	}
	return out
}

// Detach detaches from the process and ends the session.
func (m *Manager) Detach(ctx context.Context, id string) error {
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	s.close(ctx)
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	return nil
}

// CloseAll ends every session (server shutdown).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	ss := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		ss = append(ss, s)
	}
	m.sessions = map[string]*Session{}
	m.mu.Unlock()
	for _, s := range ss {
		s.close(context.Background())
	}
}

func (s *Session) close(ctx context.Context) {
	s.closeOne.Do(func() {
		if !s.proc.isDead() {
			dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			// Leave the app running: detach, not kill.
			_, _ = s.proc.exec(dctx, "process detach", 8*time.Second)
			cancel()
		}
		s.proc.close()
	})
}

// run executes a command and turns lldb "error:" replies into errors.
func (s *Session) run(ctx context.Context, cmd string) ([]string, error) {
	out, err := s.proc.exec(ctx, cmd, cmdTimeout)
	if err != nil {
		return out, err
	}
	if bad := errorLine(out); bad != "" {
		return out, errors.New(bad)
	}
	return out, nil
}

func (s *Session) stoppedOnly(ctx context.Context, cmd string) ([]string, error) {
	if st := s.proc.getState(); st == "running" {
		return nil, errors.New("debugger: process is running; interrupt it first (POST .../interrupt)")
	} else if st == "exited" || st == "detached" || st == "none" {
		return nil, fmt.Errorf("debugger: process is %s", st)
	}
	return s.run(ctx, cmd)
}

// BreakpointSpec selects where to break.
type BreakpointSpec struct {
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
	Symbol    string `json:"symbol,omitempty"`
	Condition string `json:"condition,omitempty"`
}

func (b BreakpointSpec) command() (string, error) {
	for _, v := range []string{b.File, b.Symbol, b.Condition} {
		if err := checkLine(v); err != nil {
			return "", err
		}
	}
	var c string
	switch {
	case b.File != "" && b.Line > 0:
		c = "breakpoint set -f " + quoteArg(b.File) + " -l " + strconv.Itoa(b.Line)
	case b.Symbol != "":
		c = "breakpoint set -n " + quoteArg(b.Symbol)
	default:
		return "", errors.New("debugger: breakpoint needs file+line or symbol")
	}
	if b.Condition != "" {
		c += " -c " + quoteArg(b.Condition)
	}
	return c, nil
}

// SetBreakpoint adds a breakpoint.
func (s *Session) SetBreakpoint(ctx context.Context, spec BreakpointSpec) (Breakpoint, error) {
	c, err := spec.command()
	if err != nil {
		return Breakpoint{}, err
	}
	out, err := s.run(ctx, c)
	if err != nil {
		return Breakpoint{}, err
	}
	b, ok := ParseBreakpointSet(out)
	if !ok {
		return Breakpoint{}, fmt.Errorf("debugger: unexpected lldb reply: %s", strings.Join(out, "\n"))
	}
	return b, nil
}

// RemoveBreakpoint deletes a breakpoint by id.
func (s *Session) RemoveBreakpoint(ctx context.Context, id int) error {
	if id <= 0 {
		return errors.New("debugger: invalid breakpoint id")
	}
	out, err := s.run(ctx, "breakpoint delete "+strconv.Itoa(id))
	if err != nil {
		return err
	}
	if !strings.Contains(strings.Join(out, "\n"), "1 breakpoints deleted") {
		return fmt.Errorf("debugger: no breakpoint %d", id)
	}
	return nil
}

// Breakpoints lists breakpoints.
func (s *Session) Breakpoints(ctx context.Context) ([]Breakpoint, error) {
	out, err := s.run(ctx, "breakpoint list")
	if err != nil {
		return nil, err
	}
	bps := ParseBreakpointList(out)
	if bps == nil {
		bps = []Breakpoint{}
	}
	return bps, nil
}

// StopInfo is the result of a resume/step command.
type StopInfo struct {
	State      string   `json:"state"`
	PID        int      `json:"pid,omitempty"`
	StopReason string   `json:"stopReason,omitempty"`
	Frame      *Frame   `json:"frame,omitempty"`
	Output     []string `json:"output,omitempty"`
}

// Resume kinds.
const (
	Continue = "continue"
	StepOver = "step-over"
	StepInto = "step-in"
	StepOut  = "step-out"
)

// Resume continues or steps, then waits up to waitMs for the next stop.
// With waitMs == 0 steps wait the default; continue returns immediately.
func (s *Session) Resume(ctx context.Context, kind string, waitMs int) (StopInfo, error) {
	var cmd string
	switch kind {
	case Continue:
		cmd = "process continue"
	case StepOver:
		cmd = "thread step-over"
	case StepInto:
		cmd = "thread step-in"
	case StepOut:
		cmd = "thread step-out"
	default:
		return StopInfo{}, errors.New("debugger: unknown resume kind")
	}
	if st := s.proc.getState(); st != "stopped" {
		return StopInfo{}, fmt.Errorf("debugger: process is %s, expected stopped", st)
	}
	mark := s.proc.end()
	s.proc.setState("running")
	out, err := s.proc.exec(ctx, cmd, cmdTimeout)
	if err == nil {
		if bad := errorLine(out); bad != "" {
			err = errors.New(bad)
			s.proc.setState("stopped")
		}
	}
	if err != nil {
		return StopInfo{}, err
	}
	if waitMs <= 0 && kind != Continue {
		waitMs = defaultStepMs
	}
	if waitMs > 0 {
		s.proc.waitFor(ctx, time.Duration(waitMs)*time.Millisecond, func() bool { return s.proc.state != "running" })
		time.Sleep(stopSettleTime) // let the frame lines after "Process N stopped" arrive
	}
	text := clean(s.proc.slice(mark, s.proc.end()), cmd)
	state, pid, reason, frame := ParseState(text)
	if cur := s.proc.getState(); cur != "running" || state == "none" {
		state = cur
	}
	return StopInfo{State: state, PID: pid, StopReason: reason, Frame: frame, Output: text}, nil
}

// Interrupt stops a running process.
func (s *Session) Interrupt(ctx context.Context, waitMs int) (StopInfo, error) {
	if s.proc.getState() != "running" {
		return s.State(ctx)
	}
	mark := s.proc.end()
	if _, err := s.run(ctx, "process interrupt"); err != nil {
		return StopInfo{}, err
	}
	if waitMs <= 0 {
		waitMs = 3000
	}
	s.proc.waitFor(ctx, time.Duration(waitMs)*time.Millisecond, func() bool { return s.proc.state != "running" })
	time.Sleep(stopSettleTime)
	text := clean(s.proc.slice(mark, s.proc.end()), "process interrupt")
	_, pid, reason, frame := ParseState(text)
	return StopInfo{State: s.proc.getState(), PID: pid, StopReason: reason, Frame: frame, Output: text}, nil
}

// State reports the process state and, when stopped, the top frame.
func (s *Session) State(ctx context.Context) (StopInfo, error) {
	if s.proc.isDead() {
		return StopInfo{State: "dead"}, nil
	}
	cur := s.proc.getState()
	if cur == "running" || cur == "exited" || cur == "detached" {
		return StopInfo{State: cur, PID: s.PID}, nil
	}
	out, err := s.proc.exec(ctx, "process status", cmdTimeout)
	if err != nil {
		return StopInfo{}, err
	}
	state, pid, reason, frame := ParseState(out)
	if state == "none" {
		state = cur
	} else {
		s.proc.setState(state)
	}
	if pid == 0 {
		pid = s.PID
	}
	return StopInfo{State: state, PID: pid, StopReason: reason, Frame: frame, Output: out}, nil
}

// Backtrace returns the stack of the selected (or given) thread.
func (s *Session) Backtrace(ctx context.Context, thread int, all bool) ([]Frame, []string, error) {
	cmd := "thread backtrace"
	switch {
	case all:
		cmd += " all"
	case thread > 0:
		cmd += " " + strconv.Itoa(thread)
	}
	out, err := s.stoppedOnly(ctx, cmd)
	if err != nil {
		return nil, nil, err
	}
	return ParseFrames(out), out, nil
}

func (s *Session) selectFrame(ctx context.Context, frame int) error {
	if frame < 0 {
		return nil
	}
	_, err := s.run(ctx, "frame select "+strconv.Itoa(frame))
	return err
}

// Variables returns local variables of a frame (frame < 0 = selected).
func (s *Session) Variables(ctx context.Context, frame int) ([]Variable, error) {
	if _, err := s.stoppedOnly(ctx, "frame info"); err != nil {
		return nil, err
	}
	if err := s.selectFrame(ctx, frame); err != nil {
		return nil, err
	}
	out, err := s.run(ctx, "frame variable")
	if err != nil {
		return nil, err
	}
	vars := ParseVariables(out)
	if vars == nil {
		vars = []Variable{}
	}
	return vars, nil
}

// Eval evaluates an expression in a frame (frame < 0 = selected).
func (s *Session) Eval(ctx context.Context, expr string, frame int) (Variable, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return Variable{}, errors.New("debugger: expression is required")
	}
	if err := checkLine(expr); err != nil {
		return Variable{}, err
	}
	if _, err := s.stoppedOnly(ctx, "frame info"); err != nil {
		return Variable{}, err
	}
	if err := s.selectFrame(ctx, frame); err != nil {
		return Variable{}, err
	}
	out, err := s.proc.exec(ctx, "expression -- "+expr, cmdTimeout)
	if err != nil {
		return Variable{}, err
	}
	v, msg := ParseEval(out)
	if msg != "" {
		return Variable{}, errors.New(msg)
	}
	return v, nil
}
