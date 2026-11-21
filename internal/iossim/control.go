package iossim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/danielpaulus/go-ios/ios"

	"phoneforge/internal/device"
	"phoneforge/internal/iosbridge"
	"phoneforge/internal/wda"
)

// Simulator WDA port ranges (host network is shared, so these are host ports).
// They avoid 8100/9100 and the real-device ranges 8110-8130 / 9110-9130.
const (
	baseWDAPort   = 8200
	baseMJPEGPort = 9200
	maxSims       = 90

	mjpegFPS     = 30
	mjpegScaling = 50
	mjpegQuality = 60
)

// ErrNotBuilt means the simulator WDA build is missing.
var ErrNotBuilt = errors.New("iossim: simulator WebDriverAgent is not built: run scripts/build-wda.sh --sim")

// ErrNotBooted means an operation needs a booted simulator.
var ErrNotBooted = errors.New("iossim: simulator is not booted")

// Control owns simulator state: simctl access, the WDA runner manager and the
// per-simulator wda.Client cache. It is shared by the discoverer, the
// /simulators routes and the generic iOS bridge routes.
type Control struct {
	Run Runner
	Mgr *iosbridge.Manager
	// OnChange, if set, is called after boot/shutdown so callers can rescan devices.
	OnChange func()

	wdaHome string

	mu      sync.Mutex
	clients map[string]*wda.Client
	known   map[string]bool // udids seen in the last simctl listing
	slots   map[string]int
}

// New returns a Control. stateDir is unused for now (reserved); wdaHome ""
// means $PF_WDA_HOME or ~/.phoneforge/wda.
func New(wdaHome string) *Control {
	c := &Control{Run: ExecRunner, wdaHome: wdaHome, clients: map[string]*wda.Client{}, known: map[string]bool{}, slots: map[string]int{}}
	c.Mgr = iosbridge.NewManagerWithDeps("", iosbridge.Deps{
		Tunnel:    simTunnel{},
		Launcher:  launcher{c: c},
		Forwarder: identityForwarder{},
		Installer: alwaysInstalled{},
		Probe:     probe,
	})
	return c
}

// WDAHome returns the WDA home directory.
func (c *Control) WDAHome() string {
	if c.wdaHome != "" {
		return c.wdaHome
	}
	if v := os.Getenv("PF_WDA_HOME"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".phoneforge", "wda")
}

// XCTestRun returns the path of the simulator .xctestrun file, or "".
func (c *Control) XCTestRun() string {
	m, _ := filepath.Glob(filepath.Join(c.WDAHome(), "build-sim", "Build", "Products", "*iphonesimulator*.xctestrun"))
	sort.Strings(m)
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

// IsSim reports whether udid is a known simulator.
func (c *Control) IsSim(udid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.known[udid]
}

func (c *Control) note(sims []Sim) {
	c.mu.Lock()
	for _, s := range sims {
		c.known[s.UDID] = true
	}
	c.mu.Unlock()
}

// Client returns the cached WDA client for a simulator.
func (c *Control) Client(udid string) *wda.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	cl := c.clients[udid]
	if cl == nil {
		cl = wda.New("")
		c.clients[udid] = cl
	}
	return cl
}

// Active returns a func reporting whether WDA is running for udid; it also
// points the client at the live URL.
func (c *Control) Active(udid string) func() bool {
	return func() bool {
		st := c.Mgr.Status(udid)
		if st.State != iosbridge.StateRunning || st.WDAURL == "" {
			return false
		}
		c.Client(udid).SetBaseURL(st.WDAURL)
		return true
	}
}

// Wrap routes a simulator device through WDA while it is running.
func (c *Control) Wrap(d device.Device) device.Device {
	info := d.Info()
	if !info.Simulator || info.Serial == "" {
		return d
	}
	return wda.Wrap(d, c.Client(info.Serial), c.Active(info.Serial))
}

func portFree(p int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// PrepareOpts fills in unique host ports for udid when o has none.
func (c *Control) PrepareOpts(udid string, o iosbridge.Options) iosbridge.Options {
	if o.StartTimeoutSec == 0 {
		o.StartTimeoutSec = 120 // xcodebuild is slow right after a boot
	}
	if o.WDAPort != 0 || o.MJPEGPort != 0 {
		return o
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	used := map[int]bool{}
	for u, i := range c.slots {
		if u != udid {
			used[i] = true
		}
	}
	try := func(i int) bool {
		return !used[i] && portFree(baseWDAPort+i) && portFree(baseMJPEGPort+i)
	}
	if i, ok := c.slots[udid]; ok && (c.Mgr.Status(udid).State == iosbridge.StateRunning || try(i)) {
		o.WDAPort, o.MJPEGPort = baseWDAPort+i, baseMJPEGPort+i
		return o
	}
	for i := 0; i < maxSims; i++ {
		if try(i) {
			c.slots[udid] = i
			o.WDAPort, o.MJPEGPort = baseWDAPort+i, baseMJPEGPort+i
			return o
		}
	}
	o.WDAPort, o.MJPEGPort = baseWDAPort, baseMJPEGPort
	return o
}

// StartWDA boots WDA on a booted simulator and applies MJPEG settings.
func (c *Control) StartWDA(ctx context.Context, udid string, o iosbridge.Options) (iosbridge.Status, error) {
	sim, err := c.Get(ctx, udid)
	if err != nil {
		return iosbridge.Status{}, err
	}
	if !sim.Booted() {
		return iosbridge.Status{}, ErrNotBooted
	}
	if c.XCTestRun() == "" {
		return iosbridge.Status{}, ErrNotBuilt
	}
	st, err := c.Mgr.Start(ctx, udid, c.PrepareOpts(udid, o))
	if err != nil {
		return st, err
	}
	cl := c.Client(udid)
	cl.SetBaseURL(st.WDAURL)
	mctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = cl.SetMJPEGSettings(mctx, mjpegFPS, mjpegScaling, mjpegQuality)
	return st, nil
}

// StopWDA stops the WDA runner (no-op when stopped).
func (c *Control) StopWDA(udid string) iosbridge.Status {
	_ = c.Mgr.Stop(udid)
	return c.Mgr.Status(udid)
}

// List returns all available simulators and remembers their udids.
func (c *Control) List(ctx context.Context) ([]Sim, error) {
	sims, err := ListAll(ctx, c.Run)
	if err == nil {
		c.note(sims)
	}
	return sims, err
}

// Get returns one simulator by udid.
func (c *Control) Get(ctx context.Context, udid string) (Sim, error) {
	sims, err := c.List(ctx)
	if err != nil {
		return Sim{}, err
	}
	for _, s := range sims {
		if s.UDID == udid {
			return s, nil
		}
	}
	return Sim{}, device.ErrNotFound
}

// Boot boots a simulator and waits until it is ready. Booting a booted
// simulator is a no-op.
func (c *Control) Boot(ctx context.Context, udid string) (Sim, error) {
	sim, err := c.Get(ctx, udid)
	if err != nil {
		return Sim{}, err
	}
	if !sim.Booted() {
		if _, err := c.Run(ctx, "boot", udid); err != nil && !strings.Contains(err.Error(), "current state: Booted") {
			return Sim{}, err
		}
	}
	bctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if _, err := c.Run(bctx, "bootstatus", udid, "-b"); err != nil {
		return Sim{}, err
	}
	if c.OnChange != nil {
		c.OnChange()
	}
	return c.Get(ctx, udid)
}

// Shutdown stops WDA, then shuts the simulator down.
func (c *Control) Shutdown(ctx context.Context, udid string) (Sim, error) {
	if _, err := c.Get(ctx, udid); err != nil {
		return Sim{}, err
	}
	_ = c.Mgr.Stop(udid)
	if _, err := c.Run(ctx, "shutdown", udid); err != nil && !strings.Contains(err.Error(), "current state: Shutdown") {
		return Sim{}, err
	}
	if c.OnChange != nil {
		c.OnChange()
	}
	return c.Get(ctx, udid)
}

// StopAll stops every WDA runner.
func (c *Control) StopAll() { c.Mgr.StopAll() }

// ---- iosbridge backends ----------------------------------------------------

type simTunnel struct{}

func (simTunnel) Ensure(ctx context.Context, udid string) (ios.DeviceEntry, error) {
	return ios.DeviceEntry{Properties: ios.DeviceProperties{SerialNumber: udid}}, nil
}
func (simTunnel) Release(string) error { return nil }

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// identityForwarder: the simulator shares the host network.
type identityForwarder struct{}

func (identityForwarder) Forward(ctx context.Context, dev ios.DeviceEntry, devicePort int) (int, io.Closer, error) {
	return devicePort, nopCloser{}, nil
}

type alwaysInstalled struct{}

func (alwaysInstalled) Installed(context.Context, string, string) (bool, error) { return true, nil }

// probe accepts any 2xx /status and reports the library bundle id the WDA
// manager expects.
func probe(ctx context.Context, url string) (string, error) {
	st, err := wda.New(strings.TrimSuffix(url, "/status")).Status(ctx)
	if err != nil {
		return "", err
	}
	_ = st
	return "com.facebook.WebDriverAgentRunner", nil
}

type launcher struct{ c *Control }

// Run executes `xcodebuild test-without-building` for the simulator and blocks
// until it exits or ctx is cancelled.
func (l launcher) Run(ctx context.Context, dev ios.DeviceEntry, o iosbridge.Options, logs io.Writer) error {
	udid := dev.Properties.SerialNumber
	xr := l.c.XCTestRun()
	if xr == "" {
		return ErrNotBuilt
	}
	// A fresh, explicit result bundle avoids xcodebuild stalling on a stale default one.
	rb := filepath.Join(os.TempDir(), "pf-sim-wda-"+udid+".xcresult")
	_ = os.RemoveAll(rb)
	cmd := exec.Command("xcodebuild", "test-without-building", "-xctestrun", xr, "-destination", "id="+udid, "-resultBundlePath", rb)
	env := append(os.Environ(),
		fmt.Sprintf("TEST_RUNNER_USE_PORT=%d", o.WDAPort),
		fmt.Sprintf("TEST_RUNNER_MJPEG_SERVER_PORT=%d", o.MJPEGPort))
	for k, v := range o.Env {
		env = append(env, "TEST_RUNNER_"+k+"="+v)
	}
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = logs, logs
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	waitc := make(chan error, 1)
	go func() { waitc <- cmd.Wait() }()
	select {
	case err := <-waitc:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-waitc:
		case <-time.After(4 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-waitc
		}
		// Make sure the runner app is gone so its port is released.
		tctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = l.c.Run(tctx, "terminate", udid, strings.TrimSuffix(o.RunnerBundleID, ".xctrunner")+".xctrunner")
		_, _ = l.c.Run(tctx, "terminate", udid, strings.TrimSuffix(o.RunnerBundleID, ".xctrunner"))
		return ctx.Err()
	}
}
