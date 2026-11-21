package crashreport

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"phoneforge/internal/device"
)

// Routes returns the handler to mount at /devices/{id}/crashes. The device id
// comes from the enclosing chi route ({id} URL parameter).
func Routes(svc *Service, get func(id string) (device.Device, error)) http.Handler {
	h := &handler{svc: svc, get: get}
	r := chi.NewRouter()
	r.Get("/", h.list)
	r.Get("/{name}", h.fetch)
	r.Delete("/{name}", h.remove)
	return r
}

type handler struct {
	svc *Service
	get func(id string) (device.Device, error)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code, status := "DEVICE_ERROR", http.StatusInternalServerError
	switch {
	case errors.Is(err, device.ErrNotFound):
		code, status = "DEVICE_NOT_FOUND", http.StatusNotFound
	case errors.Is(err, ErrNotFound):
		code, status = "CRASH_NOT_FOUND", http.StatusNotFound
	case errors.Is(err, device.ErrUnsupported):
		code, status = "UNSUPPORTED", http.StatusNotImplemented
	}
	writeJSON(w, status, map[string]string{"error": err.Error(), "code": code})
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg, "code": "BAD_REQUEST"})
}

func (h *handler) provider(w http.ResponseWriter, r *http.Request) (Provider, bool) {
	dev, err := h.get(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return nil, false
	}
	p, err := h.svc.ProviderFor(dev.Info())
	if err != nil {
		writeErr(w, err)
		return nil, false
	}
	return p, true
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	q := Query{App: r.URL.Query().Get("app")}
	if s := r.URL.Query().Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			badRequest(w, "since must be an RFC 3339 timestamp")
			return
		}
		q.Since = t
	}
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			badRequest(w, "limit must be a non-negative integer")
			return
		}
		q.Limit = n
	}
	p, ok := h.provider(w, r)
	if !ok {
		return
	}
	reports, err := p.List(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Filter(reports, q))
}

func reportName(r *http.Request) (string, bool) {
	n, err := url.PathUnescape(chi.URLParam(r, "name"))
	n = strings.TrimSpace(n)
	return n, err == nil && n != ""
}

func (h *handler) fetch(w http.ResponseWriter, r *http.Request) {
	name, ok := reportName(r)
	if !ok {
		badRequest(w, "report name is required")
		return
	}
	p, ok := h.provider(w, r)
	if !ok {
		return
	}
	text, err := p.Get(r.Context(), name)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(text))
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	name, ok := reportName(r)
	if !ok {
		badRequest(w, "report name is required")
		return
	}
	p, ok := h.provider(w, r)
	if !ok {
		return
	}
	if err := p.Delete(r.Context(), name); err != nil {
		if strings.Contains(err.Error(), "cannot be deleted") {
			err = errors.Join(device.ErrUnsupported, err)
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
