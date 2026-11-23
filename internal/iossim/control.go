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
	"time"

	"github.com/danielpaulus/go-ios/ios"

	"pocketlab/internal/brand"
	"pocketlab/internal/device"
	"pocketlab/internal/iosbridge"
	"pocketlab/internal/iossim/slim"
	"pocketlab/internal/procutil"
	"pocketlab/internal/wda"
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
var ErrNotBuilt = errors.New("iossim: simulator WebDriverAgent is not built yet: use \"Build WDA\" in Pocketlab (Integrations > iOS Simulators, needs Xcode) or run `pocketlab ios build-wda --sim`")

// ErrWDAStarting means WDA is still coming up; the caller should retry shortly.
var ErrWDAStarting = errors.New("iossim: starting WDA on the simulator, retry shortly")

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
	// lastPorts remembers the host ports each simulator's WDA last used (also
	// explicit ones), so an automatic restart reuses them instead of the default.
	lastPorts map[string][2]int
	sup       *iosbridge.Supervisor
	starts    map[string]*startCall // in-flight lazy WDA starts
	// autoWait bounds how long EnsureWDA blocks (default 60s; tests lower it).
	autoWait time.Duration

	// Host runs host commands (ps, footprint) for memory measurement; nil = ExecHost.
	Host HostRunner

	farmSt       *farmStore
	rebootNeeded map[string]bool // slim/unslim left labels that need a reboot

	slimPath  string
	slimEng   *slim.Engine
	noReapply bool
}

type startCall struct {
	done chan struct{}
	err  error
}

// New returns a Control. stateDir is unused for now (reserved); wdaHome ""
// means $PL_WDA_HOME or ~/.pocketlab/wda.
func New(wdaHome string) *Control {
	c := &Control{Run: ExecRunner, wdaHome: wdaHome, clients: map[string]*wda.Client{}, known: map[string]bool{}, slots: map[string]int{}, lastPorts: map[string][2]int{}, rebootNeeded: map[string]bool{}, starts: map[string]*startCall{}}
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
	if v := brand.Getenv("PL_WDA_HOME"); v != "" {
		return v
	}
	return filepath.Join(brand.Home(), "wda")
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
	udid := info.Serial
	return wda.WrapEnsure(d, c.Client(udid), c.Active(udid), func(ctx context.Context) error { return c.EnsureWDA(ctx, udid) })
}

// portFree reports whether nothing listens on host port p. A bind test alone
// is not enough: on macOS another process listening on the wildcard address
// (the user's own Pocketlab WDA) can still let a loopback bind succeed, so the
// wildcard bind and a connect probe are checked too.
func portFree(p int) bool {
	for _, addr := range []string{fmt.Sprintf(":%d", p), fmt.Sprintf("127.0.0.1:%d", p)} {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return false
		}
		_ = l.Close()
	}
	if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p), 200*time.Millisecond); err == nil {
		_ = conn.Close()
		return false
	}
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
	if lp, ok := c.lastPorts[udid]; ok {
		// Reuse the previous ports (supervisor restart after slim/reboot) when still ours or free.
		if c.Mgr.Status(udid).State == iosbridge.StateRunning || (portFree(lp[0]) && portFree(lp[1]) && !c.portTaken(udid, lp)) {
			o.WDAPort, o.MJPEGPort = lp[0], lp[1]
			return o
		}
	}
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

// portTaken reports whether another simulator's remembered ports clash with lp.
func (c *Control) portTaken(udid string, lp [2]int) bool {
	for u, o := range c.lastPorts {
		if u != udid && (o[0] == lp[0] || o[1] == lp[1] || o[0] == lp[1] || o[1] == lp[0]) {
			return true
		}
	}
	return false
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
	po := c.PrepareOpts(udid, o)
	c.mu.Lock()
	c.lastPorts[udid] = [2]int{po.WDAPort, po.MJPEGPort}
	c.mu.Unlock()
	st, err := c.Mgr.Start(ctx, udid, po)
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

// SetStatePath persists the per-simulator desired WDA state at path (loaded
// immediately). Call before Supervise/Run.
func (c *Control) SetStatePath(path string) { c.Mgr.SetStatePath(path) }

// Supervise returns the supervisor that keeps WDA running on booted simulators
// whose desired state is on. The caller feeds it discovery (Seen) and runs it.
func (c *Control) Supervise(log func(string)) *iosbridge.Supervisor {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sup == nil {
		sup := iosbridge.NewSupervisor(c.Mgr, func(ctx context.Context, udid string) (iosbridge.Status, error) {
			sctx, cancel := context.WithTimeout(ctx, 150*time.Second)
			defer cancel()
			return c.StartWDA(sctx, udid, iosbridge.Options{})
		})
		sup.Blocking = func(err error) bool { return errors.Is(err, ErrNotBuilt) }
		sup.NotReady = func(err error) bool { return errors.Is(err, ErrNotBooted) || errors.Is(err, device.ErrNotFound) }
		sup.Log = log
		c.sup = sup
	}
	return c.sup
}

// ClearBlocks resets the supervisor's give-up/blocked flags for udid.
func (c *Control) ClearBlocks(udid string) {
	c.mu.Lock()
	sup := c.sup
	c.mu.Unlock()
	if sup != nil {
		sup.ClearBlocks(udid)
	}
}

// UserStart is an explicit start: it records the desired state (so WDA comes
// back after a server restart or simulator reboot), clears any give-up, then
// starts WDA.
func (c *Control) UserStart(ctx context.Context, udid string, o iosbridge.Options) (iosbridge.Status, error) {
	_ = c.Mgr.SetDesired(udid, true)
	c.ClearBlocks(udid)
	st, err := c.StartWDA(ctx, udid, o)
	if errors.Is(err, ErrNotBuilt) || errors.Is(err, ErrNotBooted) {
		_ = c.Mgr.SetDesired(udid, false) // cannot be satisfied now; do not retry on every launch
	}
	return st, err
}

// UserStop is an explicit stop: it clears the desired state, then stops WDA.
func (c *Control) UserStop(udid string) iosbridge.Status {
	_ = c.Mgr.UserStop(udid)
	return c.Mgr.Status(udid)
}

// AutoWDAEnabled reports whether lazy WDA start is on (PL_SIM_AUTOWDA=0 disables).
func AutoWDAEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(brand.Getenv("PL_SIM_AUTOWDA"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// EnsureWDA makes sure WDA is running for the booted simulator udid, starting
// it if needed and blocking up to ~60 s for it to come up (the start itself
// continues in the background past that and ErrWDAStarting is returned). It is
// a no-op when WDA is already running or PL_SIM_AUTOWDA=0.
func (c *Control) EnsureWDA(ctx context.Context, udid string) error {
	if c.Active(udid)() || !AutoWDAEnabled() {
		return nil
	}
	var call *startCall
	if c.Mgr.Status(udid).State != iosbridge.StateStarting {
		if c.XCTestRun() == "" {
			return ErrNotBuilt
		}
		c.mu.Lock()
		call = c.starts[udid]
		if call == nil {
			call = &startCall{done: make(chan struct{})}
			c.starts[udid] = call
			go func(call *startCall) {
				sctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
				defer cancel()
				_, err := c.UserStart(sctx, udid, iosbridge.Options{})
				c.mu.Lock()
				call.err = err
				delete(c.starts, udid)
				c.mu.Unlock()
				close(call.done)
			}(call)
		}
		c.mu.Unlock()
	}
	wait := c.autoWait
	if wait <= 0 {
		wait = 60 * time.Second
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	var done <-chan struct{}
	if call != nil {
		done = call.done
	}
	for {
		if c.Active(udid)() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return ErrWDAStarting
		case <-done:
			if c.Active(udid)() {
				return nil
			}
			if call.err != nil && !errors.Is(call.err, iosbridge.ErrBusy) {
				return call.err
			}
			done = nil // someone else is starting it; keep polling
		case <-tick.C:
			if done == nil {
				if st := c.Mgr.Status(udid); st.State != iosbridge.StateStarting && !c.Active(udid)() {
					if st.Error != "" {
						return errors.New("iossim: WDA failed to start: " + st.Error)
					}
					return ErrWDAStarting
				}
			}
		}
	}
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
	c.setRebootNeeded(udid, false)
	if c.OnChange != nil {
		c.OnChange()
	}
	sim, err = c.Get(ctx, udid)
	if err == nil {
		c.mu.Lock()
		skip := c.noReapply
		c.mu.Unlock()
		if !skip {
			c.reapplyAfterBoot(sim)
		}
	}
	return sim, err
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
	c.setRebootNeeded(udid, false)
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
	procutil.SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	waitc := make(chan error, 1)
	go func() { waitc <- cmd.Wait() }()
	select {
	case err := <-waitc:
		return err
	case <-ctx.Done():
		procutil.Terminate(cmd)
		select {
		case <-waitc:
		case <-time.After(4 * time.Second):
			procutil.KillGroup(cmd)
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

func isNotFound(err error) bool { return errors.Is(err, device.ErrNotFound) }
