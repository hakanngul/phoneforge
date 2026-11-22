package iosbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios"
)

type fakeTunnel struct {
	err      error
	released atomic.Int32
}

func (f *fakeTunnel) Ensure(ctx context.Context, udid string) (ios.DeviceEntry, error) {
	return ios.DeviceEntry{}, f.err
}
func (f *fakeTunnel) Release(string) error { f.released.Add(1); return nil }

type fakeLauncher struct {
	exit   chan error // send to make the runner exit on its own
	runs   atomic.Int32
	onRun  func(Options)
	onExit func()
}

func newFakeLauncher() *fakeLauncher { return &fakeLauncher{exit: make(chan error, 1)} }

func (f *fakeLauncher) Run(ctx context.Context, _ ios.DeviceEntry, o Options, logs io.Writer) error {
	f.runs.Add(1)
	if f.onRun != nil {
		f.onRun(o)
	}
	if f.onExit != nil {
		defer f.onExit()
	}
	_, _ = logs.Write([]byte("runner up " + o.RunnerBundleID + "\npartial"))
	select {
	case <-ctx.Done():
		return nil
	case err := <-f.exit:
		return err
	}
}

type closeFlag struct{ closed atomic.Bool }

func (c *closeFlag) Close() error { c.closed.Store(true); return nil }

type fakeFwd struct {
	mu     sync.Mutex
	ports  map[int]int
	flags  []*closeFlag
	failOn int
}

func (f *fakeFwd) Forward(_ context.Context, _ ios.DeviceEntry, dp int) (int, io.Closer, error) {
	if f.failOn == dp {
		return 0, nil, errors.New("boom")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := &closeFlag{}
	f.flags = append(f.flags, c)
	return f.ports[dp], c, nil
}

type fakeInst struct {
	ok  bool
	err error
}

func (f fakeInst) Installed(context.Context, string, string) (bool, error) { return f.ok, f.err }

type rig struct {
	m   *Manager
	tun *fakeTunnel
	l   *fakeLauncher
	f   *fakeFwd
	up  atomic.Bool
	// launched flips when a launcher Run starts; before that /status is down.
	launched atomic.Int32 // local port of the WDA our launcher serves (0 = none)
	// bundle is what /status reports; empty means our own WDA bundle id.
	bundle atomic.Value
	urls   []string
	shut   []string // WDA urls Shutdown was called with
	mu     sync.Mutex
}

func newRig(inst fakeInst) *rig {
	r := &rig{tun: &fakeTunnel{}, l: newFakeLauncher(), f: &fakeFwd{ports: map[int]int{8110: 45100, 9110: 45101, 8120: 45102, 9120: 45103, 8130: 45104, 9130: 45105}}}
	r.up.Store(true)
	r.l.onRun = func(o Options) { r.launched.Store(int32(r.f.ports[o.WDAPort])) }
	r.l.onExit = func() { r.launched.Store(0) }
	r.m = NewManagerWithDeps(t0(), Deps{
		Tunnel: r.tun, Launcher: r.l, Forwarder: r.f, Installer: inst,
		Probe: func(_ context.Context, u string) (string, error) {
			r.mu.Lock()
			r.urls = append(r.urls, u)
			r.mu.Unlock()
			if !r.up.Load() || !strings.Contains(u, fmt.Sprintf(":%d/", r.launched.Load())) {
				return "", errors.New("down")
			}
			if b, _ := r.bundle.Load().(string); b != "" {
				return b, nil
			}
			return "com.pocketlab.WebDriverAgentRunner", nil
		},
		Shutdown: func(_ context.Context, u string) {
			r.mu.Lock()
			r.shut = append(r.shut, u)
			r.mu.Unlock()
		},
		PollInterval: 5 * time.Millisecond, MismatchGrace: 60 * time.Millisecond, PreProbeWindow: 30 * time.Millisecond,
	})
	return r
}

func t0() string { return "" }

const udid = "00008150-TEST"

func TestStartStopHappyPath(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	if st := r.m.Status(udid); st.State != StateStopped {
		t.Fatalf("initial state %s", st.State)
	}
	st, err := r.m.Start(context.Background(), udid, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateRunning || st.WDAURL != "http://127.0.0.1:45100" || st.MJPEGURL != "http://127.0.0.1:45101" {
		t.Fatalf("bad status %+v", st)
	}
	if st.RunnerBundleID != DefaultRunnerBundleID || !st.Installed {
		t.Fatalf("bad status %+v", st)
	}
	// idempotent start (the launcher goroutine may not have been scheduled yet)
	for i := 0; i < 200 && r.l.runs.Load() == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := r.m.Start(context.Background(), udid, Options{}); err != nil || r.l.runs.Load() != 1 {
		t.Fatalf("second start err=%v runs=%d", err, r.l.runs.Load())
	}
	logs := strings.Join(r.m.Logs(udid), "\n")
	if !strings.Contains(logs, "runner up "+DefaultRunnerBundleID) || !strings.Contains(logs, "WDA ready") {
		t.Fatalf("logs: %s", logs)
	}
	if err := r.m.Stop(udid); err != nil {
		t.Fatal(err)
	}
	st = r.m.Status(udid)
	if st.State != StateStopped || st.WDAURL != "" {
		t.Fatalf("after stop %+v", st)
	}
	for _, c := range r.f.flags {
		if !c.closed.Load() {
			t.Fatal("forward not closed")
		}
	}
	if r.tun.released.Load() != 1 {
		t.Fatalf("tunnel released %d", r.tun.released.Load())
	}
	// restartable
	if st, err := r.m.Start(context.Background(), udid, Options{}); err != nil || st.State != StateRunning {
		t.Fatalf("restart %+v %v", st, err)
	}
	r.m.StopAll()
	if r.m.Status(udid).State != StateStopped {
		t.Fatal("StopAll did not stop")
	}
}

func TestNotInstalled(t *testing.T) {
	r := newRig(fakeInst{ok: false})
	_, err := r.m.Start(context.Background(), udid, Options{RunnerBundleID: "x.y.xctrunner"})
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
	st := r.m.Status(udid)
	if st.State != StateError || st.Installed || st.RunnerBundleID != "x.y.xctrunner" || st.Error == "" {
		t.Fatalf("status %+v", st)
	}
	if r.l.runs.Load() != 0 {
		t.Fatal("launcher must not run")
	}
}

func TestInstalledLookupErrorDoesNotBlock(t *testing.T) {
	r := newRig(fakeInst{err: errors.New("lockdown down")})
	if _, err := r.m.Start(context.Background(), udid, Options{}); err != nil {
		t.Fatal(err)
	}
	r.m.StopAll()
}

func TestTunnelError(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	r.tun.err = ErrTunnelUnsupported
	_, err := r.m.Start(context.Background(), udid, Options{})
	if !errors.Is(err, ErrTunnelUnsupported) {
		t.Fatalf("err %v", err)
	}
	if st := r.m.Status(udid); st.State != StateError {
		t.Fatalf("state %s", st.State)
	}
}

func TestForwardErrorCleansUp(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	r.f.failOn = 9110
	if _, err := r.m.Start(context.Background(), udid, Options{}); err == nil {
		t.Fatal("expected error")
	}
	if st := r.m.Status(udid); st.State != StateError {
		t.Fatalf("state %s", st.State)
	}
	if len(r.f.flags) != 1 || !r.f.flags[0].closed.Load() {
		t.Fatal("first forward must be closed")
	}
}

func TestTimeout(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	r.up.Store(false)
	start := time.Now()
	_, err := r.m.Start(context.Background(), udid, Options{StartTimeoutSec: 1})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("too slow")
	}
	if st := r.m.Status(udid); st.State != StateError {
		t.Fatalf("state %s", st.State)
	}
}

func TestRunnerExitsDuringStart(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	r.up.Store(false)
	r.l.exit <- errors.New("xctrunner crashed")
	_, err := r.m.Start(context.Background(), udid, Options{})
	if err == nil || !strings.Contains(err.Error(), "xctrunner crashed") {
		t.Fatalf("err %v", err)
	}
	if st := r.m.Status(udid); st.State != StateError || !strings.Contains(st.Error, "crashed") {
		t.Fatalf("status %+v", st)
	}
}

func TestRunnerDiesWhileRunning(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	if _, err := r.m.Start(context.Background(), udid, Options{}); err != nil {
		t.Fatal(err)
	}
	r.l.exit <- errors.New("device unplugged")
	deadline := time.Now().Add(2 * time.Second)
	for r.m.Status(udid).State == StateRunning && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	st := r.m.Status(udid)
	if st.State != StateError || !strings.Contains(st.Error, "unplugged") || st.WDAURL != "" {
		t.Fatalf("status %+v", st)
	}
	if !r.f.flags[0].closed.Load() {
		t.Fatal("forward not closed")
	}
	// can start again after error
	if st, err := r.m.Start(context.Background(), udid, Options{}); err != nil || st.State != StateRunning {
		t.Fatalf("restart %+v %v", st, err)
	}
	r.m.StopAll()
}

func TestStopWhileStarting(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	r.up.Store(false)
	errc := make(chan error, 1)
	go func() {
		_, err := r.m.Start(context.Background(), udid, Options{})
		errc <- err
	}()
	for r.m.Status(udid).State != StateStarting {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := r.m.Start(context.Background(), udid, Options{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
	_ = r.m.Stop(udid)
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("start should fail")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("start did not return")
	}
	if st := r.m.Status(udid); st.State != StateStopped {
		t.Fatalf("state %s", st.State)
	}
}

func TestContextCancelDuringStart(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	r.up.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.m.Start(ctx, udid, Options{}); err == nil {
		t.Fatal("expected error")
	}
	if st := r.m.Status(udid); st.State != StateError {
		t.Fatalf("state %s", st.State)
	}
}

func TestIdentityMismatchNotReady(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	r.bundle.Store("com.other.WebDriverAgent") // another WDA
	_, err := r.m.Start(context.Background(), udid, Options{WDAPort: 8110, MJPEGPort: 9110, StartTimeoutSec: 5})
	if err == nil || !strings.Contains(err.Error(), "used by another WebDriverAgent (com.other.WebDriverAgent)") {
		t.Fatalf("err %v", err)
	}
	if st := r.m.Status(udid); st.State != StateError || !strings.Contains(st.Error, "another WebDriverAgent") {
		t.Fatalf("status %+v", st)
	}
	if strings.Contains(strings.Join(r.m.Logs(udid), "\n"), "WDA ready") {
		t.Fatal("must not be ready")
	}
}

func TestIdentityAcceptsRunnerBundleWithSuffix(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	r.bundle.Store(DefaultRunnerBundleID)
	if _, err := r.m.Start(context.Background(), udid, Options{}); err != nil {
		t.Fatal(err)
	}
	r.m.StopAll()
}

func TestIdentityAcceptsLibraryBundleAfterOurLaunch(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	r.bundle.Store("com.facebook.WebDriverAgentRunner")
	st, err := r.m.Start(context.Background(), udid, Options{})
	if err != nil || st.State != StateRunning || st.WDAURL != "http://127.0.0.1:45100" {
		t.Fatalf("st %+v err %v", st, err)
	}
	r.m.StopAll()
}

func TestPreLaunchBusyPortSkipped(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	// 8110 is already answered by someone else before we launch anything.
	r.m.deps.Probe = func(_ context.Context, u string) (string, error) {
		if strings.HasSuffix(u, ":45100/status") {
			return "com.facebook.WebDriverAgentRunner", nil
		}
		if !strings.Contains(u, fmt.Sprintf(":%d/", r.launched.Load())) {
			return "", errors.New("down")
		}
		return "com.facebook.WebDriverAgentRunner", nil
	}
	st, err := r.m.Start(context.Background(), udid, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.WDAURL != "http://127.0.0.1:45102" || r.l.runs.Load() != 1 {
		t.Fatalf("status %+v runs %d", st, r.l.runs.Load())
	}
	if !r.f.flags[0].closed.Load() || !r.f.flags[1].closed.Load() {
		t.Fatal("skipped pair forwards must be closed")
	}
	r.m.StopAll()
}

func TestPreLaunchAllPortsBusy(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	r.m.deps.Probe = func(context.Context, string) (string, error) { return "com.facebook.WebDriverAgentRunner", nil }
	_, err := r.m.Start(context.Background(), udid, Options{})
	if err == nil || !strings.Contains(err.Error(), "another WebDriverAgent") || r.l.runs.Load() != 0 {
		t.Fatalf("err %v runs %d", err, r.l.runs.Load())
	}
}

// busyLauncher reports "Address already in use" on the first port pair.
type busyLauncher struct {
	*fakeLauncher
	ports []int
	mu    sync.Mutex
}

func (b *busyLauncher) Run(ctx context.Context, d ios.DeviceEntry, o Options, logs io.Writer) error {
	b.mu.Lock()
	b.ports = append(b.ports, o.WDAPort)
	first := len(b.ports) == 1
	b.mu.Unlock()
	if first {
		_, _ = logs.Write([]byte("Failed to start web server on port 8110: Address already in use\n"))
		<-ctx.Done()
		return nil
	}
	return b.fakeLauncher.Run(ctx, d, o, logs)
}

func TestPortRetryOnAddressInUse(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	bl := &busyLauncher{fakeLauncher: r.l}
	r.m.deps.Launcher = bl
	// first pair: runner logs EADDRINUSE and never answers; second pair works
	r.m.deps.Probe = func(_ context.Context, u string) (string, error) {
		if !strings.Contains(u, fmt.Sprintf(":%d/", r.launched.Load())) {
			return "", errors.New("down")
		}
		return "com.pocketlab.WebDriverAgentRunner", nil
	}
	st, err := r.m.Start(context.Background(), udid, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st.WDAURL != "http://127.0.0.1:45102" || st.MJPEGURL != "http://127.0.0.1:45103" {
		t.Fatalf("status %+v", st)
	}
	if len(bl.ports) != 2 || bl.ports[0] != 8110 || bl.ports[1] != 8120 {
		t.Fatalf("ports %v", bl.ports)
	}
	if !r.f.flags[0].closed.Load() || !r.f.flags[1].closed.Load() {
		t.Fatal("first attempt forwards must be closed")
	}
	r.m.StopAll()
}

// alwaysBusy fails every port pair.
type alwaysBusy struct{}

func (alwaysBusy) Run(ctx context.Context, _ ios.DeviceEntry, o Options, logs io.Writer) error {
	_, _ = logs.Write([]byte("Failed to start web server: Addr"))
	_, _ = logs.Write([]byte("ess already in use\n"))
	<-ctx.Done()
	return nil
}

func TestAddressInUseSurfacesInStatusError(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	r.up.Store(false)
	r.m.deps.Launcher = alwaysBusy{}
	_, err := r.m.Start(context.Background(), udid, Options{})
	if err == nil || !strings.Contains(err.Error(), "Address already in use") {
		t.Fatalf("err %v", err)
	}
	if st := r.m.Status(udid); st.State != StateError || !strings.Contains(st.Error, "Address already in use") {
		t.Fatalf("status %+v", st)
	}
}

func TestRingBuffer(t *testing.T) {
	r := newRing(3)
	_, _ = r.Write([]byte("a\nb\nc\nd\ne"))
	// completed lines keep the newest 3; the partial line is appended
	if got := strings.Join(r.snapshot(), ","); got != "b,c,d,e" {
		t.Fatalf("got %s", got)
	}
	_, _ = r.Write([]byte("x\n"))
	if got := strings.Join(r.snapshot(), ","); got != "c,d,ex" {
		t.Fatalf("got %s", got)
	}
}

func TestOptionsDefaults(t *testing.T) {
	o := Options{}.normalized()
	if o.RunnerBundleID != DefaultRunnerBundleID || o.TestRunnerBundleID != DefaultRunnerBundleID ||
		o.XCTestConfig != DefaultXCTestConfig || o.WDAPort != 8110 || o.MJPEGPort != 9110 {
		t.Fatalf("%+v", o)
	}
	if strings.Contains(DefaultRunnerBundleID, "mobai") {
		t.Fatal("must not use a mobai bundle")
	}
}

func TestHTTPRoutes(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	r.bundle.Store("a.b")
	h := r.m.Routes(func(*http.Request) string { return udid })
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	if w := do("GET", "/status", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"stopped"`) {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/start", `{"runnerBundleId":"a.b.xctrunner"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"running"`) || !strings.Contains(w.Body.String(), "a.b.xctrunner") {
		t.Fatalf("start %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/logs", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "runner up a.b.xctrunner") {
		t.Fatalf("logs %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/stop", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"stopped"`) {
		t.Fatalf("stop %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/start", `{bad`); w.Code != 400 {
		t.Fatalf("bad body %d", w.Code)
	}
	r.tun.err = ErrTunnelUnsupported
	if w := do("POST", "/start", ``); w.Code != 409 || !strings.Contains(w.Body.String(), "IOSBRIDGE_TUNNEL_ROOT") {
		t.Fatalf("tunnel %d %s", w.Code, w.Body)
	}
}
