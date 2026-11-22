// Command pocketlab-desktop is the native macOS Pocketlab app: it runs the
// same server as pocketlabd in-process (or attaches to one that is already
// running) and shows the web UI in a native Wails window.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"pocketlab/frontend"
	"pocketlab/internal/brand"
	"pocketlab/internal/config"
	"pocketlab/internal/server"
	"pocketlab/internal/version"
)

const (
	repoURL = "https://github.com/hakanngul/pocketlab"
)

// devBuild is set to a non-empty value via -ldflags "-X main.devBuild=1" to
// enable the Toggle DevTools menu item and the web inspector.
var devBuild string

//go:embed assets/icon.png
var appIcon []byte

//go:embed assets/tray.png
var trayIcon []byte

func main() {
	brand.DataDir() // migrate ~/.phoneforge -> ~/.pocketlab (logged once) before anything reads the state dir
	def := config.Default().Addr
	if v := brand.Getenv("POCKETLAB_ADDR"); v != "" {
		def = v
	}
	addr := flag.String("addr", def, "server address to listen on / attach to (env POCKETLAB_ADDR)")
	webDir := flag.String("webdir", "", "serve the web UI from this directory instead of the embedded copy (development)")
	adb := flag.String("adb", "", "path to adb (default: adb in PATH)")
	token := flag.String("token", "", "optional API token required for non-loopback requests")
	noTray := flag.Bool("no-tray", false, "disable the menu-bar icon (closing the window then quits)")
	flag.Parse()
	dev := devBuild != "" || brand.Getenv("POCKETLAB_DEV") == "1"

	// 1. Attach to a Pocketlab server that already owns the address, or start our own.
	var (
		srv      *server.Server
		uiAddr   = *addr
		attached bool
		ctl      *serverCtl
	)
	opts := server.Options{Addr: *addr, APIToken: *token, ADBPath: *adb, WebDir: *webDir, WebFS: frontend.FS(), StartedVia: "desktop", Owner: "desktop"}
	if isPocketlab(*addr) {
		attached = true
		log.Printf("attaching to the running Pocketlab server at %s", uiAddr)
	} else {
		var err error
		srv, err = server.Start(context.Background(), opts)
		if err != nil && portBusy(*addr) {
			log.Printf("%s is in use by another program, picking a free port", *addr)
			host, _, _ := net.SplitHostPort(*addr)
			if host == "" {
				host = "127.0.0.1"
			}
			opts.Addr = net.JoinHostPort(host, "0")
			srv, err = server.Start(context.Background(), opts)
		}
		if err != nil {
			log.Fatalf("server: %v", err)
		}
		uiAddr = srv.Addr()
	}
	baseURL := "http://" + uiAddr + "/"

	title := "Pocketlab"
	if attached {
		title = "Pocketlab — connected to running server"
	}

	// 2. Native app + window.
	st := loadState()
	app := application.New(application.Options{
		Name:        "Pocketlab",
		Description: "Pocketlab " + version.Version + ". Control Android and iOS devices from your Mac or from an AI agent.",
		Icon:        appIcon,
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: *noTray,
		},
		OnShutdown: func() {
			st.save()
			if ctl != nil {
				ctl.shutdownOwned("desktop app quit")
			}
		},
	})

	wopts := application.WebviewWindowOptions{
		Name:             "main",
		Title:            title,
		Width:            1400,
		Height:           900,
		MinWidth:         900,
		MinHeight:        600,
		URL:              baseURL,
		DevToolsEnabled:  dev,
		BackgroundColour: application.NewRGB(14, 13, 20),
	}
	if st.valid(app) {
		wopts.Width, wopts.Height = st.Width, st.Height
		wopts.InitialPosition = application.WindowXY
		wopts.X, wopts.Y = st.X, st.Y
	}
	win := app.Window.NewWithOptions(wopts)

	// Persist size/position (debounced).
	var mu sync.Mutex
	var timer *time.Timer
	remember := func() {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(500*time.Millisecond, func() { st.capture(win); st.save() })
	}
	win.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) { remember() })
	win.OnWindowEvent(events.Common.WindowDidMove, func(*application.WindowEvent) { remember() })

	if !*noTray {
		// Closing the window hides it; the app keeps running in the menu bar.
		win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
			st.capture(win)
			st.save()
			win.Hide()
			e.Cancel()
		})
		app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) {
			win.Show()
			win.Focus()
		})
	}

	// Server lifecycle controller: start/stop/restart, fallback page when the server vanishes.
	ctl = newServerCtl(app, opts, uiAddr)
	ctl.win = win
	ctl.adopt(srv, uiAddr, attached)
	if err := ctl.startControl(); err != nil {
		log.Printf("fallback control server: %v", err)
	}
	// Tell the web UI it runs inside the desktop app (recorded as the origin of a Quit, banner wording).
	win.OnWindowEvent(events.Mac.WebViewDidFinishNavigation, func(*application.WindowEvent) {
		win.ExecJS(`window.PL_ORIGIN='desktop-ui'`)
	})
	defer func() {
		if r := recover(); r != nil {
			ctl.mu.Lock()
			s := ctl.srv
			ctl.mu.Unlock()
			if s != nil {
				s.RecordCrash(fmt.Sprint(r))
			}
			panic(r)
		}
	}()

	// Toast after first load when attached to an existing server.
	if attached {
		var once sync.Once
		win.OnWindowEvent(events.Mac.WebViewDidFinishNavigation, func(*application.WindowEvent) {
			once.Do(func() {
				time.AfterFunc(800*time.Millisecond, func() {
					win.ExecJS(`window.PF&&PF.toast&&PF.toast('Connected to running server (` + uiAddr + `)',true)`)
				})
			})
		})
	}

	showWindow := func() { win.Show(); win.Focus() }
	openSettings := func() {
		showWindow()
		win.ExecJS(`(function(){var b=document.querySelector('[data-int="aiconfig"]');if(b)b.click();})()`)
	}
	buildMenu(app, win, ctl, dev, openSettings)

	if !*noTray {
		buildTray(app, ctl, showWindow)
	}
	go ctl.monitor()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// serverMenuItems adds Start/Stop/Restart server entries to a menu. Actions run
// in the background and report failures on the fallback page.
func serverMenuItems(m *application.Menu, ctl *serverCtl) {
	run := func(fn func() error) func(*application.Context) {
		return func(*application.Context) {
			go func() {
				if err := fn(); err != nil {
					log.Printf("server action: %v", err)
					ctl.showFallback(err.Error())
				}
			}()
		}
	}
	m.Add("Start server").OnClick(run(ctl.Start))
	m.Add("Stop server").OnClick(run(ctl.Stop))
	m.Add("Restart server").OnClick(run(ctl.Restart))
}

func buildMenu(app *application.App, win *application.WebviewWindow, ctl *serverCtl, dev bool, openSettings func()) {
	menu := app.NewMenu()

	appMenu := menu.AddSubmenu("Pocketlab")
	appMenu.AddRole(application.About)
	appMenu.AddSeparator()
	appMenu.Add("Settings…").SetAccelerator("CmdOrCtrl+,").OnClick(func(*application.Context) { openSettings() })
	appMenu.AddSeparator()
	appMenu.AddRole(application.Hide)
	appMenu.AddRole(application.HideOthers)
	appMenu.AddRole(application.ShowAll)
	appMenu.AddSeparator()
	appMenu.AddRole(application.Quit)

	menu.AddRole(application.EditMenu)

	view := menu.AddSubmenu("View")
	view.Add("Reload").SetAccelerator("CmdOrCtrl+r").OnClick(func(*application.Context) { win.Reload() })
	view.AddSeparator()
	view.Add("Actual Size").SetAccelerator("CmdOrCtrl+0").OnClick(func(*application.Context) { win.ZoomReset() })
	view.Add("Zoom In").SetAccelerator("CmdOrCtrl+plus").OnClick(func(*application.Context) { win.ZoomIn() })
	view.Add("Zoom Out").SetAccelerator("CmdOrCtrl+-").OnClick(func(*application.Context) { win.ZoomOut() })
	view.AddSeparator()
	view.AddRole(application.ToggleFullscreen)
	if dev {
		view.AddSeparator()
		view.Add("Toggle DevTools").SetAccelerator("CmdOrCtrl+alt+i").OnClick(func(*application.Context) { win.OpenDevTools() })
	}

	srvMenu := menu.AddSubmenu("Server")
	serverMenuItems(srvMenu, ctl)
	srvMenu.AddSeparator()
	srvMenu.Add("Server Status & History…").OnClick(func(*application.Context) {
		win.Show()
		win.Focus()
		win.ExecJS(`if(window.PF&&PF.openServer){PF.openServer()}`)
	})

	menu.AddRole(application.WindowMenu)

	help := menu.AddSubmenu("Help")
	help.Add("Pocketlab Documentation").OnClick(func(*application.Context) { _ = app.Browser.OpenURL(repoURL) })
	help.Add("Open in Browser").OnClick(func(*application.Context) { _ = app.Browser.OpenURL(ctl.base()) })

	app.Menu.SetApplicationMenu(menu)
}

func buildTray(app *application.App, ctl *serverCtl, showWindow func()) {
	tray := app.SystemTray.New()
	tray.SetTemplateIcon(trayIcon)
	tray.SetTooltip("Pocketlab")

	m := app.NewMenu()
	m.Add("Open Pocketlab").OnClick(func(*application.Context) { showWindow() })
	m.AddSeparator()
	status := m.Add("Server: checking...")
	status.SetEnabled(false)
	serverMenuItems(m, ctl)
	m.AddSeparator()
	m.Add("Quit Pocketlab").OnClick(func(*application.Context) { app.Quit() })
	tray.SetMenu(m)

	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for range t.C {
			ctl.mu.Lock()
			addr, attached := ctl.addr, ctl.attached
			ctl.mu.Unlock()
			up := isPocketlab(addr)
			switch {
			case up && attached:
				status.SetLabel("Connected to running server " + addr)
			case up:
				status.SetLabel("Server: running on " + addr)
			default:
				status.SetLabel("Server: not responding (" + addr + ")")
			}
		}
	}()
}

// isPocketlab reports whether a Pocketlab server answers /api/v1/health at addr.
func isPocketlab(addr string) bool {
	c := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := c.Get("http://" + addr + "/api/v1/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		Service string `json:"service"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return false
	}
	return body.Service == "pocketlab" || body.Service == "phoneforge" // an older server on the port
}

// portBusy reports whether something is listening on addr (any protocol).
func portBusy(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return strings.Contains(err.Error(), "address already in use")
	}
	conn.Close()
	return true
}

// windowState is persisted to ~/.pocketlab/desktop.json.
type windowState struct {
	mu                  sync.Mutex `json:"-"`
	X, Y, Width, Height int
}

func statePath() string {
	return filepath.Join(server.PocketlabHome(), "desktop.json") // ~/.pocketlab/desktop.json
}

func loadState() *windowState {
	s := &windowState{}
	if p := statePath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, s)
		}
	}
	return s
}

// valid reports whether the stored geometry is sane and on a connected screen.
func (s *windowState) valid(app *application.App) bool {
	if s.Width < 600 || s.Height < 400 {
		return false
	}
	screens := app.Screen.GetAll()
	if len(screens) == 0 {
		return true
	}
	cx, cy := s.X+s.Width/2, s.Y+s.Height/2
	for _, sc := range screens {
		b := sc.Bounds
		if cx >= b.X && cx < b.X+b.Width && cy >= b.Y && cy < b.Y+b.Height {
			return true
		}
	}
	return false
}

func (s *windowState) capture(win *application.WebviewWindow) {
	w, h := win.Size()
	x, y := win.Position()
	s.mu.Lock()
	s.X, s.Y, s.Width, s.Height = x, y, w, h
	s.mu.Unlock()
}

func (s *windowState) save() {
	p := statePath()
	if p == "" {
		return
	}
	s.mu.Lock()
	b, err := json.MarshalIndent(s, "", "  ")
	s.mu.Unlock()
	if err != nil || s.Width == 0 {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		log.Printf("desktop state: %v", err)
	}
}
