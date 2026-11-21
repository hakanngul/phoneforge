package debugger

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"phoneforge/internal/device"
)

// Routes returns the handler to mount at /debug.
func Routes(m *Manager, get func(id string) (device.Device, error)) http.Handler {
	h := &handler{m: m, get: get}
	r := chi.NewRouter()
	r.Get("/sessions", h.list)
	r.Post("/sessions", h.create)
	r.Route("/sessions/{sid}", func(r chi.Router) {
		r.Get("/", h.info)
		r.Delete("/", h.detach)
		r.Get("/state", h.state)
		r.Get("/breakpoints", h.listBP)
		r.Post("/breakpoints", h.addBP)
		r.Delete("/breakpoints/{bid}", h.delBP)
		r.Post("/continue", h.resume(Continue))
		r.Post("/step-over", h.resume(StepOver))
		r.Post("/step-into", h.resume(StepInto))
		r.Post("/step-out", h.resume(StepOut))
		r.Post("/interrupt", h.interrupt)
		r.Get("/backtrace", h.backtrace)
		r.Get("/variables", h.variables)
		r.Post("/eval", h.eval)
	})
	return r
}

type handler struct {
	m   *Manager
	get func(id string) (device.Device, error)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code, status := "DEBUG_ERROR", http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNoSession):
		code, status = "SESSION_NOT_FOUND", http.StatusNotFound
	case errors.Is(err, device.ErrNotFound):
		code, status = "DEVICE_NOT_FOUND", http.StatusNotFound
	case errors.Is(err, device.ErrUnsupported):
		code, status = "UNSUPPORTED", http.StatusNotImplemented
	case errors.Is(err, ErrTimeout):
		code, status = "DEBUG_TIMEOUT", http.StatusGatewayTimeout
	case errors.Is(err, ErrDead):
		code, status = "SESSION_DEAD", http.StatusGone
	}
	writeJSON(w, status, map[string]string{"error": err.Error(), "code": code})
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg, "code": "BAD_REQUEST"})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		badRequest(w, err.Error())
		return false
	}
	if len(b) == 0 {
		return true // empty body = defaults
	}
	if err := json.Unmarshal(b, v); err != nil {
		badRequest(w, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func (h *handler) session(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	s, err := h.m.Get(chi.URLParam(r, "sid"))
	if err != nil {
		writeErr(w, err)
		return nil, false
	}
	return s, true
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.m.List())
}

type createReq struct {
	DeviceID    string            `json:"deviceId"`
	BundleID    string            `json:"bundleId"`
	PID         int               `json:"pid"`
	ProcessName string            `json:"processName"`
	Launch      bool              `json:"launch"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var b createReq
	if !decode(w, r, &b) {
		return
	}
	if b.DeviceID == "" {
		badRequest(w, "deviceId is required")
		return
	}
	dev, err := h.get(b.DeviceID)
	if err != nil {
		writeErr(w, err)
		return
	}
	info := dev.Info()
	platform := info.Platform
	if info.Simulator {
		platform = device.PlatformSimulator
	}
	if platform != device.PlatformIOS && platform != device.PlatformSimulator {
		writeErr(w, ErrNotSupported)
		return
	}
	// Attach/launch can take a while; not tied to the HTTP request timeout.
	ctx, cancel := context.WithTimeout(r.Context(), attachTimeout+30*time.Second)
	defer cancel()
	s, err := h.m.Create(ctx, Target{
		Platform: platform, Serial: info.Serial, BundleID: b.BundleID, PID: b.PID,
		ProcessName: b.ProcessName, Launch: b.Launch, Args: b.Args, Env: b.Env,
	})
	if err != nil {
		if errors.Is(err, ErrTimeout) || errors.Is(err, device.ErrUnsupported) {
			writeErr(w, err)
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "code": "DEBUG_ATTACH_FAILED"})
		}
		return
	}
	writeJSON(w, http.StatusCreated, s.Info())
}

func (h *handler) info(w http.ResponseWriter, r *http.Request) {
	if s, ok := h.session(w, r); ok {
		writeJSON(w, http.StatusOK, s.Info())
	}
}

func (h *handler) detach(w http.ResponseWriter, r *http.Request) {
	if err := h.m.Detach(r.Context(), chi.URLParam(r, "sid")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *handler) state(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	st, err := s.State(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": s.Info(), "stop": st})
}

func (h *handler) listBP(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	bps, err := s.Breakpoints(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bps)
}

func (h *handler) addBP(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	var spec BreakpointSpec
	if !decode(w, r, &spec) {
		return
	}
	if _, err := spec.command(); err != nil {
		badRequest(w, err.Error())
		return
	}
	bp, err := s.SetBreakpoint(r.Context(), spec)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, bp)
}

func (h *handler) delBP(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	id, err := strconv.Atoi(chi.URLParam(r, "bid"))
	if err != nil || id <= 0 {
		badRequest(w, "invalid breakpoint id")
		return
	}
	if err := s.RemoveBreakpoint(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type waitReq struct {
	WaitMs int `json:"waitMs"`
}

func (h *handler) resume(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := h.session(w, r)
		if !ok {
			return
		}
		var b waitReq
		if !decode(w, r, &b) {
			return
		}
		st, err := s.Resume(r.Context(), kind, b.WaitMs)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	}
}

func (h *handler) interrupt(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	var b waitReq
	if !decode(w, r, &b) {
		return
	}
	st, err := s.Interrupt(r.Context(), b.WaitMs)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func intQ(r *http.Request, name string, def int) (int, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}

func (h *handler) backtrace(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	th, ok1 := intQ(r, "thread", 0)
	if !ok1 {
		badRequest(w, "thread must be an integer")
		return
	}
	frames, raw, err := s.Backtrace(r.Context(), th, r.URL.Query().Get("all") == "true")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"frames": frames, "raw": raw})
}

func (h *handler) variables(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	fr, ok1 := intQ(r, "frame", -1)
	if !ok1 {
		badRequest(w, "frame must be an integer")
		return
	}
	vars, err := s.Variables(r.Context(), fr)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vars)
}

func (h *handler) eval(w http.ResponseWriter, r *http.Request) {
	s, ok := h.session(w, r)
	if !ok {
		return
	}
	var b struct {
		Expression string `json:"expression"`
		Frame      *int   `json:"frame"`
	}
	if !decode(w, r, &b) {
		return
	}
	fr := -1
	if b.Frame != nil {
		fr = *b.Frame
	}
	v, err := s.Eval(r.Context(), b.Expression, fr)
	if err != nil {
		if errors.Is(err, ErrTimeout) || errors.Is(err, ErrDead) {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "code": "EVAL_FAILED"})
		return
	}
	writeJSON(w, http.StatusOK, v)
}
