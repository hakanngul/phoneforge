package iossim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"pocketlab/internal/device"
	"pocketlab/internal/iosbridge"
)

// SimStatus is the REST shape of one simulator.
type SimStatus struct {
	Sim
	DeviceID string           `json:"deviceId,omitempty"` // set when booted (iOS)
	WDA      iosbridge.Status `json:"wda"`
}

func (c *Control) status(s Sim) SimStatus {
	st := SimStatus{Sim: s, WDA: c.Mgr.Status(s.UDID)}
	if s.Booted() && s.Platform == "iOS" {
		st.DeviceID = FormatID(s.UDID)
	}
	return st
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code string, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, device.ErrNotFound):
		writeErr(w, "SIMULATOR_NOT_FOUND", http.StatusNotFound, "simulator not found")
	case errors.Is(err, ErrNotBooted):
		writeErr(w, "SIMULATOR_NOT_BOOTED", http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotBuilt):
		writeErr(w, "SIMULATOR_WDA_NOT_BUILT", http.StatusConflict, err.Error())
	case errors.Is(err, ErrWDAStarting):
		w.Header().Set("Retry-After", "5")
		writeErr(w, "SIMULATOR_WDA_STARTING", http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, iosbridge.ErrBusy):
		writeErr(w, "IOSBRIDGE_BUSY", http.StatusConflict, err.Error())
	default:
		writeErr(w, "SIMULATOR_ERROR", http.StatusInternalServerError, err.Error())
	}
}

// Routes returns the router mounted at /api/v1/simulators.
func (c *Control) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 15*time.Second)
		defer cancel()
		sims, err := c.List(ctx)
		if err != nil {
			fail(w, err)
			return
		}
		out := make([]SimStatus, 0, len(sims))
		for _, s := range sims {
			out = append(out, c.status(s))
		}
		writeJSON(w, http.StatusOK, map[string]any{"simulators": out, "wdaBuilt": c.XCTestRun() != ""})
	})
	r.Route("/{udid}", func(r chi.Router) {
		r.Post("/boot", func(w http.ResponseWriter, req *http.Request) {
			s, err := c.Boot(req.Context(), chi.URLParam(req, "udid"))
			if err != nil {
				fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, c.status(s))
		})
		r.Post("/shutdown", func(w http.ResponseWriter, req *http.Request) {
			s, err := c.Shutdown(req.Context(), chi.URLParam(req, "udid"))
			if err != nil {
				fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, c.status(s))
		})
		r.Post("/wda/start", func(w http.ResponseWriter, req *http.Request) {
			var o iosbridge.Options
			if req.Body != nil {
				_ = json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<16)).Decode(&o)
			}
			ctx, cancel := context.WithTimeout(req.Context(), 120*time.Second)
			defer cancel()
			st, err := c.UserStart(ctx, chi.URLParam(req, "udid"), o)
			if err != nil {
				fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, st)
		})
		r.Post("/wda/stop", func(w http.ResponseWriter, req *http.Request) {
			writeJSON(w, http.StatusOK, c.UserStop(chi.URLParam(req, "udid")))
		})
		r.Get("/wda/status", func(w http.ResponseWriter, req *http.Request) {
			writeJSON(w, http.StatusOK, c.Mgr.Status(chi.URLParam(req, "udid")))
		})
		r.Get("/wda/logs", func(w http.ResponseWriter, req *http.Request) {
			c.Mgr.HandleLogs(w, req, chi.URLParam(req, "udid"))
		})
	})
	return r
}
