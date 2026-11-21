package iosbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code string, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// HandleStatus: GET .../status
func (m *Manager) HandleStatus(w http.ResponseWriter, r *http.Request, udid string) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, m.Refresh(ctx, udid))
}

// HandleStart: POST .../start with an optional JSON Options body.
func (m *Manager) HandleStart(w http.ResponseWriter, r *http.Request, udid string) {
	var o Options
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&o); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, "BAD_REQUEST", http.StatusBadRequest, err.Error())
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	st, err := m.Start(ctx, udid, o)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, st)
	case errors.Is(err, ErrBusy):
		writeErr(w, "IOSBRIDGE_BUSY", http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotInstalled):
		writeErr(w, "IOSBRIDGE_NOT_INSTALLED", http.StatusConflict, err.Error())
	case errors.Is(err, ErrTunnelUnsupported):
		writeErr(w, "IOSBRIDGE_TUNNEL_ROOT", http.StatusConflict, err.Error())
	default:
		writeErr(w, "IOSBRIDGE_START_FAILED", http.StatusInternalServerError, err.Error())
	}
}

// HandleStop: POST .../stop
func (m *Manager) HandleStop(w http.ResponseWriter, r *http.Request, udid string) {
	_ = m.Stop(udid)
	writeJSON(w, http.StatusOK, m.Status(udid))
}

// HandleLogs: GET .../logs (text/plain, one line per log line).
func (m *Manager) HandleLogs(w http.ResponseWriter, r *http.Request, udid string) {
	lines := m.Logs(udid)
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(strings.Join(lines, "\n") + "\n"))
}

// Routes returns a handler serving GET /status, POST /start, POST /stop and
// GET /logs for the device udidOf(r) returns (e.g. read from a chi URL param).
// Mount it under /api/v1/devices/{id}/iosbridge with http.StripPrefix.
func (m *Manager) Routes(udidOf func(*http.Request) string) http.Handler {
	mux := http.NewServeMux()
	wrap := func(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id := udidOf(r)
			if id == "" {
				writeErr(w, "DEVICE_NOT_FOUND", http.StatusNotFound, "unknown device")
				return
			}
			h(w, r, id)
		}
	}
	mux.HandleFunc("GET /status", wrap(m.HandleStatus))
	mux.HandleFunc("POST /start", wrap(m.HandleStart))
	mux.HandleFunc("POST /stop", wrap(m.HandleStop))
	mux.HandleFunc("GET /logs", wrap(m.HandleLogs))
	return mux
}
