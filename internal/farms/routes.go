package farms

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Routes returns the farms HTTP API (mount under e.g. /api/farms):
//
//	GET    /                     status of all providers (no secrets)
//	PUT    /{provider}           body: JSON object of credential strings
//	DELETE /{provider}           disable and delete stored credentials
//	GET    /{provider}/devices   list cloud devices
//
// Credentials are accepted on PUT but are never echoed in any response.
func (m *Manager) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, m.Status())
	})
	r.Put("/{provider}", func(w http.ResponseWriter, req *http.Request) {
		p := Provider(chi.URLParam(req, "provider"))
		var creds map[string]string
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<16)).Decode(&creds); err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
			return
		}
		if err := m.Configure(p, creds); err != nil {
			writeErr(w, statusFor(err), sanitize(err))
			return
		}
		writeJSON(w, http.StatusOK, m.statusOf(p))
	})
	r.Delete("/{provider}", func(w http.ResponseWriter, req *http.Request) {
		p := Provider(chi.URLParam(req, "provider"))
		if err := m.Disable(p); err != nil {
			writeErr(w, statusFor(err), sanitize(err))
			return
		}
		writeJSON(w, http.StatusOK, m.statusOf(p))
	})
	r.Get("/{provider}/devices", func(w http.ResponseWriter, req *http.Request) {
		p := Provider(chi.URLParam(req, "provider"))
		devs, err := m.ListDevices(req.Context(), p)
		if err != nil {
			writeErr(w, statusFor(err), sanitize(err))
			return
		}
		if devs == nil {
			devs = []CloudDevice{}
		}
		writeJSON(w, http.StatusOK, devs)
	})
	return r
}

func (m *Manager) statusOf(p Provider) Config {
	for _, c := range m.Status() {
		if c.Provider == p {
			return c
		}
	}
	return Config{Provider: p}
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrUnknownProvider):
		return http.StatusNotFound
	case errors.Is(err, ErrMissingCredentials):
		return http.StatusPreconditionFailed
	case errors.Is(err, ErrAWSNotImplemented):
		return http.StatusNotImplemented
	}
	return http.StatusBadGateway
}

// sanitize returns err unchanged; provider errors never include credentials
// (only HTTP status codes), so nothing secret can leak through it.
func sanitize(err error) error { return err }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
