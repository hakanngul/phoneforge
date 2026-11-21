package uia2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	serverPkg        = "io.appium.uiautomator2.server"
	testPkg          = "io.appium.uiautomator2.server.test"
	instrumentRunner = testPkg + "/androidx.test.runner.AndroidJUnitRunner"
	devicePort       = 6790
	readyTimeout     = 30 * time.Second
	defaultADBCmd    = "adb"
)

// State is a snapshot of a bridge's status.
type State struct {
	Running   bool      `json:"running"`
	Port      int       `json:"port"`
	SessionID string    `json:"sessionId"`
	Since     time.Time `json:"since"`
	LastError string    `json:"lastError"`
}

// Proc is a long-running spawned process.
type Proc interface {
	Kill() error
	Done() <-chan struct{}
}

// Runner abstracts adb invocation so tests can inject a fake.
type Runner struct {
	// Run executes adb with args and returns stdout.
	Run func(ctx context.Context, args ...string) ([]byte, error)
	// Spawn starts adb with args as a long-running process.
	Spawn func(args ...string) (Proc, error)
}

// Bridge manages the UiAutomator2 server on one device.
type Bridge struct {
	serial string
	assets *Assets
	runner Runner
	http   *http.Client

	baseURLOverride string // tests: skip port allocation and use this URL

	mu      sync.Mutex
	running bool
	port    int
	baseURL string
	session string
	since   time.Time
	lastErr string
	proc    Proc
}

// New creates a bridge for the device with the given adb serial.
func New(adbPath, serial string, assets *Assets) *Bridge {
	if adbPath == "" {
		adbPath = defaultADBCmd
	}
	return &Bridge{
		serial: serial,
		assets: assets,
		runner: execRunner(adbPath, serial),
		http:   &http.Client{Timeout: 60 * time.Second},
	}
}

// Status returns the current state.
func (b *Bridge) Status() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return State{Running: b.running, Port: b.port, SessionID: b.session, Since: b.since, LastError: b.lastErr}
}

func (b *Bridge) endpoint() (base, session string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.running {
		return "", ""
	}
	return b.baseURL, b.session
}

func (b *Bridge) setError(err error) {
	b.mu.Lock()
	if err != nil {
		b.lastErr = err.Error()
	}
	b.mu.Unlock()
}

type cappedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (c *cappedBuf) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.b.Len() < 8192 {
		c.b.Write(p)
	}
	return len(p), nil
}
func (c *cappedBuf) String() string { c.mu.Lock(); defer c.mu.Unlock(); return c.b.String() }

type execProc struct {
	cmd  *exec.Cmd
	done chan struct{}
	out  *cappedBuf
}

func (p *execProc) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}
func (p *execProc) Done() <-chan struct{} { return p.done }

func execRunner(adbPath, serial string) Runner {
	full := func(args []string) []string { return append([]string{"-s", serial}, args...) }
	return Runner{
		Run: func(ctx context.Context, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, adbPath, full(args)...)
			var so, se bytes.Buffer
			cmd.Stdout, cmd.Stderr = &so, &se
			if err := cmd.Run(); err != nil {
				if ctx.Err() != nil {
					return nil, fmt.Errorf("adb %s: %w", strings.Join(args, " "), ctx.Err())
				}
				msg := strings.TrimSpace(se.String())
				if msg == "" {
					msg = strings.TrimSpace(so.String())
				}
				return nil, fmt.Errorf("adb %s: %w: %s", strings.Join(args, " "), err, msg)
			}
			return so.Bytes(), nil
		},
		Spawn: func(args ...string) (Proc, error) {
			cmd := exec.Command(adbPath, full(args)...)
			out := &cappedBuf{}
			cmd.Stdout, cmd.Stderr = out, out
			if err := cmd.Start(); err != nil {
				return nil, err
			}
			p := &execProc{cmd: cmd, done: make(chan struct{}), out: out}
			go func() { _ = cmd.Wait(); close(p.done) }()
			return p, nil
		},
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// ErrAssetsMissing is returned by Start when the UiAutomator2 packages are not
// installed on the device and no local APKs are available to install them.
var ErrAssetsMissing = errors.New("uia2: UiAutomator2 server packages are not installed on the device and the APKs are missing locally; " +
	"download them first (POST /api/v1/bridge/assets/download, or place the APKs in the assets dir)")

// Installed reports whether both UiAutomator2 packages are present on the
// device. It only runs a read-only `pm list packages`.
func (b *Bridge) Installed(ctx context.Context) (bool, error) {
	out, err := b.runner.Run(ctx, "shell", "pm", "list", "packages", "io.appium.uiautomator2")
	if err != nil {
		return false, err
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		have[strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "package:"))] = true
	}
	return have[serverPkg] && have[testPkg], nil
}

// Start installs (if needed), launches and connects to the UiAutomator2
// server. It is a no-op if already running.
func (b *Bridge) Start(ctx context.Context) error {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()

	err := b.start(ctx)
	if err != nil {
		b.setError(err)
	}
	return err
}

func (b *Bridge) start(ctx context.Context) error {
	// Packages already on the device are used as they are: no install and no
	// version comparison. Local assets are only needed when one is missing.
	installed, err := b.Installed(ctx)
	if err != nil {
		return fmt.Errorf("uia2: check packages: %w", err)
	}
	if !installed {
		serverAPK, testAPK, ok := b.assets.Status()
		if !ok {
			return ErrAssetsMissing
		}
		for _, apk := range []string{serverAPK, testAPK} {
			if _, err := b.runner.Run(ctx, "install", "-r", apk); err != nil {
				return fmt.Errorf("uia2: install: %w", err)
			}
		}
	}

	base := b.baseURLOverride
	port := 0
	forwarded := false
	if base == "" {
		p, err := freePort()
		if err != nil {
			return fmt.Errorf("uia2: free port: %w", err)
		}
		port = p
		if _, err := b.runner.Run(ctx, "forward", fmt.Sprintf("tcp:%d", port), fmt.Sprintf("tcp:%d", devicePort)); err != nil {
			return fmt.Errorf("uia2: forward: %w", err)
		}
		forwarded = true
		base = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	cleanup := func(proc Proc) {
		if proc != nil {
			_ = proc.Kill()
		}
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if forwarded {
			_, _ = b.runner.Run(cctx, "forward", "--remove", fmt.Sprintf("tcp:%d", port))
		}
	}

	proc, err := b.runner.Spawn("shell", "am", "instrument", "-w", "-e", "disableAnalytics", "true", instrumentRunner)
	if err != nil {
		cleanup(nil)
		return fmt.Errorf("uia2: launch instrumentation: %w", err)
	}

	if err := b.waitReady(ctx, base, proc); err != nil {
		cleanup(proc)
		return err
	}
	sid, err := b.createSession(ctx, base)
	if err != nil {
		cleanup(proc)
		return err
	}

	b.mu.Lock()
	b.running, b.port, b.baseURL, b.session, b.proc = true, port, base, sid, proc
	b.since = time.Now()
	b.lastErr = ""
	b.mu.Unlock()

	go func() { // detect the instrumentation dying underneath us
		<-proc.Done()
		b.mu.Lock()
		if b.proc == proc && b.running {
			b.running = false
			b.session = ""
			b.lastErr = "uia2: instrumentation process exited"
		}
		b.mu.Unlock()
	}()
	return nil
}

func (b *Bridge) waitReady(ctx context.Context, base string, proc Proc) error {
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		rctx, rc := context.WithTimeout(ctx, 3*time.Second)
		_, err := b.doRaw(rctx, http.MethodGet, base+"/status", nil)
		rc()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("uia2: server not ready within %s: %w", readyTimeout, err)
		case <-proc.Done():
			return errors.New("uia2: instrumentation exited before server became ready")
		case <-t.C:
		}
	}
}

func (b *Bridge) createSession(ctx context.Context, base string) (string, error) {
	v, err := b.doRaw(ctx, http.MethodPost, base+"/session", map[string]any{
		"capabilities": map[string]any{"alwaysMatch": map[string]any{}, "firstMatch": []any{map[string]any{}}},
	})
	if err != nil {
		return "", fmt.Errorf("uia2: create session: %w", err)
	}
	var r struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(v, &r); err != nil || r.SessionID == "" {
		return "", fmt.Errorf("uia2: create session: no sessionId in response")
	}
	return r.SessionID, nil
}

// Stop deletes the session, kills the instrumentation and removes the port
// forward. The APKs stay installed.
func (b *Bridge) Stop(ctx context.Context) error {
	b.mu.Lock()
	base, sid, proc, port := b.baseURL, b.session, b.proc, b.port
	wasRunning := b.running
	b.running, b.session, b.proc, b.port, b.baseURL = false, "", nil, 0, ""
	b.mu.Unlock()
	if !wasRunning && proc == nil {
		return nil
	}
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	if sid != "" {
		dctx, c := context.WithTimeout(ctx, 5*time.Second)
		_, err := b.doRaw(dctx, http.MethodDelete, base+"/session/"+sid, nil)
		c()
		keep(err)
	}
	if proc != nil {
		keep(proc.Kill())
		_, _ = b.runner.Run(ctx, "shell", "am", "force-stop", serverPkg)
		_, _ = b.runner.Run(ctx, "shell", "am", "force-stop", testPkg)
	}
	if port != 0 {
		_, err := b.runner.Run(ctx, "forward", "--remove", fmt.Sprintf("tcp:%d", port))
		keep(err)
	}
	return first
}
