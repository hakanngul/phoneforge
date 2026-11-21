package debugger

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"phoneforge/internal/device"
)

func TestParseFrame(t *testing.T) {
	f, ok := ParseFrame("  * frame #0: 0x00000001000004b0 t`work(n=3) at t.c:5:12")
	if !ok || f.Index != 0 || !f.Selected || f.Module != "t" || f.Function != "work(n=3)" || f.File != "t.c" || f.Line != 5 || f.Column != 12 {
		t.Fatalf("%+v", f)
	}
	f, ok = ParseFrame("    frame #2: 0x0000000180000010 dyld`start + 6000")
	if !ok || f.Module != "dyld" || f.Function != "start + 6000" || f.File != "" {
		t.Fatalf("%+v", f)
	}
	if _, ok := ParseFrame("* thread #1, queue = 'x'"); ok {
		t.Fatal("thread line is not a frame")
	}
}

func TestParseBreakpoints(t *testing.T) {
	b, ok := ParseBreakpointSet([]string{"Breakpoint 3: where = t`work + 8 at t.c:5:12, address = 0x1"})
	if !ok || b.ID != 3 || !b.Resolved {
		t.Fatalf("%+v", b)
	}
	b, _ = ParseBreakpointSet([]string{"Breakpoint 4: no locations (pending)."})
	if b.ID != 4 || b.Resolved {
		t.Fatalf("%+v", b)
	}
	lst := ParseBreakpointList(strings.Split("Current breakpoints:\n1: file = 't.c', line = 5, exact_match = 0, locations = 1, resolved = 1, hit count = 2\n  1.1: where = t`work + 8, resolved, hit count = 2\n\n2: name = 'foo', locations = 0", "\n"))
	if len(lst) != 2 || lst[0].ID != 1 || lst[0].Hits != 2 || lst[1].Resolved {
		t.Fatalf("%+v", lst)
	}
}

func TestParseVariablesAndEval(t *testing.T) {
	vars := ParseVariables([]string{"(int) n = 3", "(Point) p = {", "  x = 1", "  y = 2", "}"})
	if len(vars) != 2 || vars[0].Name != "n" || vars[0].Value != "3" || !strings.Contains(vars[1].Value, "y = 2") {
		t.Fatalf("%+v", vars)
	}
	v, msg := ParseEval([]string{"(int) $0 = 3"})
	if msg != "" || v.Name != "$0" || v.Value != "3" || v.Type != "int" {
		t.Fatalf("%+v %q", v, msg)
	}
	if _, msg = ParseEval([]string{"error: use of undeclared identifier 'x'"}); !strings.Contains(msg, "undeclared") {
		t.Fatalf("msg %q", msg)
	}
}

func TestParseState(t *testing.T) {
	st, pid, reason, fr := ParseState(strings.Split(stopText, "\n"))
	if st != "stopped" || pid != 42 || reason != "breakpoint 1.1" || fr == nil || fr.Function != "work(n=3)" {
		t.Fatalf("%s %d %q %+v", st, pid, reason, fr)
	}
	if st, _, _, _ = ParseState([]string{"Process 42 exited with status = 0 (0x00000000)"}); st != "exited" {
		t.Fatal(st)
	}
	if st, _, _, _ = ParseState([]string{"error: invalid target, create a target using the 'target create' command"}); st != "none" {
		t.Fatal(st)
	}
}

func TestSimulatorPIDAndDeviceMatch(t *testing.T) {
	out := "PID\tStatus\tLabel\n-\t0\tcom.apple.foo\n555\t0\tUIKitApplication:com.apple.Preferences[abcd][rb-legacy]\n-\t0\tUIKitApplication:com.other[1][x]\n"
	if SimulatorPID(out, "com.apple.Preferences") != 555 || SimulatorPID(out, "com.other") != 0 || SimulatorPID(out, "nope") != 0 {
		t.Fatal("simulator pid")
	}
	procs := []DeviceProcess{{"file:///usr/libexec/x", 1}, {"file:///private/var/containers/Bundle/Application/U/My.app/My", 77}}
	if MatchDevicePID("file:///private/var/containers/Bundle/Application/U/My.app/", procs) != 77 {
		t.Fatal("device pid")
	}
	if MatchDevicePID("file:///private/var/containers/Bundle/Application/U/Other.app/", procs) != 0 {
		t.Fatal("device pid false positive")
	}
}

func TestValidateTarget(t *testing.T) {
	ok := Target{Platform: device.PlatformSimulator, Serial: "AAAA-1", BundleID: "com.apple.Preferences"}
	if err := validateTarget(ok); err != nil {
		t.Fatal(err)
	}
	bad := []Target{
		{Platform: device.PlatformAndroid, Serial: "x", BundleID: "a.b"},
		{Platform: device.PlatformIOS, Serial: "x; rm", BundleID: "a.b"},
		{Platform: device.PlatformIOS, Serial: "x", BundleID: "a b\nquit"},
		{Platform: device.PlatformIOS, Serial: "x"},
		{Platform: device.PlatformIOS, Serial: "x", PID: 3, Launch: true},
	}
	for i, b := range bad {
		if validateTarget(b) == nil {
			t.Fatalf("case %d should fail", i)
		}
	}
	if err := validateTarget(bad[0]); !errors.Is(err, device.ErrUnsupported) || !strings.Contains(err.Error(), "not supported yet") {
		t.Fatalf("android error: %v", err)
	}
}

func fakeRunner(calls *[]string) Runner {
	return func(ctx context.Context, env []string, name string, args ...string) (string, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " ")+" env="+strings.Join(env, ","))
		switch {
		case len(args) > 1 && args[1] == "launch":
			return "com.apple.Preferences: 4321\n", nil
		case len(args) > 3 && args[3] == "launchctl":
			return "555\t0\tUIKitApplication:com.apple.Preferences[ab][x]\n", nil
		case len(args) > 0 && args[0] == "devicectl":
			var out string
			for i, a := range args {
				if a == "--json-output" {
					out = args[i+1]
				}
			}
			body := `{"result":{"devices":[{"identifier":"CORE-1","hardwareProperties":{"udid":"UDID-1"}}]}}`
			if args[1] == "device" && args[2] == "process" {
				body = `{"result":{"process":{"processIdentifier":901}}}`
			}
			if args[1] == "device" && args[2] == "info" && args[3] == "apps" {
				body = `{"result":{"apps":[{"bundleIdentifier":"com.me.app","url":"file:///private/var/containers/Bundle/Application/U/Me.app/"}]}}`
			}
			if args[1] == "device" && args[2] == "info" && args[3] == "processes" {
				body = `{"result":{"runningProcesses":[{"executable":"file:///private/var/containers/Bundle/Application/U/Me.app/Me","processIdentifier":902}]}}`
			}
			return "", os.WriteFile(out, []byte(body), 0o600)
		}
		return "", errors.New("unexpected command")
	}
}

func TestPlans(t *testing.T) {
	var calls []string
	run := fakeRunner(&calls)
	ctx := context.Background()

	p, err := planFor(ctx, run, Target{Platform: device.PlatformSimulator, Serial: "S1", BundleID: "com.apple.Preferences"})
	if err != nil || p.pid != 555 || p.commands[0] != "process attach --pid 555" {
		t.Fatalf("sim attach: %+v %v", p, err)
	}
	p, err = planFor(ctx, run, Target{Platform: device.PlatformSimulator, Serial: "S1", BundleID: "com.apple.Preferences", Launch: true, Env: map[string]string{"A": "b"}})
	if err != nil || p.pid != 4321 || !strings.Contains(calls[len(calls)-1], "--wait-for-debugger") || !strings.Contains(calls[len(calls)-1], "SIMCTL_CHILD_A=b") {
		t.Fatalf("sim launch: %+v %v %v", p, err, calls)
	}
	p, err = planFor(ctx, run, Target{Platform: device.PlatformIOS, Serial: "UDID-1", BundleID: "com.me.app", Launch: true})
	if err != nil || p.pid != 901 || p.commands[0] != "device select CORE-1" || p.commands[1] != "device process attach --pid 901" {
		t.Fatalf("device launch: %+v %v", p, err)
	}
	p, err = planFor(ctx, run, Target{Platform: device.PlatformIOS, Serial: "UDID-1", BundleID: "com.me.app"})
	if err != nil || p.pid != 902 {
		t.Fatalf("device attach: %+v %v", p, err)
	}
	if _, err = planFor(ctx, run, Target{Platform: device.PlatformIOS, Serial: "NOPE", BundleID: "com.me.app"}); err == nil || !strings.Contains(err.Error(), "CoreDevice") {
		t.Fatalf("unknown device: %v", err)
	}
}

func newTestManager() *Manager {
	var calls []string
	m := NewManager()
	m.NewCommand = fakeCommand
	m.Run = fakeRunner(&calls)
	return m
}

func TestSessionLifecycle(t *testing.T) {
	m := newTestManager()
	defer m.CloseAll()
	ctx := context.Background()
	s, err := m.Create(ctx, Target{Platform: device.PlatformSimulator, Serial: "S1", BundleID: "com.apple.Preferences"})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Info(); got.State != "stopped" || got.PID != 555 {
		t.Fatalf("%+v", got)
	}
	bp, err := s.SetBreakpoint(ctx, BreakpointSpec{File: "t.c", Line: 5})
	if err != nil || bp.ID != 1 || !bp.Resolved {
		t.Fatalf("%+v %v", bp, err)
	}
	if _, err = s.SetBreakpoint(ctx, BreakpointSpec{Symbol: "evil\nquit"}); err == nil {
		t.Fatal("newline in symbol must be rejected")
	}
	bps, err := s.Breakpoints(ctx)
	if err != nil || len(bps) != 1 || bps[0].Hits != 2 {
		t.Fatalf("%+v %v", bps, err)
	}

	// continue returns running immediately, state() sees the async stop.
	st, err := s.Resume(ctx, Continue, 0)
	if err != nil || st.State != "running" {
		t.Fatalf("continue: %+v %v", st, err)
	}
	if _, _, err = s.Backtrace(ctx, 0, false); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("backtrace while running should fail: %v", err)
	}
	if !s.proc.waitFor(ctx, 3*time.Second, func() bool { return s.proc.state == "stopped" }) {
		t.Fatal("async stop not observed")
	}
	state, err := s.State(ctx)
	if err != nil || state.State != "stopped" || state.StopReason != "breakpoint 1.1" || state.Frame == nil || state.Frame.Line != 5 {
		t.Fatalf("state: %+v %v", state, err)
	}
	frames, _, err := s.Backtrace(ctx, 0, false)
	if err != nil || len(frames) != 3 || frames[1].Function != "main" {
		t.Fatalf("bt: %+v %v", frames, err)
	}
	vars, err := s.Variables(ctx, 0)
	if err != nil || len(vars) != 3 || vars[2].Name != "p" {
		t.Fatalf("vars: %+v %v", vars, err)
	}
	v, err := s.Eval(ctx, "1+2", -1)
	if err != nil || v.Value != "3" {
		t.Fatalf("eval: %+v %v", v, err)
	}
	if _, err = s.Eval(ctx, "bad", -1); err == nil || !strings.Contains(err.Error(), "undeclared") {
		t.Fatalf("eval error: %v", err)
	}
	over, err := s.Resume(ctx, StepOver, 0)
	if err != nil || over.State != "stopped" || over.StopReason != "step over" || over.Frame == nil || over.Frame.Line != 6 {
		t.Fatalf("step: %+v %v", over, err)
	}
	if err := s.RemoveBreakpoint(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveBreakpoint(ctx, 7); err == nil {
		t.Fatal("unknown breakpoint should error")
	}
	if len(m.List()) != 1 {
		t.Fatal("list")
	}
	if err := m.Detach(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(s.ID); !errors.Is(err, ErrNoSession) {
		t.Fatal("session should be gone")
	}
	if !s.proc.isDead() {
		t.Fatal("lldb should have exited")
	}
}

func TestMultilineCommandsRejected(t *testing.T) {
	m := newTestManager()
	defer m.CloseAll()
	s, err := m.Create(context.Background(), Target{Platform: device.PlatformSimulator, Serial: "S1", PID: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Eval(context.Background(), "1\nplatform shell id", -1); err == nil {
		t.Fatal("newline in expression must be rejected")
	}
	if _, err := s.proc.exec(context.Background(), "a\nquit", time.Second); err == nil {
		t.Fatal("newline in command must be rejected")
	}
	if _, err := s.SetBreakpoint(context.Background(), BreakpointSpec{File: "x\nquit", Line: 1}); err == nil {
		t.Fatal("newline in file must be rejected")
	}
}

func TestAttachTimeoutKillsUnresponsiveLLDB(t *testing.T) {
	defer func(a, b time.Duration) { attachTimeout, interruptGap = a, b }(attachTimeout, interruptGap)
	attachTimeout, interruptGap = 600*time.Millisecond, 400*time.Millisecond
	m := newTestManager()
	start := time.Now()
	_, err := m.Create(context.Background(), Target{Platform: device.PlatformSimulator, Serial: "S1", PID: 999})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want timeout, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout path too slow")
	}
	if !strings.Contains(err.Error(), "Developer Tools") {
		t.Fatalf("simulator hint missing: %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatal("failed session must not be registered")
	}
}

func TestAttachInterruptedAndFailed(t *testing.T) {
	defer func(a time.Duration) { attachTimeout = a }(attachTimeout)
	attachTimeout = 500 * time.Millisecond
	m := newTestManager()
	if _, err := m.Create(context.Background(), Target{Platform: device.PlatformSimulator, Serial: "S1", PID: 998}); err == nil {
		t.Fatal("interrupted attach should fail")
	}
	_, err := m.Create(context.Background(), Target{Platform: device.PlatformIOS, Serial: "UDID-1", PID: 997})
	if err == nil || !strings.Contains(err.Error(), "get-task-allow") {
		t.Fatalf("attach error: %v", err)
	}
}

// ---- REST ----

type fakeDev struct {
	device.Device
	info device.Info
}

func (d fakeDev) Info() device.Info { return d.info }

func restServer(t *testing.T) (*httptest.Server, *Manager) {
	m := newTestManager()
	get := func(id string) (device.Device, error) {
		switch id {
		case "sim-1":
			return fakeDev{info: device.Info{ID: id, Serial: "S1", Platform: device.PlatformSimulator}}, nil
		case "android-1":
			return fakeDev{info: device.Info{ID: id, Serial: "A1", Platform: device.PlatformAndroid}}, nil
		}
		return nil, device.ErrNotFound
	}
	r := chi.NewRouter()
	r.Mount("/api/v1/debug", Routes(m, get))
	ts := httptest.NewServer(r)
	t.Cleanup(func() { ts.Close(); m.CloseAll() })
	return ts, m
}

func call(t *testing.T, method, url, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestRESTFlow(t *testing.T) {
	ts, _ := restServer(t)
	base := ts.URL + "/api/v1/debug"

	var e map[string]string
	if c := call(t, "POST", base+"/sessions", `{"deviceId":"android-1","bundleId":"a.b"}`, &e); c != 501 || !strings.Contains(e["error"], "not supported yet") {
		t.Fatalf("android: %d %v", c, e)
	}
	if c := call(t, "POST", base+"/sessions", `{"deviceId":"nope","bundleId":"a.b"}`, nil); c != 404 {
		t.Fatalf("no device: %d", c)
	}
	if c := call(t, "POST", base+"/sessions", `{"bundleId":"a.b"}`, nil); c != 400 {
		t.Fatalf("no deviceId: %d", c)
	}
	var info Info
	if c := call(t, "POST", base+"/sessions", `{"deviceId":"sim-1","bundleId":"com.apple.Preferences"}`, &info); c != 201 || info.State != "stopped" {
		t.Fatalf("create: %d %+v", c, info)
	}
	s := base + "/sessions/" + info.ID
	var bp Breakpoint
	if c := call(t, "POST", s+"/breakpoints", `{"file":"t.c","line":5}`, &bp); c != 201 || bp.ID != 1 {
		t.Fatalf("bp: %d %+v", c, bp)
	}
	if c := call(t, "POST", s+"/breakpoints", `{}`, nil); c != 400 {
		t.Fatalf("empty bp: %d", c)
	}
	var st StopInfo
	if c := call(t, "POST", s+"/step-over", ``, &st); c != 200 || st.StopReason != "step over" {
		t.Fatalf("step: %d %+v", c, st)
	}
	var bt struct{ Frames []Frame }
	if c := call(t, "GET", s+"/backtrace", "", &bt); c != 200 || len(bt.Frames) != 3 {
		t.Fatalf("bt: %d %+v", c, bt)
	}
	var v Variable
	if c := call(t, "POST", s+"/eval", `{"expression":"1+2"}`, &v); c != 200 || v.Value != "3" {
		t.Fatalf("eval: %d %+v", c, v)
	}
	if c := call(t, "POST", s+"/eval", `{"expression":"bad"}`, &e); c != 400 {
		t.Fatalf("eval err: %d", c)
	}
	var state struct {
		Stop StopInfo `json:"stop"`
	}
	if c := call(t, "GET", s+"/state", "", &state); c != 200 || state.Stop.State != "stopped" {
		t.Fatalf("state: %d %+v", c, state)
	}
	if c := call(t, "DELETE", s+"/breakpoints/1", "", nil); c != 200 {
		t.Fatalf("del bp: %d", c)
	}
	if c := call(t, "DELETE", s, "", nil); c != 200 {
		t.Fatalf("detach: %d", c)
	}
	if c := call(t, "GET", s+"/state", "", nil); c != 404 {
		t.Fatalf("after detach: %d", c)
	}
}
