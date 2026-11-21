package debugger

import (
	"context"
	"os"
	"os/exec"
	"phoneforge/internal/device"
	"strconv"
	"testing"
	"time"
)

// TestRealLLDBPlumbing drives the real lldb (no process needed): prompt/echo
// handling, the sentinel protocol, breakpoint set/list/delete and expression
// parsing. Opt-in: PF_LIVE_LLDB=<path to a binary built with -g from a file t.c>.
func TestRealLLDBPlumbing(t *testing.T) {
	bin := os.Getenv("PF_LIVE_LLDB")
	if bin == "" {
		t.Skip("set PF_LIVE_LLDB to a -g built binary to run against the real lldb")
	}
	p, err := startLLDB(exec.Command("lldb", "--no-lldbinit"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	ctx := context.Background()
	run := func(c string) []string {
		t.Helper()
		out, err := p.exec(ctx, c, 20*time.Second)
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		return out
	}
	for _, c := range []string{`settings set prompt "` + promptTag + `"`, "settings set auto-confirm true", "settings set interpreter.echo-commands false"} {
		run(c)
	}
	run("file " + bin)
	bp, ok := ParseBreakpointSet(run("breakpoint set -f t.c -l 5"))
	if !ok || !bp.Resolved {
		t.Fatalf("set: %+v", bp)
	}
	if list := ParseBreakpointList(run("breakpoint list")); len(list) != 1 || list[0].Locations != 1 {
		t.Fatalf("list: %+v", list)
	}
	if v, msg := ParseEval(run("expression -- 1+2")); msg != "" || v.Value != "3" {
		t.Fatalf("eval: %+v %q", v, msg)
	}
	if _, msg := ParseEval(run("expression -- nosuchvar")); msg == "" {
		t.Fatal("expected eval error")
	}
	out := run("breakpoint delete " + "1")
	if len(out) == 0 {
		t.Fatal("empty delete reply")
	}
	if st, _, _, _ := ParseState(run("process status")); st != "none" {
		t.Fatalf("state without a process: %s", st)
	}
	for _, l := range run("script print(1)") {
		if isSentinel(l) {
			t.Fatalf("sentinel leaked: %q", l)
		}
	}
}

// TestRealLLDBAttachHang: opt-in (PF_LIVE_ATTACH_PID=<pid of a local process>).
// On a Mac with Developer mode disabled, lldb blocks on the authorization
// prompt; the driver must time out, interrupt/kill lldb and report a hint.
func TestRealLLDBAttachHang(t *testing.T) {
	pid := os.Getenv("PF_LIVE_ATTACH_PID")
	if pid == "" {
		t.Skip("set PF_LIVE_ATTACH_PID")
	}
	n, _ := strconv.Atoi(pid)
	defer func(a time.Duration) { attachTimeout = a }(attachTimeout)
	attachTimeout = 8 * time.Second
	m := NewManager()
	start := time.Now()
	s, err := m.Create(context.Background(), Target{Platform: device.PlatformSimulator, Serial: "X", PID: n})
	t.Logf("elapsed %s err=%v", time.Since(start).Round(time.Millisecond), err)
	if err == nil {
		t.Logf("attached: %+v", s.Info())
		m.CloseAll()
	}
}
