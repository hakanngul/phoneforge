// Package httpapi exposes the PhoneForge REST API under /api/v1.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"phoneforge/internal/agent"
	"phoneforge/internal/config"
	"phoneforge/internal/device"
	"phoneforge/internal/dsl"
	"phoneforge/internal/events"
	"phoneforge/internal/iossim"
	"phoneforge/internal/registry"
	"phoneforge/internal/uia2"
)

// allowedOrigins are the only origins that receive CORS headers.
var allowedOrigins = map[string]bool{
	"http://localhost:8700": true,
	"http://127.0.0.1:8700": true,
}

const maxBody = 4 << 20

type api struct {
	reg     *registry.Registry
	cfg     config.Config
	bridges *uia2.Manager
	ios     *IOSControl
}

// Deps holds optional feature sub-routers mounted under /api/v1.
// A nil handler is simply not mounted.
type Deps struct {
	Nodes       http.Handler  // mounted at /nodes
	Webhooks    http.Handler  // mounted at /webhooks
	Credentials http.Handler  // mounted at /credentials (exposes /secrets, /app-cards)
	Farms       http.Handler  // mounted at /farms
	Tests       http.Handler  // mounted at /tests (projects, .mob scripts, runs)
	AI          http.Handler  // mounted at /ai (AI Configuration: agents, settings, runs)
	OCR         http.Handler  // mounted at /ocr (on-screen text recognition)
	Web         http.Handler  // mounted at /web (Chrome/WebView automation over CDP)
	Debug       http.Handler  // mounted at /debug (lldb debug sessions: attach, breakpoints, step, eval)
	Crashes     http.Handler  // mounted at /devices/{id}/crashes (crash report list/read/delete)
	Sims        http.Handler  // mounted at /simulators (iOS Simulator boot/shutdown/WDA)
	Bridges     *uia2.Manager // UiAutomator2 bridge manager (enables /bridge routes)
	IOS         *IOSControl   // WebDriverAgent bridge for iOS (enables /iosbridge, /mjpeg and iOS /bridge)
}

// NewRouter builds the HTTP handler serving the REST API with no feature deps.
func NewRouter(reg *registry.Registry, cfg config.Config) http.Handler {
	return NewRouterWithDeps(reg, cfg, Deps{})
}

// NewRouterWithDeps builds the REST API and mounts the provided feature routers.
func NewRouterWithDeps(reg *registry.Registry, cfg config.Config, deps Deps) http.Handler {
	a := &api{reg: reg, cfg: cfg, bridges: deps.Bridges, ios: deps.IOS}
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", a.health)
		r.Get("/version", a.version)

		r.Group(func(r chi.Router) {
			r.Use(authMiddleware(cfg.APIToken))
			r.Handle("/events", events.Handler())
			if deps.Nodes != nil {
				r.Mount("/nodes", deps.Nodes)
			}
			if deps.Webhooks != nil {
				r.Mount("/webhooks", deps.Webhooks)
			}
			if deps.Credentials != nil {
				r.Mount("/credentials", deps.Credentials)
			}
			if deps.Farms != nil {
				r.Mount("/farms", deps.Farms)
			}
			if deps.Tests != nil {
				r.Mount("/tests", deps.Tests)
			}
			if deps.AI != nil {
				r.Mount("/ai", deps.AI)
			}
			if deps.OCR != nil {
				r.Mount("/ocr", deps.OCR)
			}
			if deps.Web != nil {
				r.Mount("/web", deps.Web)
			}
			if deps.Debug != nil {
				r.Mount("/debug", deps.Debug)
			}
			if deps.Sims != nil {
				r.Mount("/simulators", deps.Sims)
			}
			if deps.Bridges != nil {
				r.Get("/bridge/assets", a.bridgeAssets)
				r.Post("/bridge/assets/download", a.bridgeAssetsDownload)
			}
			r.Get("/devices", a.listDevices)
			r.Route("/devices/{id}", func(r chi.Router) {
				r.Get("/", a.withDevice(a.getDevice))
				r.Get("/screenshot", a.withDevice(a.screenshot))
				r.Get("/ui-tree", a.withDevice(a.uiTree))
				r.Post("/tap", a.withDevice(a.tap))
				r.Post("/long-press", a.withDevice(a.longPress))
				r.Post("/swipe", a.withDevice(a.swipe))
				r.Post("/type", a.withDevice(a.typeText))
				r.Post("/press-key", a.withDevice(a.pressKey))
				r.Get("/apps", a.withDevice(a.listApps))
				r.Post("/launch-app", a.withDevice(a.launchApp))
				r.Post("/stop-app", a.withDevice(a.stopApp))
				r.Post("/dsl/execute", a.withDevice(a.dslExecute))
				r.Post("/agent/run", a.withDevice(a.agentRun))
				if deps.IOS != nil {
					r.Route("/iosbridge", func(r chi.Router) {
						r.Get("/status", a.withDevice(a.iosBridgeStatus))
						r.Post("/start", a.withDevice(a.iosBridgeStart))
						r.Post("/stop", a.withDevice(a.iosBridgeStop))
						r.Get("/logs", a.withDevice(a.iosBridgeLogs))
						r.Post("/install", a.withDevice(a.iosBridgeInstall))
					})
					r.Get("/mjpeg", a.withDevice(a.mjpegStream))
					r.Get("/mjpeg/info", a.withDevice(a.mjpegInfo))
				}
				if deps.Bridges != nil || deps.IOS != nil {
					r.Get("/bridge", a.withDevice(a.bridgeGet))
					r.Post("/bridge/start", a.withDevice(a.bridgeStart))
					r.Post("/bridge/stop", a.withDevice(a.bridgeStop))
				}
				if deps.Crashes != nil {
					r.Mount("/crashes", deps.Crashes)
				}
				a.mountExtras(r)
			})
		})
	})
	return r
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code string, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// deviceErr maps a driver error to an HTTP response.
func deviceErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, device.ErrNotFound):
		writeErr(w, "DEVICE_NOT_FOUND", http.StatusNotFound, err.Error())
	case errors.Is(err, device.ErrUnsupported):
		writeErr(w, "UNSUPPORTED", http.StatusNotImplemented, err.Error())
	case errors.Is(err, iossim.ErrWDAStarting):
		w.Header().Set("Retry-After", "5")
		writeErr(w, "SIMULATOR_WDA_STARTING", http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, iossim.ErrNotBuilt):
		writeErr(w, "SIMULATOR_WDA_NOT_BUILT", http.StatusConflict, err.Error())
	case errors.Is(err, device.ErrElementNotFound):
		writeErr(w, "ELEMENT_NOT_FOUND", http.StatusNotFound, err.Error())
	default:
		writeErr(w, "DEVICE_ERROR", http.StatusInternalServerError, err.Error())
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && allowedOrigins[o] {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", o)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization")
			h.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func authMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" || isLoopback(r.RemoteAddr) ||
				r.Header.Get("X-API-Key") == token ||
				r.Header.Get("Authorization") == "Bearer "+token {
				next.ServeHTTP(w, r)
				return
			}
			writeErr(w, "UNAUTHORIZED", http.StatusUnauthorized, "unauthorized")
		})
	}
}

func (a *api) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service":     "phoneforge",
		"status":      "ok",
		"version":     a.cfg.Version,
		"deviceCount": len(a.reg.List()),
		"time":        time.Now().UTC().Format(time.RFC3339),
	})
}

func (a *api) version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": a.cfg.Version})
}

func (a *api) listDevices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.reg.List())
}

type devHandler func(w http.ResponseWriter, r *http.Request, dev device.Device)

func (a *api) withDevice(h devHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dev, err := a.reg.Get(chi.URLParam(r, "id"))
		if err != nil {
			deviceErr(w, err)
			return
		}
		h(w, r, dev)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(v); err != nil {
		writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func ok(w http.ResponseWriter, err error) {
	if err != nil {
		deviceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *api) getDevice(w http.ResponseWriter, r *http.Request, dev device.Device) {
	writeJSON(w, http.StatusOK, dev.Info())
}

func (a *api) screenshot(w http.ResponseWriter, r *http.Request, dev device.Device) {
	shot, err := dev.Screenshot(r.Context())
	if err != nil {
		deviceErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	if shot.Width > 0 && shot.Height > 0 {
		// Logical screen size (points on iOS); the PNG may be 2x/3x larger.
		w.Header().Set("X-Screen-Width", strconv.Itoa(shot.Width))
		w.Header().Set("X-Screen-Height", strconv.Itoa(shot.Height))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(shot.PNG)
}

func (a *api) uiTree(w http.ResponseWriter, r *http.Request, dev device.Device) {
	el, err := dev.UITree(r.Context())
	if err != nil {
		deviceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, el)
}

func (a *api) tap(w http.ResponseWriter, r *http.Request, dev device.Device) {
	var b struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	if !decode(w, r, &b) {
		return
	}
	ok(w, dev.Tap(r.Context(), b.X, b.Y))
}

func (a *api) longPress(w http.ResponseWriter, r *http.Request, dev device.Device) {
	var b struct {
		X  int `json:"x"`
		Y  int `json:"y"`
		Ms int `json:"ms"`
	}
	if !decode(w, r, &b) {
		return
	}
	ok(w, dev.LongPress(r.Context(), b.X, b.Y, b.Ms))
}

func (a *api) swipe(w http.ResponseWriter, r *http.Request, dev device.Device) {
	var b struct {
		X1         int `json:"x1"`
		Y1         int `json:"y1"`
		X2         int `json:"x2"`
		Y2         int `json:"y2"`
		DurationMs int `json:"durationMs"`
	}
	if !decode(w, r, &b) {
		return
	}
	ok(w, dev.Swipe(r.Context(), b.X1, b.Y1, b.X2, b.Y2, b.DurationMs))
}

func (a *api) typeText(w http.ResponseWriter, r *http.Request, dev device.Device) {
	var b struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &b) {
		return
	}
	ok(w, dev.TypeText(r.Context(), b.Text))
}

func (a *api) pressKey(w http.ResponseWriter, r *http.Request, dev device.Device) {
	var b struct {
		Key string `json:"key"`
	}
	if !decode(w, r, &b) {
		return
	}
	ok(w, dev.PressKey(r.Context(), device.Key(b.Key)))
}

func (a *api) listApps(w http.ResponseWriter, r *http.Request, dev device.Device) {
	apps, err := dev.ListApps(r.Context())
	if err != nil {
		deviceErr(w, err)
		return
	}
	if apps == nil {
		apps = []device.AppInfo{}
	}
	writeJSON(w, http.StatusOK, apps)
}

func bundleID(w http.ResponseWriter, r *http.Request) (string, bool) {
	var b struct {
		BundleID string `json:"bundleId"`
	}
	if !decode(w, r, &b) {
		return "", false
	}
	if strings.TrimSpace(b.BundleID) == "" {
		writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "bundleId is required")
		return "", false
	}
	return b.BundleID, true
}

func (a *api) launchApp(w http.ResponseWriter, r *http.Request, dev device.Device) {
	if id, good := bundleID(w, r); good {
		ok(w, dev.LaunchApp(r.Context(), id))
	}
}

func (a *api) stopApp(w http.ResponseWriter, r *http.Request, dev device.Device) {
	if id, good := bundleID(w, r); good {
		ok(w, dev.StopApp(r.Context(), id))
	}
}

func (a *api) dslExecute(w http.ResponseWriter, r *http.Request, dev device.Device) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		writeErr(w, "BAD_REQUEST", http.StatusBadRequest, err.Error())
		return
	}
	res, err := dsl.Execute(r.Context(), dev, body)
	if err != nil {
		writeErr(w, "DSL_ERROR", http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// agentRun streams an AI agent session over SSE: it runs a natural-language
// task against the device and emits one `data: <Step JSON>` frame per step.
func (a *api) agentRun(w http.ResponseWriter, r *http.Request, dev device.Device) {
	var body struct {
		Task     string `json:"task"`
		MaxSteps int    `json:"maxSteps"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Task == "" {
		writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "task is required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, "UNSUPPORTED", http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events.Info("agent: " + body.Task)
	emit := func(s agent.Step) {
		b, err := json.Marshal(s)
		if err != nil {
			return
		}
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(b)
		_, _ = w.Write([]byte("\n\n"))
		flusher.Flush()
		if s.Type == "done" {
			events.Success("agent done: " + s.Text)
		} else if s.Type == "error" {
			events.Errorf("agent error: %s", s.Text)
		}
	}
	_, _ = agent.Run(r.Context(), dev, body.Task, body.MaxSteps, emit)
}
