// Package server wires and runs the PhoneForge HTTP server (REST /api/v1,
// MCP /mcp and the web UI). It is shared by cmd/phoneforged and the desktop app.
package server

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"phoneforge/internal/aiconfig"
	"phoneforge/internal/androiddrv"
	"phoneforge/internal/avcapture"
	"phoneforge/internal/config"
	"phoneforge/internal/crashreport"
	"phoneforge/internal/credentials"
	"phoneforge/internal/debugger"
	"phoneforge/internal/device"
	"phoneforge/internal/dsl"
	"phoneforge/internal/events"
	"phoneforge/internal/farms"
	"phoneforge/internal/httpapi"
	"phoneforge/internal/iosbridge"
	"phoneforge/internal/iosdrv"
	"phoneforge/internal/iossim"
	"phoneforge/internal/mcpserver"
	"phoneforge/internal/mobtest"
	"phoneforge/internal/ocr"
	"phoneforge/internal/registry"
	"phoneforge/internal/remotenode"
	"phoneforge/internal/uia2"
	"phoneforge/internal/webauto"
	"phoneforge/internal/webhooks"
)

// providerKeys are secret names that, when stored via AI Tools, are loaded
// into the environment at boot so the agent's LLM client can use them.
var providerKeys = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "OLLAMA_HOST", "OLLAMA_MODEL", "OLLAMA_VISION"}

// Options configures Start.
type Options struct {
	Addr     string // listen address; default config.Default().Addr
	APIToken string // optional API token required for non-loopback requests
	ADBPath  string // path to adb (default: adb in PATH)
	// WebDir, when non-empty and existing, is served at / (development override).
	WebDir string
	// WebFS is served at / when WebDir is empty or missing (e.g. frontend.FS()).
	WebFS fs.FS
	// StateDir overrides the state directory ($PHONEFORGE_HOME, else ~/.phoneforge).
	StateDir string
}

// Server is a running PhoneForge server.
type Server struct {
	addr     string
	srv      *http.Server
	cancel   context.CancelFunc
	done     chan struct{}
	bridges  *uia2.Manager
	iosMgr   *iosbridge.Manager
	dbgMgr   *debugger.Manager
	simCtl   *iossim.Control
	webMgr   *webauto.Manager
	once     sync.Once
	shutdown error
	stopReq  chan struct{}
}

// ShutdownRequested is closed when a client asked the server to stop via
// POST /api/v1/server/shutdown. The owner should then call Shutdown and exit.
func (s *Server) ShutdownRequested() <-chan struct{} { return s.stopReq }

// Addr returns the actual listen address (host:port).
func (s *Server) Addr() string { return s.addr }

// Done is closed when the HTTP server has stopped serving.
func (s *Server) Done() <-chan struct{} { return s.done }

// Start wires all subsystems, binds the listener (returning an error if the
// address is unavailable) and serves in the background until Shutdown.
func Start(ctx context.Context, opts Options) (*Server, error) {
	cfg := config.Default()
	if opts.Addr != "" {
		cfg.Addr = opts.Addr
	}
	cfg.APIToken = opts.APIToken
	adbPath := opts.ADBPath

	// Bind first so a busy port fails before any subsystem is started.
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, err
	}
	cfg.Addr = ln.Addr().String()

	stateDir := opts.StateDir
	if stateDir == "" {
		stateDir = PhoneforgeHome()
	}
	_ = os.MkdirAll(stateDir, 0o700)

	// Secret store (Keychain-backed) + app cards, and load provider keys into env.
	sec, err := credentials.New(filepath.Join(stateDir, "secret-index.json"))
	if err != nil {
		log.Printf("secret store: %v", err)
	}
	cards, err := credentials.NewCardStore(filepath.Join(stateDir, "app-cards.json"), sec)
	if err != nil {
		log.Printf("app-card store: %v", err)
	}
	if sec != nil {
		for _, k := range providerKeys {
			if os.Getenv(k) == "" && sec.Has(k) {
				if v, rerr := sec.Reveal(k); rerr == nil && v != "" {
					_ = os.Setenv(k, v)
				}
			}
		}
	}

	// Remote nodes (add-by-address) + webhooks + farms.
	nodeStore, err := remotenode.New(filepath.Join(stateDir, "nodes.json"))
	if err != nil {
		log.Printf("node store: %v", err)
	}
	whStore, err := webhooks.New(filepath.Join(stateDir, "webhooks.json"))
	if err != nil {
		log.Printf("webhook store: %v", err)
	}
	whMgr := webhooks.NewManager(whStore)
	farmMgr := farms.NewManager(sec, filepath.Join(stateDir, "farms.json"))

	bgCtx, cancel := context.WithCancel(context.Background())
	go func() { // honour cancellation of the caller's ctx too
		select {
		case <-ctx.Done():
			cancel()
		case <-bgCtx.Done():
		}
	}()

	reg := registry.New()
	bridges := uia2.NewManager(adbPath, filepath.Join(stateDir, "uia2"))
	reg.AddDiscoverer(bridgeDiscoverer{inner: androiddrv.NewDiscoverer(adbPath), mgr: bridges})
	iosMgr := iosbridge.NewManager(filepath.Join(stateDir, "iosbridge"))
	iosMgr.SetStatePath(filepath.Join(stateDir, "iosbridge-state.json"))
	iosCtl := httpapi.NewIOSControl(iosMgr)
	iosCtl.AV = avcapture.Default // macOS native USB capture for real iPhones; falls back to WDA
	iosSup := iosCtl.Supervise()
	// Wi-Fi paired iPhones flap in and out of usbmuxd; keep them listed for a grace period.
	reg.AddDiscoverer(iosWDADiscoverer{inner: registry.WithGrace(iosdrv.NewDiscoverer(), registry.DefaultGrace), ctl: iosCtl})
	simCtl := iossim.New("")
	simCtl.SetStatePath(filepath.Join(stateDir, "iossim-state.json"))
	simSup := simCtl.Supervise(events.Info)
	iosCtl.SetSim(simCtl)
	reg.AddDiscoverer(iossim.NewDiscoverer(simCtl))
	simCtl.OnChange = func() {
		cctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = reg.Refresh(cctx)
	}
	if nodeStore != nil {
		reg.AddDiscoverer(remotenode.NewDiscoverer(nodeStore))
	}

	go whMgr.Start(bgCtx)
	if err := reg.Refresh(bgCtx); err != nil {
		log.Printf("initial device scan: %v", err)
	}
	known := map[string]string{} // id -> name, for connect/disconnect events
	noteDevices(reg, known, false)
	iosSup.Seen(bgCtx, iosUDIDs(reg)) // auto-start WDA for devices the user left it on for
	go iosSup.Run(bgCtx)
	simSup.Seen(bgCtx, simUDIDs(reg)) // restore WDA on booted simulators the user left it on for
	go simSup.Run(bgCtx)
	events.Info("PhoneForge started")

	// Background rescan every 5s so hot-plugged devices appear.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-bgCtx.Done():
				return
			case <-t.C:
			}
			cctx, c := context.WithTimeout(bgCtx, 10*time.Second)
			if err := reg.Refresh(cctx); err != nil && bgCtx.Err() == nil {
				log.Printf("device rescan: %v", err)
			}
			c()
			noteDevices(reg, known, true)
			iosSup.Seen(bgCtx, iosUDIDs(reg))
			simSup.Seen(bgCtx, simUDIDs(reg))
		}
	}()

	getDevice := func(id string) (device.Device, error) { return reg.Get(id) }

	// Web (Chrome/WebView over CDP) automation: Android only, keyed by adb serial.
	webMgr := webauto.NewManager(adbPath)
	dsl.SetWebHost(dsl.NewWebHost(webMgr))
	getSerial := func(id string) (string, error) {
		d, err := reg.Get(id)
		if err != nil {
			return "", err
		}
		info := d.Info()
		if info.Platform != device.PlatformAndroid || info.Serial == "" {
			return "", fmt.Errorf("web automation needs an Android device: %w", device.ErrNotFound)
		}
		return info.Serial, nil
	}

	stopReq := make(chan struct{})
	var stopOnce sync.Once
	deps := httpapi.Deps{
		Shutdown: func() { stopOnce.Do(func() { close(stopReq) }) },
		Webhooks: whMgr.Routes(), Farms: farmMgr.Routes(), Bridges: bridges, IOS: iosCtl,
		AI:  aiconfig.New(filepath.Join(stateDir, "aiconfig.json"), "http://"+cfg.Addr+"/mcp").Routes(),
		OCR: ocr.Routes(getDevice),
		Web: webauto.NewRoutes(webMgr, getSerial), Sims: simCtl.Routes(),
	}
	dbgMgr := debugger.NewManager()
	deps.Debug = debugger.Routes(dbgMgr, getDevice)
	deps.Crashes = crashreport.Routes(&crashreport.Service{ADB: adbPath}, getDevice)
	go warmOCR()
	if root, rerr := mobtest.DefaultRoot(); rerr != nil {
		log.Printf("tests: %v", rerr)
	} else if tests, terr := mobtest.NewService(root); terr != nil {
		log.Printf("tests: %v", terr)
	} else {
		if sec != nil {
			tests.Secrets = sec.Reveal
		}
		deps.Tests = tests.Routes(getDevice)
	}
	if nodeStore != nil {
		deps.Nodes = nodeStore.Routes()
	}
	if sec != nil && cards != nil {
		deps.Credentials = credentials.Routes(sec, cards)
	}
	router := httpapi.NewRouterWithDeps(reg, cfg, deps)

	// Mount the MCP streamable-HTTP server at /mcp; it proxies to our own API.
	mcp := mcpserver.New("http://"+cfg.Addr+"/api/v1", cfg.Version)
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.Handler(mcp))
	mux.Handle("/mcp/", mcpserver.Handler(mcp))
	mux.Handle("/api/", router)
	switch {
	case opts.WebDir != "" && dirExists(opts.WebDir):
		mux.Handle("/", http.FileServer(http.Dir(opts.WebDir)))
		log.Printf("serving web UI from %s", opts.WebDir)
	case opts.WebFS != nil:
		mux.Handle("/", http.FileServer(http.FS(opts.WebFS)))
		log.Printf("serving embedded web UI")
	default:
		mux.Handle("/", router)
	}

	s := &Server{
		addr: cfg.Addr, srv: &http.Server{Handler: mux}, cancel: cancel, done: make(chan struct{}),
		stopReq: stopReq, bridges: bridges, iosMgr: iosMgr, dbgMgr: dbgMgr, simCtl: simCtl, webMgr: webMgr,
	}
	go func() {
		defer close(s.done)
		log.Printf("PhoneForge %s listening on http://%s  (REST /api/v1, MCP /mcp)", cfg.Version, cfg.Addr)
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("server: %v", err)
		}
	}()
	return s, nil
}

// Shutdown stops the HTTP server and all bridges/sessions. Safe to call twice.
func (s *Server) Shutdown(ctx context.Context) error {
	s.once.Do(func() {
		s.cancel()
		s.shutdown = s.srv.Shutdown(ctx)
		s.bridges.StopAll(ctx)
		s.iosMgr.StopAll()
		s.dbgMgr.CloseAll()
		s.simCtl.StopAll()
		s.webMgr.CloseAll(ctx)
	})
	return s.shutdown
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// warmOCR compiles/loads the OCR helper and runs one throwaway recognition so
// the first real request is fast. Failures are logged, never fatal.
func warmOCR() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := ocr.EnsureHelper(ctx); err != nil {
		log.Printf("ocr warm-up: helper: %v", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 32, 32))); err != nil {
		return
	}
	if _, err := ocr.Recognize(ctx, buf.Bytes()); err != nil {
		log.Printf("ocr warm-up: recognize: %v", err)
	}
}

// bridgeDiscoverer wraps every Android device from the inner discoverer so
// that, once the user starts the UiAutomator2 bridge, UI tree and gestures go
// through it. Behaviour is unchanged while the bridge is stopped.
type bridgeDiscoverer struct {
	inner registry.Discoverer
	mgr   *uia2.Manager
}

func (d bridgeDiscoverer) Discover(ctx context.Context) ([]device.Device, error) {
	devs, err := d.inner.Discover(ctx)
	if err != nil {
		return nil, err
	}
	for i, dev := range devs {
		if info := dev.Info(); info.Platform == device.PlatformAndroid && info.Serial != "" {
			devs[i] = uia2.Wrap(dev, d.mgr.For(info.Serial))
		}
	}
	return devs, nil
}

// iosWDADiscoverer wraps every iOS device from the inner discoverer so that,
// once the user starts the WebDriverAgent bridge, screenshots, UI tree, input
// and app launch go through WDA. Behaviour is unchanged while it is stopped.
type iosWDADiscoverer struct {
	inner registry.Discoverer
	ctl   *httpapi.IOSControl
}

func (d iosWDADiscoverer) Discover(ctx context.Context) ([]device.Device, error) {
	devs, err := d.inner.Discover(ctx)
	if err != nil {
		return nil, err
	}
	for i, dev := range devs {
		devs[i] = d.ctl.Wrap(dev)
	}
	return devs, nil
}

// iosUDIDs lists the udids of the currently known local iOS devices.
func iosUDIDs(reg *registry.Registry) []string {
	var out []string
	for _, d := range reg.List() {
		if d.Platform == device.PlatformIOS && d.Serial != "" && !strings.HasPrefix(d.ID, "remote-") && !d.Simulator {
			out = append(out, d.Serial)
		}
	}
	return out
}

// simUDIDs lists the udids of the currently booted iOS simulators.
func simUDIDs(reg *registry.Registry) []string {
	var out []string
	for _, d := range reg.List() {
		if d.Platform == device.PlatformIOS && d.Serial != "" && d.Simulator {
			out = append(out, d.Serial)
		}
	}
	return out
}

// noteDevices publishes activity events when devices appear or disappear.
// On the first call (emit=false) it only records the baseline.
func noteDevices(reg *registry.Registry, known map[string]string, emit bool) {
	cur := map[string]string{}
	for _, d := range reg.List() {
		cur[d.ID] = d.Name
	}
	for id, name := range cur {
		if _, ok := known[id]; !ok {
			known[id] = name
			if emit {
				events.Success("Device connected: " + name)
			}
		}
	}
	for id, name := range known {
		if _, ok := cur[id]; !ok {
			delete(known, id)
			if emit {
				events.Info("Device disconnected: " + name)
			}
		}
	}
}

// PhoneforgeHome returns the state directory ($PHONEFORGE_HOME, else ~/.phoneforge, or ./.phoneforge).
func PhoneforgeHome() string {
	if h := os.Getenv("PHONEFORGE_HOME"); h != "" {
		return h // lets a second instance (tests) keep its state apart
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".phoneforge")
	}
	return ".phoneforge"
}
