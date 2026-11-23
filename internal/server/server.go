// Package server wires and runs the Pocketlab HTTP server (REST /api/v1,
// MCP /mcp and the web UI). It is shared by cmd/pocketlabd and the desktop app.
package server

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"pocketlab/internal/aiconfig"
	"pocketlab/internal/androiddrv"
	"pocketlab/internal/avcapture"
	"pocketlab/internal/brand"
	"pocketlab/internal/config"
	"pocketlab/internal/crashreport"
	"pocketlab/internal/credentials"
	"pocketlab/internal/debugger"
	"pocketlab/internal/device"
	"pocketlab/internal/dsl"
	"pocketlab/internal/events"
	"pocketlab/internal/farms"
	"pocketlab/internal/httpapi"
	"pocketlab/internal/iosbridge"
	"pocketlab/internal/iosdrv"
	"pocketlab/internal/iossim"
	"pocketlab/internal/lifecycle"
	"pocketlab/internal/mcpserver"
	"pocketlab/internal/mobflow"
	"pocketlab/internal/mobtest"
	"pocketlab/internal/ocr"
	"pocketlab/internal/registry"
	"pocketlab/internal/remotenode"
	"pocketlab/internal/uia2"
	"pocketlab/internal/webauto"
	"pocketlab/internal/webhooks"
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
	// StateDir overrides the state directory ($POCKETLAB_HOME, else ~/.pocketlab).
	StateDir string
	// StartedVia and Owner describe who launched this server, for the lifecycle
	// history ("pocketlabd"/"desktop"/"pocketlab server start"; owner "cli"/"desktop").
	StartedVia string
	Owner      string
}

// Server is a running Pocketlab server.
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
	life     *lifecycle.Recorder
}

// logTail keeps the last log lines so crashes and unclean exits can show them.
var (
	logTail     = lifecycle.NewTailWriter(60)
	logTailOnce sync.Once
)

// Lifecycle returns the start/stop recorder of this server.
func (s *Server) Lifecycle() *lifecycle.Recorder { return s.life }

// SetStopReason records why the server is about to stop (first call wins).
// origin names the actor (e.g. "desktop", "signal:SIGTERM").
func (s *Server) SetStopReason(reason, origin string) { s.life.SetStopReason(reason, origin, "") }

// RecordCrash logs a fatal error/panic for this server and marks the run as crashed.
func (s *Server) RecordCrash(msg string) { s.life.Crash(msg) }

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
	logTailOnce.Do(func() { log.SetOutput(io.MultiWriter(log.Writer(), logTail)) })
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		dir := opts.StateDir
		if dir == "" {
			dir = PocketlabHome()
		}
		lifecycle.RecordListenFailure(dir, lifecycle.Info{Version: cfg.Version, Addr: cfg.Addr, StartedVia: opts.StartedVia, Owner: opts.Owner}, err)
		return nil, err
	}
	cfg.Addr = ln.Addr().String()

	stateDir := opts.StateDir
	if stateDir == "" {
		stateDir = PocketlabHome()
	}
	_ = os.MkdirAll(stateDir, 0o700)
	life := lifecycle.Begin(stateDir, lifecycle.Info{
		Version: cfg.Version, Addr: cfg.Addr, StartedVia: opts.StartedVia, Owner: opts.Owner, LogTail: logTail.Lines,
	})

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
	// Wi-Fi paired iPhones flap in and out of usbmuxd; keep only those listed for a grace period (USB unplug is immediate).
	reg.AddDiscoverer(iosWDADiscoverer{inner: registry.WithGraceIf(iosdrv.NewDiscoverer(), registry.DefaultGrace, iosdrv.IsNetworkDevice), ctl: iosCtl})
	simCtl := iossim.New("")
	simCtl.SetStatePath(filepath.Join(stateDir, "iossim-state.json"))
	simCtl.SetSlimPath(filepath.Join(stateDir, "iossim-slim.json"))
	simSup := simCtl.Supervise(events.Info)
	iosCtl.SetSim(simCtl)
	if runtime.GOOS == "darwin" { // simulators need xcrun simctl
		reg.AddDiscoverer(iossim.NewDiscoverer(simCtl))
	}
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
	iosSup.Seen(bgCtx, iosUDIDs(reg)) // auto-start WDA for devices the user left it on for
	go iosSup.Run(bgCtx)
	simSup.Seen(bgCtx, simUDIDs(reg)) // restore WDA on booted simulators the user left it on for
	if runtime.GOOS == "darwin" {
		go simSup.Run(bgCtx)
	}
	events.Info("Pocketlab started")

	// Hot-plug: usbmuxd Listen events kick an immediate rescan; a periodic
	// rescan stays on as the safety net (Android, listener down, Windows/Linux).
	kick := make(chan struct{}, 1)
	go iosdrv.Watch(bgCtx, iosdrv.UsbmuxdListen, func(iosdrv.HotplugEvent) {
		select {
		case kick <- struct{}{}:
		default:
		}
	}, iosdrv.WatchOptions{Logf: log.Printf})
	loop := newDeviceLoop(reg, kick)
	loop.Baseline()
	loop.OnRemoved = func(d device.Info) {
		// Tear down the WDA bridge/forwarders of an unplugged iPhone. "Desired"
		// is kept, so the supervisor restarts WDA when it is plugged back in.
		if d.Platform == device.PlatformIOS && !d.Simulator && d.Serial != "" && !strings.HasPrefix(d.ID, "remote-") {
			go func() {
				if err := iosMgr.Stop(d.Serial); err != nil {
					log.Printf("stop WDA bridge for %s: %v", d.Serial, err)
				}
			}()
		}
	}
	loop.AfterScan = func([]device.Info) {
		iosSup.Seen(bgCtx, iosUDIDs(reg))
		simSup.Seen(bgCtx, simUDIDs(reg))
	}
	go loop.Run(bgCtx)

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
		Lifecycle: life,
		Shutdown:  func() { stopOnce.Do(func() { close(stopReq) }) },
		Webhooks:  whMgr.Routes(), Farms: farmMgr.Routes(), Bridges: bridges, IOS: iosCtl,
		AI:  aiconfig.New(filepath.Join(stateDir, "aiconfig.json"), "http://"+cfg.Addr+"/mcp").Routes(),
		OCR: ocr.Routes(getDevice),
		Web: webauto.NewRoutes(webMgr, getSerial), Sims: simCtl.Routes(),
		WDABuild: httpapi.WDABuildRoutes(iosbridge.NewBuilder(filepath.Join(stateDir, config.UserSettingsFile))),
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
		if flows, ferr := mobflow.NewService(root); ferr != nil {
			log.Printf("flows: %v", ferr)
		} else {
			flows.Devices = getDevice
			deps.Flows = flows.Routes()
		}
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
		mux.Handle("/", noCache(http.FileServer(http.Dir(opts.WebDir))))
		log.Printf("serving web UI from %s", opts.WebDir)
	case opts.WebFS != nil:
		mux.Handle("/", noCache(http.FileServer(http.FS(opts.WebFS))))
		log.Printf("serving embedded web UI")
	default:
		mux.Handle("/", router)
	}

	s := &Server{
		addr: cfg.Addr, srv: &http.Server{Handler: mux}, cancel: cancel, done: make(chan struct{}),
		stopReq: stopReq, life: life, bridges: bridges, iosMgr: iosMgr, dbgMgr: dbgMgr, simCtl: simCtl, webMgr: webMgr,
	}
	go func() {
		defer close(s.done)
		log.Printf("Pocketlab %s listening on http://%s  (REST /api/v1, MCP /mcp)", cfg.Version, cfg.Addr)
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("server: %v", err)
			s.life.Crash("http server stopped serving: " + err.Error())
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
		s.life.Stop("server stopped (no reason recorded)")
	})
	return s.shutdown
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// warmOCR compiles/loads the OCR helper and reads a text image once so Vision's
// models are compiled before the first real request. Failures are logged,
// never fatal.
func warmOCR() {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	if err := ocr.Warm(ctx); err != nil {
		log.Printf("ocr warm-up: %v", err)
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

// PocketlabHome returns the state directory ($POCKETLAB_HOME, else ~/.pocketlab, or ./.pocketlab).
func PocketlabHome() string {
	return brand.Home()
}

// noCache makes browsers revalidate static frontend assets on every load
// (Last-Modified/ETag 304s keep that cheap) so updated JS/CSS is never stale.
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}
