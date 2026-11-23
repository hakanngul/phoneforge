package iosbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danielpaulus/go-ios/ios"
)

// DefaultRunnerBundleID is the Pocketlab-owned WDA runner bundle id.
const DefaultRunnerBundleID = "com.pocketlab.WebDriverAgentRunner.xctrunner"

// DefaultXCTestConfig is the xctest bundle name inside the WDA runner.
const DefaultXCTestConfig = "WebDriverAgentRunner.xctest"

const (
	// Default on-device ports. Other WebDriverAgent installs commonly use
	// 8100/9100, so Pocketlab uses a distinct range to coexist with them.
	defaultWDAPort   = 8110
	defaultMJPEGPort = 9110
	defaultTimeout   = 60 * time.Second
	logLines         = 2000
)

var (
	// ErrNotInstalled means the WDA runner app is not on the phone.
	ErrNotInstalled = errors.New("iosbridge: WebDriverAgent runner is not installed on the device")
	// ErrTunnelUnsupported means the device needs a root-only kernel tunnel.
	ErrTunnelUnsupported = errors.New("iosbridge: device requires a root kernel tunnel")
	// ErrBusy means a start is already in progress for the device.
	ErrBusy = errors.New("iosbridge: runner is already starting")
)

// State is the lifecycle state of a runner.
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateError    State = "error"
)

// Options configures one runner start. Zero values pick defaults.
type Options struct {
	// RunnerBundleID is the installed xctrunner app (also used as the test
	// runner bundle id unless TestRunnerBundleID is set).
	RunnerBundleID string `json:"runnerBundleId,omitempty"`
	// TestRunnerBundleID defaults to RunnerBundleID.
	TestRunnerBundleID string `json:"testRunnerBundleId,omitempty"`
	// XCTestConfig defaults to WebDriverAgentRunner.xctest.
	XCTestConfig string `json:"xctestConfig,omitempty"`
	// Env is passed to the runner (USE_PORT / MJPEG_SERVER_PORT are set automatically).
	Env  map[string]string `json:"env,omitempty"`
	Args []string          `json:"args,omitempty"`
	// WDAPort / MJPEGPort are the on-device ports (default 8110 / 9110; on "Address already in use" the next pair 8120 / 9120 is tried).
	WDAPort   int `json:"wdaPort,omitempty"`
	MJPEGPort int `json:"mjpegPort,omitempty"`
	// StartTimeout bounds waiting for /status (default 60s).
	StartTimeoutSec int `json:"startTimeoutSec,omitempty"`
}

func (o Options) normalized() Options {
	if o.RunnerBundleID == "" {
		o.RunnerBundleID = DefaultRunnerBundleID
	}
	if o.TestRunnerBundleID == "" {
		o.TestRunnerBundleID = o.RunnerBundleID
	}
	if o.XCTestConfig == "" {
		o.XCTestConfig = DefaultXCTestConfig
	}
	if o.WDAPort == 0 {
		o.WDAPort = defaultWDAPort
	}
	if o.MJPEGPort == 0 {
		o.MJPEGPort = defaultMJPEGPort
	}
	return o
}

// Status is a snapshot of one device's runner.
type Status struct {
	State          State  `json:"state"`
	WDAURL         string `json:"wdaUrl"`
	MJPEGURL       string `json:"mjpegUrl"`
	Error          string `json:"error"`
	Installed      bool   `json:"installed"`
	RunnerBundleID string `json:"runnerBundleId"`
	// Desired is true while the user wants WDA kept running (persisted).
	Desired bool `json:"desired"`
	// Restarts is the number of automatic start attempts since the last
	// healthy period (set by the Supervisor).
	Restarts int `json:"restarts"`
}

// Tunnel provides a device handle that go-ios services can use (on iOS 17+
// this carries the RSD address and userspace tunnel port).
type Tunnel interface {
	Ensure(ctx context.Context, udid string) (ios.DeviceEntry, error)
	Release(udid string) error
}

// Launcher runs the WDA runner and blocks until it exits or ctx is cancelled.
type Launcher interface {
	Run(ctx context.Context, dev ios.DeviceEntry, opts Options, logs io.Writer) error
}

// Forwarder exposes a device TCP port on a free 127.0.0.1 port.
type Forwarder interface {
	Forward(ctx context.Context, dev ios.DeviceEntry, devicePort int) (localPort int, closer io.Closer, err error)
}

// Installer reports whether a bundle id is installed.
type Installer interface {
	Installed(ctx context.Context, udid, bundleID string) (bool, error)
}

// Deps are the pluggable backends. Zero fields use the go-ios implementations.
type Deps struct {
	Tunnel    Tunnel
	Launcher  Launcher
	Forwarder Forwarder
	Installer Installer
	// Probe checks a WDA /status URL once and returns the
	// value.build.productBundleIdentifier it reports; nil means HTTP GET
	// expecting a 2xx JSON body.
	Probe func(ctx context.Context, url string) (bundleID string, err error)
	// Shutdown asks the WDA at wdaURL to exit (best effort); nil means
	// GET <wdaURL>/wda/shutdown. Without it the runner process can outlive
	// Stop on the device and squat on the port.
	Shutdown func(ctx context.Context, wdaURL string)
	// PollInterval between probes (default 500ms).
	PollInterval time.Duration
	// MismatchGrace is how long a /status answered by a different WDA is
	// tolerated before the port is declared taken (default 5s).
	MismatchGrace time.Duration
	// PreProbeWindow is how long the forwarded WDA port is probed before
	// launching; an answer means another WDA owns it (default 1s).
	PreProbeWindow time.Duration
}

type session struct {
	state     State
	opts      Options
	err       string
	wdaURL    string
	mjpegURL  string
	installed bool
	gen       int
	cancel    context.CancelFunc
	closers   []io.Closer
	done      chan struct{} // closed when the launcher goroutine returns
	logs      *ring
}

// Manager manages WDA runners keyed by udid.
type Manager struct {
	stateDir string
	deps     Deps

	mu       sync.Mutex
	sessions map[string]*session
	desired  *desiredStore
	restarts map[string]int
}

// NewManager returns a Manager using the real go-ios backends. stateDir holds
// per-device scratch data (test attachments).
func NewManager(stateDir string) *Manager {
	return NewManagerWithDeps(stateDir, Deps{})
}

// NewManagerWithDeps is NewManager with injectable backends (tests).
func NewManagerWithDeps(stateDir string, d Deps) *Manager {
	if d.Tunnel == nil {
		d.Tunnel = newGoIOSTunnel()
	}
	if d.Launcher == nil {
		d.Launcher = goiosLauncher{stateDir: stateDir}
	}
	if d.Forwarder == nil {
		d.Forwarder = goiosForwarder{}
	}
	if d.Installer == nil {
		d.Installer = goiosInstaller{}
	}
	if d.Probe == nil {
		d.Probe = httpProbe
	}
	if d.Shutdown == nil {
		d.Shutdown = httpShutdown
	}
	if d.MismatchGrace == 0 {
		d.MismatchGrace = 5 * time.Second
	}
	if d.PreProbeWindow == 0 {
		d.PreProbeWindow = time.Second
	}
	if d.PollInterval == 0 {
		d.PollInterval = 500 * time.Millisecond
	}
	return &Manager{stateDir: stateDir, deps: d, sessions: map[string]*session{}, desired: newDesiredStore(""), restarts: map[string]int{}}
}

func httpProbe(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var st struct {
		Value struct {
			Build struct {
				ProductBundleIdentifier string `json:"productBundleIdentifier"`
			} `json:"build"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return "", fmt.Errorf("bad /status body: %w", err)
	}
	return st.Value.Build.ProductBundleIdentifier, nil
}

// httpShutdown asks WDA to terminate itself so the runner does not linger on
// the device after we drop the testmanagerd session.
func httpShutdown(ctx context.Context, wdaURL string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(wdaURL, "/")+"/wda/shutdown", nil)
	if err != nil {
		return
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<12))
	resp.Body.Close()
}

const addrInUseMsg = "Address already in use"

type portPair struct{ wda, mjpeg int }

// portPairs are tried in order when no explicit ports were requested.
var portPairs = []portPair{{defaultWDAPort, defaultMJPEGPort}, {8120, 9120}, {8130, 9130}}

// watchWriter forwards to w and flags "Address already in use" lines.
type watchWriter struct {
	w    io.Writer
	busy atomic.Bool
	mu   sync.Mutex
	tail string
}

func (ww *watchWriter) Write(p []byte) (int, error) {
	ww.mu.Lock()
	buf := ww.tail + string(p)
	if strings.Contains(buf, addrInUseMsg) {
		ww.busy.Store(true)
	}
	if n := len(addrInUseMsg) - 1; len(buf) > n {
		buf = buf[len(buf)-n:]
	}
	ww.tail = buf
	ww.mu.Unlock()
	return ww.w.Write(p)
}

// libraryBundleID is the constant WebDriverAgentLib reports in /status as
// build.productBundleIdentifier regardless of the runner's own bundle id.
const libraryBundleID = "com.facebook.WebDriverAgentRunner"

// expectedBundles lists the bundle ids a WDA we launched may report.
func expectedBundles(o Options) map[string]bool {
	m := map[string]bool{libraryBundleID: true}
	for _, id := range []string{o.RunnerBundleID, o.TestRunnerBundleID} {
		m[id] = true
		m[strings.TrimSuffix(id, ".xctrunner")] = true
	}
	return m
}

// attempt launches the runner on opts' device ports, forwards them and waits
// until our own WDA answers /status. retry reports that the failure was a
// port conflict; on any failure everything the attempt started is torn down.
func (m *Manager) attempt(ctx, runCtx context.Context, udid string, gen int, s *session, dev ios.DeviceEntry, opts Options, logs *ring, timeout time.Duration) (urls [2]string, runErr chan error, wl *watchWriter, retry bool, err error) {
	attCtx, attCancel := context.WithCancel(runCtx)
	wl = &watchWriter{w: logs}
	done := make(chan struct{})
	runErr = make(chan error, 1)
	var closers []io.Closer
	var launched bool
	cleanup := func() {
		attCancel()
		for _, c := range closers {
			_ = c.Close()
		}
		if launched {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		}
		m.mu.Lock()
		if s.gen == gen {
			s.closers = nil
		}
		m.mu.Unlock()
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	for i, dp := range []int{opts.WDAPort, opts.MJPEGPort} {
		lp, c, ferr := m.deps.Forwarder.Forward(attCtx, dev, dp)
		if ferr != nil {
			return urls, runErr, wl, false, fmt.Errorf("forward device port %d: %w", dp, ferr)
		}
		closers = append(closers, c)
		m.mu.Lock()
		if s.gen == gen {
			s.closers = append(s.closers, c)
		} else {
			_ = c.Close()
		}
		m.mu.Unlock()
		urls[i] = fmt.Sprintf("http://127.0.0.1:%d", lp)
		logs.logf(fmt.Sprintf("forward device:%d -> %s", dp, urls[i]))
	}

	// Pre-launch probe: nothing of ours is running yet, so any /status answer
	// on this port comes from another WDA.
	preCtx, pcancel := context.WithTimeout(ctx, m.deps.PreProbeWindow)
	defer pcancel()
	for {
		if bundle, perr := m.deps.Probe(preCtx, urls[0]+"/status"); perr == nil {
			if bundle == "" {
				bundle = "unknown bundle"
			}
			logs.logf(fmt.Sprintf("device port %d already answers /status (%s); skipping", opts.WDAPort, bundle))
			return urls, runErr, wl, true, fmt.Errorf("port %d on device is used by another WebDriverAgent (%s)", opts.WDAPort, bundle)
		}
		select {
		case <-preCtx.Done():
		case <-time.After(m.deps.PollInterval):
			continue
		}
		break
	}
	if ctx.Err() != nil {
		return urls, runErr, wl, false, ctx.Err()
	}

	m.mu.Lock()
	s.done = done
	m.mu.Unlock()
	logs.logf(fmt.Sprintf("launching %s via testmanagerd (WDA port %d, MJPEG port %d)", opts.RunnerBundleID, opts.WDAPort, opts.MJPEGPort))
	launched = true
	go func() {
		defer close(done)
		runErr <- m.deps.Launcher.Run(attCtx, dev, opts, wl)
	}()

	busyErr := func(other string) error {
		if other != "" {
			return fmt.Errorf("port %d on device is used by another WebDriverAgent (%s)", opts.WDAPort, other)
		}
		return fmt.Errorf("port %d on device is already in use (%s in runner log)", opts.WDAPort, addrInUseMsg)
	}
	want := expectedBundles(opts)
	waitCtx, wcancel := context.WithTimeout(ctx, timeout)
	defer wcancel()
	tick := time.NewTicker(m.deps.PollInterval)
	defer tick.Stop()
	var mismatchSince time.Time
	var other string
	for {
		bundle, perr := m.deps.Probe(waitCtx, urls[0]+"/status")
		if perr == nil {
			if want[bundle] {
				// Identity verified; the runner must also still be alive.
				select {
				case e := <-runErr:
					if e == nil {
						e = errors.New("runner exited before WDA was ready")
					}
					return urls, runErr, wl, wl.busy.Load(), fmt.Errorf("runner exited: %w", e)
				default:
				}
				return urls, runErr, wl, false, nil
			}
			other = bundle
			if other == "" {
				other = "unknown bundle"
			}
			if mismatchSince.IsZero() {
				mismatchSince = time.Now()
				logs.logf("WDA at " + urls[0] + " reports bundle " + other + ", not ours; waiting")
			}
		}
		if wl.busy.Load() {
			return urls, runErr, wl, true, busyErr(other)
		}
		if !mismatchSince.IsZero() && time.Since(mismatchSince) >= m.deps.MismatchGrace {
			return urls, runErr, wl, true, busyErr(other)
		}
		select {
		case e := <-runErr:
			if e == nil {
				e = errors.New("runner exited before WDA answered")
			}
			if wl.busy.Load() {
				return urls, runErr, wl, true, fmt.Errorf("%w: runner exited: %v", busyErr(other), e)
			}
			return urls, runErr, wl, false, fmt.Errorf("runner exited: %w (check Developer Mode, the mounted Developer Disk Image and that the runner is trusted on the phone)", e)
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return urls, runErr, wl, false, ctx.Err()
			}
			if other != "" {
				return urls, runErr, wl, true, busyErr(other)
			}
			return urls, runErr, wl, false, fmt.Errorf("timed out after %s waiting for WDA /status at %s", timeout, urls[0])
		case <-tick.C:
		}
	}
}

func (m *Manager) get(udid string) *session {
	s := m.sessions[udid]
	if s == nil {
		s = &session{state: StateStopped, logs: newRing(logLines)}
		m.sessions[udid] = s
	}
	return s
}

func (s *session) status() Status {
	rb := s.opts.RunnerBundleID
	if rb == "" {
		rb = DefaultRunnerBundleID
	}
	return Status{State: s.state, WDAURL: s.wdaURL, MJPEGURL: s.mjpegURL, Error: s.err, Installed: s.installed, RunnerBundleID: rb}
}

// Status returns the cached status for udid (Installed is the last known value;
// use Refresh to re-query the device).
func (m *Manager) Status(udid string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked(udid)
}

func (m *Manager) statusLocked(udid string) Status {
	st := m.get(udid).status()
	st.Desired = m.desired.get(udid)
	st.Restarts = m.restarts[udid]
	return st
}

// setRestarts records the supervisor's attempt counter for status output.
func (m *Manager) setRestarts(udid string, n int) {
	m.mu.Lock()
	m.restarts[udid] = n
	m.mu.Unlock()
}

// markDead tears a running runner down after failed health checks and records
// msg as its error. It reports whether a running runner was torn down.
func (m *Manager) markDead(udid, msg string) bool {
	m.mu.Lock()
	s := m.get(udid)
	if s.state != StateRunning {
		m.mu.Unlock()
		return false
	}
	gen, logs := s.gen, s.logs
	m.mu.Unlock()
	logs.logf(msg)
	m.teardown(udid, gen, StateError, msg)
	return true
}

// annotate replaces the error text of a device in the error state.
func (m *Manager) annotate(udid, msg string) {
	m.mu.Lock()
	if s := m.get(udid); s.state == StateError {
		s.err = msg
	}
	m.mu.Unlock()
}

// ClearError drops a stale error after a successful helper install: a device
// in the error state goes back to stopped with no error text.
func (m *Manager) ClearError(udid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.get(udid)
	if s.state == StateError {
		s.state = StateStopped
	}
	s.err = ""
}

// Refresh re-queries the installed flag, then returns Status.
func (m *Manager) Refresh(ctx context.Context, udid string) Status {
	m.mu.Lock()
	bid := m.get(udid).status().RunnerBundleID
	m.mu.Unlock()
	ok, err := m.deps.Installer.Installed(ctx, udid, bid)
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.get(udid)
	if err == nil {
		s.installed = ok
	}
	return m.statusLocked(udid)
}

// Installed reports whether bundleID is installed on the device.
func (m *Manager) Installed(ctx context.Context, udid, bundleID string) bool {
	ok, err := m.deps.Installer.Installed(ctx, udid, bundleID)
	return err == nil && ok
}

// Logs returns the runner log ring buffer (oldest first).
func (m *Manager) Logs(udid string) []string {
	m.mu.Lock()
	r := m.get(udid).logs
	m.mu.Unlock()
	return r.snapshot()
}

// Start brings up tunnel, runner and forwards, and returns once WDA answers
// /status. The runner outlives ctx; use Stop to end it. Starting a running
// device is a no-op that returns its status.
func (m *Manager) Start(ctx context.Context, udid string, opts Options) (Status, error) {
	if udid == "" {
		return Status{}, errors.New("iosbridge: empty udid")
	}
	explicitPorts := opts.WDAPort != 0 || opts.MJPEGPort != 0
	opts = opts.normalized()
	timeout := defaultTimeout
	if opts.StartTimeoutSec > 0 {
		timeout = time.Duration(opts.StartTimeoutSec) * time.Second
	}

	m.mu.Lock()
	s := m.get(udid)
	switch s.state {
	case StateRunning:
		st := m.statusLocked(udid)
		m.mu.Unlock()
		return st, nil
	case StateStarting:
		m.mu.Unlock()
		return Status{}, ErrBusy
	}
	s.gen++
	gen := s.gen
	s.state, s.opts, s.err, s.wdaURL, s.mjpegURL = StateStarting, opts, "", "", ""
	s.logs.reset()
	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.closers = nil
	s.done = nil
	logs := s.logs
	m.mu.Unlock()

	fail := func(err error) (Status, error) {
		logs.logf("start failed: " + err.Error())
		m.teardown(udid, gen, StateError, err.Error())
		return m.Status(udid), err
	}

	// Installed check (best effort: a lookup failure does not block the start).
	logs.logf("checking runner " + opts.RunnerBundleID)
	if ok, err := m.deps.Installer.Installed(ctx, udid, opts.RunnerBundleID); err == nil {
		m.mu.Lock()
		if s.gen == gen {
			s.installed = ok
		}
		m.mu.Unlock()
		if !ok {
			return fail(fmt.Errorf("%w: build and install %s with your own signing identity", ErrNotInstalled, opts.RunnerBundleID))
		}
	}

	logs.logf("ensuring tunnel")
	dev, err := m.deps.Tunnel.Ensure(ctx, udid)
	if err != nil {
		return fail(fmt.Errorf("tunnel: %w", err))
	}

	pairs := portPairs
	if explicitPorts {
		pairs = []portPair{{opts.WDAPort, opts.MJPEGPort}}
	}
	var (
		urls   [2]string
		runErr chan error
		wlogs  *watchWriter
	)
	for i, pp := range pairs {
		opts.WDAPort, opts.MJPEGPort = pp.wda, pp.mjpeg
		m.mu.Lock()
		if s.gen == gen {
			s.opts = opts
		}
		m.mu.Unlock()
		last := i == len(pairs)-1
		var retry bool
		var aerr error
		urls, runErr, wlogs, retry, aerr = m.attempt(ctx, runCtx, udid, gen, s, dev, opts, logs, timeout)
		if aerr == nil {
			break
		}
		if retry && !last {
			logs.logf(fmt.Sprintf("device port %d is busy, retrying with %d/%d", pp.wda, pairs[i+1].wda, pairs[i+1].mjpeg))
			continue
		}
		return fail(aerr)
	}

	m.mu.Lock()
	if s.gen != gen || s.state != StateStarting { // stopped while starting
		m.mu.Unlock()
		return m.Status(udid), errors.New("iosbridge: stopped while starting")
	}
	s.state, s.wdaURL, s.mjpegURL = StateRunning, urls[0], urls[1]
	st := m.statusLocked(udid)
	m.mu.Unlock()
	logs.logf("WDA ready at " + urls[0])

	// Watch the runner: if it dies on its own, flip to error and clean up.
	go func() {
		e := <-runErr
		m.mu.Lock()
		cur := m.get(udid)
		alive := cur.gen == gen && cur.state == StateRunning
		m.mu.Unlock()
		if !alive {
			return
		}
		msg := "runner exited unexpectedly"
		if e != nil {
			msg += ": " + e.Error()
		}
		if wlogs.busy.Load() {
			msg += " (" + addrInUseMsg + " on device port " + fmt.Sprint(opts.WDAPort) + ")"
		}
		logs.logf(msg)
		m.teardown(udid, gen, StateError, msg)
	}()
	return st, nil
}

// teardown stops everything for generation gen and records the final state.
// It is a no-op if a newer generation has taken over.
func (m *Manager) teardown(udid string, gen int, final State, errMsg string) {
	m.mu.Lock()
	s := m.get(udid)
	if s.gen != gen {
		m.mu.Unlock()
		return
	}
	cancel, closers, done, wdaURL := s.cancel, s.closers, s.done, s.wdaURL
	s.cancel, s.closers = nil, nil
	s.state, s.err = final, errMsg
	s.wdaURL, s.mjpegURL = "", ""
	if final == StateStopped {
		s.gen++ // invalidate in-flight Start/watch goroutines
	}
	m.mu.Unlock()

	if wdaURL != "" {
		// Ask the runner to exit while the forward is still up.
		sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
		m.deps.Shutdown(sctx, wdaURL)
		scancel()
	}
	if cancel != nil {
		cancel()
	}
	for _, c := range closers {
		_ = c.Close()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	_ = m.deps.Tunnel.Release(udid)
}

// Stop ends the runner, forwards and tunnel for udid. Safe to call when stopped.
func (m *Manager) Stop(udid string) error {
	m.mu.Lock()
	s := m.get(udid)
	gen := s.gen
	was := s.state
	m.mu.Unlock()
	if was == StateStopped {
		return nil
	}
	s.logs.logf("stopping")
	m.teardown(udid, gen, StateStopped, "")
	return nil
}

// StopAll stops every device.
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); _ = m.Stop(id) }()
	}
	wg.Wait()
}
