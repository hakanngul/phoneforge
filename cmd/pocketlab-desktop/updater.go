package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"pocketlab/internal/brand"
	"pocketlab/internal/lifecycle"
	"pocketlab/internal/server"
	"pocketlab/internal/update"
	"pocketlab/internal/version"
)

const (
	releasesPage       = repoURL + "/releases/latest"
	healthyAfter       = 60 * time.Second
	checkMenuLabel     = "Check for Updates…"
	remindLaterDefault = 24 * time.Hour
)

// desktopUpdater wires internal/update into the desktop app: the manager, its
// hooks, the menu/tray items and the native fallback dialog.
type desktopUpdater struct {
	mgr *update.Manager

	app *application.App
	win *application.WebviewWindow
	ctl *serverCtl

	mu    sync.Mutex
	items []*application.MenuItem

	navDone atomic.Bool
}

// isDevVersion reports an unstamped (development) build.
func isDevVersion(v string) bool {
	return v == "" || strings.HasSuffix(v, "-dev")
}

// newDesktopUpdater builds the manager. It must be created before the server
// starts (the handler goes into server.Options); bind() supplies the app, window
// and server controller afterwards.
func newDesktopUpdater(dev bool) *desktopUpdater {
	u := &desktopUpdater{}
	home := server.PocketlabHome()

	disabled, reason := false, ""
	switch {
	case brand.Getenv("PL_UPDATE_DISABLE") != "" || brand.Getenv("POCKETLAB_UPDATE_DISABLE") != "":
		disabled, reason = true, "updates are disabled by PL_UPDATE_DISABLE"
	case isDevVersion(version.Version):
		disabled, reason = true, "this is a development build ("+version.Version+")"
	}

	cfg := update.Config{CurrentVersion: version.Version}
	delay := time.Duration(0)
	if v := brand.Getenv("PL_UPDATE_URL"); v != "" {
		if dev {
			cfg.ManifestURL = v
			log.Printf("update: manifest URL overridden by PL_UPDATE_URL: %s", v)
		} else {
			log.Printf("update: ignoring PL_UPDATE_URL (honoured only in dev builds)")
		}
	}
	if v := brand.Getenv("PL_UPDATE_CHECK_DELAY"); v != "" && dev {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			delay = d
		} else {
			log.Printf("update: bad PL_UPDATE_CHECK_DELAY %q", v)
		}
	}

	currentApp, err := update.RunningApp()
	if err != nil {
		currentApp = ""
		log.Printf("update: in-place install unavailable: %v", err)
	}
	staging := filepath.Join(home, "updates")
	inst := update.NewPlatformInstaller(update.InstallerOptions{
		CurrentApp: currentApp, StagingDir: staging, BackupDir: filepath.Join(staging, "backup"),
	})

	u.mgr = update.NewManager(update.ManagerOptions{
		Dir:             home,
		CurrentVersion:  version.Version,
		Checker:         cfg,
		Installer:       inst,
		Disabled:        disabled,
		DisabledReason:  reason,
		ManualURL:       manualURL,
		FirstCheckDelay: delay,
		Hooks: update.Hooks{
			OnChange:           u.onChange,
			Busy:               u.busy,
			BeforeInstall:      u.beforeInstall,
			AfterFailedInstall: u.afterFailedInstall,
			Quit:               u.quit,
			Record: func(kind, reason, message string) {
				lifecycle.Append(home, lifecycle.Event{Event: kind, Reason: reason, Message: message, Origin: "updater", Version: version.Version})
			},
		},
	})
	return u
}

// manualURL picks the page to send the user to when in-place install is not
// possible: this platform's DMG / zip, else the release notes, else the releases page.
func manualURL(m *update.Manifest) string {
	if m == nil {
		return releasesPage
	}
	kind := update.KindAppZip
	if runtime.GOOS == "darwin" {
		kind = update.KindDMG
	}
	for _, a := range m.Assets {
		if a.OS == runtime.GOOS && a.Arch == runtime.GOARCH && a.Kind == kind && a.URL != "" {
			return a.URL
		}
	}
	if m.NotesURL != "" {
		return m.NotesURL
	}
	return releasesPage
}

// bind connects the updater to the running app and starts background work.
func (u *desktopUpdater) bind(app *application.App, win *application.WebviewWindow, ctl *serverCtl) {
	u.app, u.win, u.ctl = app, win, ctl
	u.mgr.Startup()
	u.mgr.Start(context.Background())

	win.OnWindowEvent(events.Mac.WebViewDidFinishNavigation, func(*application.WindowEvent) { u.navDone.Store(true) })
	go u.healthyLoop()
}

// healthyLoop marks the running version healthy once the window has loaded and
// the server answers, after healthyAfter of uptime: a crash during startup
// leaves the pending marker so the next launch offers a rollback.
func (u *desktopUpdater) healthyLoop() {
	time.Sleep(healthyAfter)
	for {
		u.ctl.mu.Lock()
		addr, fb, quitting := u.ctl.addr, u.ctl.fallback, u.ctl.quitting
		u.ctl.mu.Unlock()
		if quitting {
			return
		}
		if up, _ := probeHealth(addr); up && !fb && u.navDone.Load() {
			u.mgr.MarkHealthy()
			return
		}
		time.Sleep(2 * time.Second)
	}
}

// ---- hooks ----

// busy refuses an install while a test run, flow run or screen recording is
// active in the owned server. An attached (external) server cannot be asked:
// it reports not busy.
func (u *desktopUpdater) busy() (bool, string) {
	if u.ctl == nil {
		return false, ""
	}
	u.ctl.mu.Lock()
	srv := u.ctl.srv
	u.ctl.mu.Unlock()
	if srv == nil {
		return false, ""
	}
	return srv.Busy()
}

func (u *desktopUpdater) beforeInstall(ver string) error {
	if u.ctl != nil {
		u.ctl.shutdownOwnedBy("update to "+ver, "updater")
	}
	return nil
}

func (u *desktopUpdater) afterFailedInstall() {
	if u.ctl == nil {
		return
	}
	u.ctl.mu.Lock()
	has, attached := u.ctl.srv != nil, u.ctl.attached
	u.ctl.mu.Unlock()
	if has || attached {
		return
	}
	if err := u.ctl.Start(); err != nil {
		log.Printf("update: restarting the server after a failed install: %v", err)
	}
}

func (u *desktopUpdater) quit() {
	if u.ctl != nil {
		u.ctl.quit() // marks quitting, then app.Quit()
	}
}

// onChange refreshes the menu/tray labels. It must not block.
func (u *desktopUpdater) onChange(s update.Status) {
	label := checkMenuLabel
	if a := s.Available; a != nil && !s.Disabled && !s.Skipped && s.RemindUntil == nil {
		switch s.State {
		case update.StateAvailable, update.StateReady, update.StateDownloading:
			label = "Update to " + a.Version + " available…"
		}
	}
	u.mu.Lock()
	items := append([]*application.MenuItem(nil), u.items...)
	u.mu.Unlock()
	for _, it := range items {
		it.SetLabel(label)
	}
}

// ---- menu ----

// addMenuItem adds the "Check for Updates…" entry to menu.
func (u *desktopUpdater) addMenuItem(m *application.Menu) {
	it := m.Add(checkMenuLabel)
	it.OnClick(func(*application.Context) { u.open() })
	u.mu.Lock()
	u.items = append(u.items, it)
	u.mu.Unlock()
	u.onChange(u.mgr.Status())
}

// open handles a click on the menu item: the web UI's update modal when the
// in-process server serves it, a native dialog otherwise (attached server,
// fallback page).
func (u *desktopUpdater) open() {
	u.win.Show()
	u.win.Focus()
	u.ctl.mu.Lock()
	inProcess := u.ctl.srv != nil && !u.ctl.fallback && !u.ctl.attached
	addr := u.ctl.addr
	u.ctl.mu.Unlock()
	if inProcess {
		u.win.ExecJS(`window.PF&&PF.openUpdates&&PF.openUpdates(true)`)
		return
	}
	go u.nativeFlow(addr)
}

func (u *desktopUpdater) dialog(kind string, title, msg string) *application.MessageDialog {
	var d *application.MessageDialog
	switch kind {
	case "warn":
		d = u.app.Dialog.Warning()
	case "q":
		d = u.app.Dialog.Question()
	default:
		d = u.app.Dialog.Info()
	}
	return d.SetTitle(title).SetMessage(msg).AttachToWindow(u.win)
}

// nativeFlow runs a check and shows the result in native dialogs.
func (u *desktopUpdater) nativeFlow(addr string) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	st, err := u.mgr.CheckNow(ctx)
	if st.Disabled {
		u.dialogShow("info", "Updates", "Updates are turned off: "+st.Error)
		return
	}
	if err != nil {
		u.dialogShow("warn", "Could not check for updates", err.Error())
		return
	}
	a := st.Available
	if a == nil {
		msg := "Pocketlab " + version.Version + " is the latest version."
		u.dialogShow("info", "You're up to date", msg)
		return
	}
	msg := "Pocketlab " + a.Version + " is available. You have " + version.Version + "."
	if notes := trimNotes(a.NotesMD); notes != "" {
		msg += "\n\n" + notes
	}
	if v := attachedVersion(addr); v != "" && v != version.Version {
		msg += "\n\nNote: the server this app is connected to runs " + v + "; it is not updated by this app."
	}
	d := u.dialog("q", "Pocketlab "+a.Version+" available", msg)
	if st.Supported {
		d.AddButton("Install and Restart").SetAsDefault().OnClick(func() {
			if err := u.mgr.StartInstall(); err != nil {
				u.dialogShow("warn", "Could not install the update", err.Error())
			}
		})
	} else {
		url := st.ManualURL
		if url == "" {
			url = releasesPage
		}
		d.AddButton("Download").SetAsDefault().OnClick(func() { _ = u.app.Browser.OpenURL(url) })
	}
	d.AddButton("Remind me later").OnClick(func() { u.mgr.Remind(remindLaterDefault) })
	d.AddButton("Skip this version").SetAsCancel().OnClick(func() { u.mgr.Skip(a.Version) })
	d.Show()
}

func (u *desktopUpdater) dialogShow(kind, title, msg string) {
	d := u.dialog(kind, title, msg)
	d.AddButton("OK").SetAsDefault()
	d.Show()
}

func trimNotes(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 700 {
		s = string(r[:700]) + "…"
	}
	return s
}

// attachedVersion returns the version of the server at addr, "" when unknown.
func attachedVersion(addr string) string {
	if addr == "" {
		return ""
	}
	resp, err := (&http.Client{Timeout: 1500 * time.Millisecond}).Get("http://" + addr + "/api/v1/version")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var b struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(resp.Body).Decode(&b) != nil {
		return ""
	}
	return b.Version
}
