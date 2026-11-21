package debugger

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	promptTag = "PFP> "
	maxLines  = 20000
)

// interruptGap is how long lldb gets to answer after SIGINT before it is killed.
var interruptGap = 3 * time.Second

// ErrTimeout means lldb did not answer in time.
var ErrTimeout = errors.New("debugger: lldb did not respond in time")

// ErrDead means the lldb process is gone.
var ErrDead = errors.New("debugger: lldb process has exited")

// lldbProc drives one interactive lldb subprocess over pipes. Command
// completion is detected with a sentinel line printed by lldb's embedded
// Python (`script print(...)`) after every command, because lldb does not
// flush its prompt when stdin is not a terminal.
type lldbProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	cmdMu sync.Mutex // serialises commands
	seq   int

	mu      sync.Mutex
	lines   []string
	base    int // absolute index of lines[0]
	changed chan struct{}
	state   string // stopped | running | exited | detached | none
	dead    bool
	done    chan struct{}
}

func startLLDB(c *exec.Cmd) (*lldbProc, error) {
	stdin, err := c.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c.Stdout, c.Stderr = pw, pw
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	pw.Close()
	p := &lldbProc{cmd: c, stdin: stdin, changed: make(chan struct{}), state: "none", done: make(chan struct{})}
	go p.read(pr)
	go func() {
		_ = c.Wait()
		p.mu.Lock()
		p.dead = true
		p.broadcast()
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

func (p *lldbProc) broadcast() {
	close(p.changed)
	p.changed = make(chan struct{})
}

func (p *lldbProc) read(r io.ReadCloser) {
	defer r.Close()
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			p.addLine(strings.TrimRight(line, "\r\n"))
		}
		if err != nil {
			return
		}
	}
}

func (p *lldbProc) addLine(l string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(l) > 1<<20 {
		l = l[:1<<20]
	}
	p.lines = append(p.lines, l)
	if len(p.lines) > maxLines {
		drop := len(p.lines) - maxLines/2
		p.lines = append([]string(nil), p.lines[drop:]...)
		p.base += drop
	}
	if m := procLineRe.FindStringSubmatch(strings.TrimPrefix(l, promptTag)); m != nil {
		switch m[2] {
		case "stopped":
			p.state = "stopped"
		case "exited":
			p.state = "exited"
		case "detached":
			p.state = "detached"
		default:
			p.state = "running"
		}
	}
	p.broadcast()
}

func (p *lldbProc) end() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.base + len(p.lines)
}

func (p *lldbProc) setState(s string) {
	p.mu.Lock()
	p.state = s
	p.broadcast()
	p.mu.Unlock()
}

func (p *lldbProc) getState() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

func (p *lldbProc) isDead() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead
}

// slice returns raw lines in [from, to) clipped to the buffer.
func (p *lldbProc) slice(from, to int) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	lo, hi := from-p.base, to-p.base
	if lo < 0 {
		lo = 0
	}
	if hi > len(p.lines) {
		hi = len(p.lines)
	}
	if lo >= hi {
		return nil
	}
	return append([]string(nil), p.lines[lo:hi]...)
}

// waitFor blocks until cond (called under the lock) holds, lldb dies, ctx is
// done or the timeout passes.
func (p *lldbProc) waitFor(ctx context.Context, timeout time.Duration, cond func() bool) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		p.mu.Lock()
		ok := cond()
		ch := p.changed
		dead := p.dead
		p.mu.Unlock()
		if ok {
			return true
		}
		if dead {
			return false
		}
		select {
		case <-ch:
		case <-timer.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

func (p *lldbProc) tokenIndex(tok string, from int) int {
	// caller holds p.mu
	start := from - p.base
	if start < 0 {
		start = 0
	}
	for i := start; i < len(p.lines); i++ {
		if p.lines[i] == tok || p.lines[i] == promptTag+tok {
			return p.base + i
		}
	}
	return -1
}

func checkLine(s string) error {
	if strings.ContainsAny(s, "\r\n\x00") {
		return errors.New("debugger: command must be a single line")
	}
	return nil
}

// exec sends one lldb command and returns its cleaned output. On timeout it
// interrupts lldb; if lldb still does not answer it is killed.
func (p *lldbProc) exec(ctx context.Context, line string, timeout time.Duration) ([]string, error) {
	if err := checkLine(line); err != nil {
		return nil, err
	}
	p.cmdMu.Lock()
	defer p.cmdMu.Unlock()
	if p.isDead() {
		return nil, ErrDead
	}
	p.seq++
	tok := fmt.Sprintf("PFEND-%d", p.seq)
	mark := p.end()
	payload := line + "\n" + fmt.Sprintf("script print(\"PFEN\"+\"D-%d\")\n", p.seq)
	if _, err := io.WriteString(p.stdin, payload); err != nil {
		return nil, ErrDead
	}
	idx := -1
	got := p.waitFor(ctx, timeout, func() bool { idx = p.tokenIndex(tok, mark); return idx >= 0 })
	if !got {
		out := clean(p.slice(mark, p.end()), line)
		if p.isDead() {
			return out, ErrDead
		}
		if ctx.Err() == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			// Try to interrupt whatever lldb is blocked on, then give it a moment.
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGINT)
			if p.waitFor(context.Background(), interruptGap, func() bool { idx = p.tokenIndex(tok, mark); return idx >= 0 }) {
				return clean(p.slice(mark, idx), line), fmt.Errorf("%w: %q was interrupted after %s", ErrTimeout, line, timeout)
			}
		}
		p.kill()
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return out, ctx.Err()
		}
		return out, fmt.Errorf("%w: %q did not finish in %s and lldb was stopped", ErrTimeout, line, timeout)
	}
	return clean(p.slice(mark, idx), line), nil
}

// clean strips prompt tags and echoes of the command and sentinel.
func clean(lines []string, cmd string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		t := strings.TrimPrefix(l, promptTag)
		t = strings.TrimPrefix(t, "(lldb) ")
		if t == cmd || strings.HasPrefix(t, `script print("PFEN"`) || isSentinel(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func isSentinel(s string) bool {
	if !strings.HasPrefix(s, "PFEND-") {
		return false
	}
	for _, r := range s[len("PFEND-"):] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (p *lldbProc) kill() {
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		_ = p.cmd.Process.Kill()
	}
}

// close asks lldb to quit, then kills it.
func (p *lldbProc) close() {
	if !p.isDead() {
		_, _ = io.WriteString(p.stdin, "quit\n")
		_ = p.stdin.Close()
		select {
		case <-p.done:
			return
		case <-time.After(2 * time.Second):
		}
		p.kill()
		<-p.done
	}
}
