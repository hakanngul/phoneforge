// Command phoneforged runs the PhoneForge headless server: the local REST API
// (/api/v1) and the embedded MCP server (/mcp) that an AI agent connects to.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"path/filepath"

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

func main() {
	cfg := config.Default()
	flag.StringVar(&cfg.Addr, "addr", cfg.Addr, "listen address")
	flag.StringVar(&cfg.APIToken, "token", "", "optional API token required for non-loopback requests")
	adbPath := flag.String("adb", "", "path to adb (default: adb in PATH)")
	webDir := flag.String("webdir", "frontend", "directory of the web UI to serve at /")
	flag.Parse()

	// State directory for stores (~/.phoneforge).
	stateDir := phoneforgeHome()
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

	reg := registry.New()
	bridges := uia2.NewManager(*adbPath, filepath.Join(stateDir, "uia2"))
	reg.AddDiscoverer(bridgeDiscoverer{inner: androiddrv.NewDiscoverer(*adbPath), mgr: bridges})
	iosMgr := iosbridge.NewManager(filepath.Join(stateDir, "iosbridge"))
	iosMgr.SetStatePath(filepath.Join(stateDir, "iosbridge-state.json"))
	iosCtl := httpapi.NewIOSControl(iosMgr)
	iosCtl.AV = avcapture.Default // macOS native USB capture for real iPhones; falls back to WDA
	iosSup := iosCtl.Supervise()
	// Wi-Fi paired iPhones flap in and out of usbmuxd; keep them listed for a grace period.
	reg.AddDiscoverer(iosWDADiscoverer{inner: registry.WithGrace(iosdrv.NewDiscoverer(), registry.DefaultGrace), ctl: iosCtl})
	simCtl := iossim.New("")
	iosCtl.SetSim(simCtl)
	reg.AddDiscoverer(iossim.NewDiscoverer(simCtl))
	simCtl.OnChange = func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = reg.Refresh(cctx)
	}
	if nodeStore != nil {
		reg.AddDiscoverer(remotenode.NewDiscoverer(nodeStore))
	}

	ctx := context.Background()
	go whMgr.Start(ctx)
	if err := reg.Refresh(ctx); err != nil {
		log.Printf("initial device scan: %v", err)
	}
	known := map[string]string{} // id -> name, for connect/disconnect events
	noteDevices(reg, known, false)
	iosSup.Seen(ctx, iosUDIDs(reg)) // auto-start WDA for devices the user left it on for
	go iosSup.Run(ctx)
	events.Info("PhoneForge started")

	// Background rescan every 5s so hot-plugged devices appear.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for range t.C {
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := reg.Refresh(cctx); err != nil {
				log.Printf("device rescan: %v", err)
			}
			cancel()
			noteDevices(reg, known, true)
			iosSup.Seen(context.Background(), iosUDIDs(reg))
		}
	}()

	getDevice := func(id string) (device.Device, error) { return reg.Get(id) }

	// Web (Chrome/WebView over CDP) automation: Android only, keyed by adb serial.
	webMgr := webauto.NewManager(*adbPath)
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

	deps := httpapi.Deps{
		Webhooks: whMgr.Routes(), Farms: farmMgr.Routes(), Bridges: bridges, IOS: iosCtl,
		AI:  aiconfig.New(filepath.Join(stateDir, "aiconfig.json"), "http://"+cfg.Addr+"/mcp").Routes(),
		OCR: ocr.Routes(getDevice),
		Web: webauto.NewRoutes(webMgr, getSerial), Sims: simCtl.Routes(),
	}
	dbgMgr := debugger.NewManager()
	deps.Debug = debugger.Routes(dbgMgr, getDevice)
	deps.Crashes = crashreport.Routes(&crashreport.Service{ADB: *adbPath}, getDevice)
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
	if _, err := os.Stat(*webDir); err == nil {
		mux.Handle("/", http.FileServer(http.Dir(*webDir)))
		log.Printf("serving web UI from %s", *webDir)
	} else {
		mux.Handle("/", router)
	}

	srv := &http.Server{Addr: cfg.Addr, Handler: mux}
	go func() {
		log.Printf("PhoneForge %s listening on http://%s  (REST /api/v1, MCP /mcp)", cfg.Version, cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	bridges.StopAll(sctx)
	iosMgr.StopAll()
	dbgMgr.CloseAll()
	simCtl.StopAll()
	webMgr.CloseAll(sctx)
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
		if d.Platform == device.PlatformIOS && d.Serial != "" && !strings.HasPrefix(d.ID, "remote-") {
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

// phoneforgeHome returns the state directory ($PHONEFORGE_HOME, else ~/.phoneforge, or ./.phoneforge).
func phoneforgeHome() string {
	if h := os.Getenv("PHONEFORGE_HOME"); h != "" {
		return h // lets a second instance (tests) keep its state apart
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".phoneforge")
	}
	return ".phoneforge"
}
