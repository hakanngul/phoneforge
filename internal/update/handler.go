package update

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Handler serves the REST contract. Mount it at /api/v1/update; it routes on
// whatever follows the last "/update" in the request path (or on the whole
// path when the prefix was already stripped with http.StripPrefix), so both
//
//	mux.Handle("/api/v1/update", h); mux.Handle("/api/v1/update/", h)
//	mux.Handle("/api/v1/update/", http.StripPrefix("/api/v1/update", h))
//
// work. Mutations (POST/PUT) are accepted only from loopback clients; a
// request carrying an Origin must come from this server's own origin (or the
// Wails webview), and every mutation must carry the X-Pocketlab-Action: update
// header (which forces a CORS preflight that is never granted, so other
// websites cannot POST here) and, when it has a body, Content-Type
// application/json. Anything else gets 403 (415 for a wrong content type).
func (m *Manager) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub := r.URL.Path
		if i := strings.LastIndex(sub, "/update"); i >= 0 {
			sub = sub[i+len("/update"):]
		}
		sub = strings.TrimSuffix(sub, "/")

		if sub == "" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				methodNotAllowed(w, "GET")
				return
			}
			writeJSON(w, http.StatusOK, m.Status())
			return
		}
		want := http.MethodPost
		switch sub {
		case "/check", "/download", "/install", "/skip", "/remind", "/rollback":
		case "/settings":
			want = http.MethodPut
		default:
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if r.Method != want {
			methodNotAllowed(w, want)
			return
		}
		if !loopbackRequest(r) {
			writeErr(w, http.StatusForbidden, "update actions are only available from this computer")
			return
		}
		if !originOK(r) {
			writeErr(w, http.StatusForbidden, "cross-origin update request rejected")
			return
		}
		if r.Header.Get(ActionHeader) != ActionValue {
			writeErr(w, http.StatusForbidden, "missing "+ActionHeader+": "+ActionValue+" header")
			return
		}
		if r.ContentLength != 0 && !isJSON(r.Header.Get("Content-Type")) {
			writeErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		switch sub {
		case "/check":
			// Detached from the request: a client that disconnects mid-check must
			// not make the check fail (and count as a feed failure).
			ctx, cancel := context.WithTimeout(m.baseContext(), 60*time.Second)
			st, err := m.CheckNow(ctx)
			cancel()
			switch {
			case errors.Is(err, ErrDisabled), errors.Is(err, ErrBusyOp):
				writeErr(w, http.StatusConflict, err.Error())
			default: // a failed check is reported in Status.error
				writeJSON(w, http.StatusOK, st)
			}
		case "/download":
			m.async(w, m.StartDownload())
		case "/install":
			m.async(w, m.StartInstall())
		case "/rollback":
			m.async(w, m.StartRollback())
		case "/skip":
			var body struct {
				Version string `json:"version"`
			}
			if !readJSON(w, r, &body) {
				return
			}
			if !ValidVersion(body.Version) {
				writeErr(w, http.StatusBadRequest, "version is required")
				return
			}
			m.Skip(body.Version)
			writeJSON(w, http.StatusOK, m.Status())
		case "/remind":
			var body struct {
				Hours float64 `json:"hours"`
			}
			if r.ContentLength != 0 && !readJSON(w, r, &body) {
				return
			}
			if body.Hours < 0 || body.Hours > 24*365 {
				writeErr(w, http.StatusBadRequest, "hours out of range")
				return
			}
			m.Remind(time.Duration(body.Hours * float64(time.Hour)))
			writeJSON(w, http.StatusOK, m.Status())
		case "/settings":
			var body Settings
			if !readJSON(w, r, &body) {
				return
			}
			if err := m.SetSettings(body); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, m.Status())
		}
	})
}

func (m *Manager) async(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, m.Status())
	case errors.Is(err, ErrNothingToUpdate), errors.Is(err, ErrBusyOp), errors.Is(err, ErrDisabled),
		errors.Is(err, ErrUnsupported), strings.HasPrefix(err.Error(), "busy:"):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// ActionHeader must be sent as ActionValue on every update mutation.
const (
	ActionHeader = "X-Pocketlab-Action"
	ActionValue  = "update"
)

func isJSON(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && mt == "application/json"
}

// originOK mirrors the server's shutdown rule: a request without Origin (curl,
// CLI) is fine; otherwise the Origin must be this server's own (same as Host)
// or the Wails webview's.
func originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	if o == "wails://wails" || o == "http://wails.localhost" || o == "https://wails.localhost" {
		return true
	}
	return o == "http://"+r.Host || o == "https://"+r.Host
}

func loopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != "wails://wails" && o != "http://wails.localhost" && o != "https://wails.localhost" {
		u, err := url.Parse(o)
		if err != nil {
			return false
		}
		h := u.Hostname()
		if ip := net.ParseIP(h); !(h == "localhost" || strings.HasSuffix(h, ".localhost") || (ip != nil && ip.IsLoopback())) {
			return false
		}
	}
	return true
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
