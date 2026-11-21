package debugger

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("PF_FAKE_LLDB") == "1" {
		fakeLLDB()
		return
	}
	os.Exit(m.Run())
}

func fakeCommand() *exec.Cmd {
	c := exec.Command(os.Args[0])
	c.Env = append(os.Environ(), "PF_FAKE_LLDB=1")
	return c
}

const stopText = "Process 42 stopped\n* thread #1, queue = 'com.apple.main-thread', stop reason = breakpoint 1.1\n    frame #0: 0x00000001000004b0 t`work(n=3) at t.c:5:12"

// fakeLLDB imitates the parts of lldb's non-tty behaviour the driver relies
// on: prompt+command echo, immediate async resume, SIGINT handling.
func fakeLLDB() {
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGINT)
	out := bufio.NewWriter(os.Stdout)
	say := func(s string) { fmt.Fprintln(out, s); out.Flush() }
	prompt := "(lldb) "
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := sc.Text()
		say(prompt + line)
		switch {
		case strings.HasPrefix(line, "settings set prompt"):
			prompt = "PFP> "
		case strings.HasPrefix(line, `script print("PFEN"+"D-`):
			n := strings.TrimSuffix(strings.TrimPrefix(line, `script print("PFEN"+"D-`), `")`)
			say("PFEND-" + n)
		case line == "process attach --pid 999": // never answers, ignores SIGINT
			for {
				<-sig
			}
		case line == "process attach --pid 998": // answers only after SIGINT
			<-sig
			say("error: attach interrupted")
		case strings.HasSuffix(line, "process attach --pid 997"):
			say("error: attach failed: not debuggable (get-task-allow)")
		case strings.HasPrefix(line, "process attach --pid"):
			say("Process 42 stopped\n* thread #1, queue = 'com.apple.main-thread', stop reason = signal SIGSTOP\n    frame #0: 0x0000000180000000 libsystem_kernel.dylib`__semwait_signal + 8")
		case strings.HasPrefix(line, "device select"), strings.HasPrefix(line, "settings"):
		case strings.HasPrefix(line, "device process attach"):
			say("Process 42 stopped\n* thread #1, stop reason = signal SIGSTOP\n    frame #0: 0x0000000180000000 libsystem_kernel.dylib`mach_msg2_trap + 8")
		case strings.HasPrefix(line, "breakpoint set"):
			say("Breakpoint 1: where = t`work + 8 at t.c:5:12, address = 0x00000001000004b0")
		case line == "breakpoint list":
			say("Current breakpoints:\n1: file = 't.c', line = 5, exact_match = 0, locations = 1, resolved = 1, hit count = 2\n  1.1: where = t`work + 8 at t.c:5:12, address = t[0x00000001000004b0], resolved, hit count = 2\n")
		case line == "breakpoint delete 1":
			say("1 breakpoints deleted; 0 breakpoint locations disabled.")
		case strings.HasPrefix(line, "breakpoint delete"):
			say("error: No breakpoints exist to be deleted.")
		case line == "process continue":
			say("Process 42 resuming")
			go func() {
				time.Sleep(150 * time.Millisecond)
				say(stopText)
			}()
		case line == "thread step-over", line == "thread step-in":
			say("Process 42 stopped\n* thread #1, queue = 'com.apple.main-thread', stop reason = step over\n    frame #0: 0x00000001000004bc t`work(n=3) at t.c:6:5")
		case line == "process interrupt":
			say("Process 42 stopped\n* thread #1, stop reason = signal SIGSTOP\n    frame #0: 0x0000000180000000 libsystem_kernel.dylib`usleep + 8")
		case line == "process status":
			say(stopText)
		case line == "frame info", strings.HasPrefix(line, "frame select"):
			say("frame #0: 0x00000001000004b0 t`work(n=3) at t.c:5:12")
		case strings.HasPrefix(line, "thread backtrace"):
			say("* thread #1, queue = 'com.apple.main-thread', stop reason = breakpoint 1.1\n  * frame #0: 0x00000001000004b0 t`work(n=3) at t.c:5:12\n    frame #1: 0x0000000100000510 t`main at t.c:11:5\n    frame #2: 0x0000000180000010 dyld`start + 6000")
		case line == "frame variable":
			say("(int) n = 3\n(int) sq = 9\n(Point) p = {\n  x = 1\n  y = 2\n}")
		case line == "expression -- 1+2":
			say("(int) $0 = 3")
		case strings.HasPrefix(line, "expression --"):
			say("error: use of undeclared identifier 'bad'")
		case line == "process detach":
			say("Process 42 detached")
		case line == "quit":
			return
		}
	}
}
