package webhooks

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// webhookView is the API representation of a Webhook; the secret is never
// returned, only whether one is set.
type webhookView struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	URL        string          `json:"url"`
	Events     []string        `json:"events"`
	Secret     string          `json:"secret,omitempty"` // always masked
	HasSecret  bool            `json:"has_secret"`
	Enabled    bool            `json:"enabled"`
	CreatedAt  time.Time       `json:"created_at"`
	LastStatus *DeliveryStatus `json:"last_delivery,omitempty"`
}

func (m *Manager) view(w Webhook) webhookView {
	v := webhookView{
		ID: w.ID, Name: w.Name, URL: w.URL, Events: w.Events,
		HasSecret: w.Secret != "", Enabled: w.Enabled, CreatedAt: w.CreatedAt,
	}
	if w.Secret != "" {
		v.Secret = "********"
	}
	if st, ok := m.Status(w.ID); ok {
		v.LastStatus = &st
	}
	return v
}

func writeJSON(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	_ = json.NewEncoder(rw).Encode(v)
}

func writeErr(rw http.ResponseWriter, code int, err error) {
	writeJSON(rw, code, map[string]string{"error": err.Error()})
}

// Routes returns the webhook management API:
//
//	GET    /           list (secrets masked)
//	POST   /           create {name,url,events,secret}
//	PUT    /{id}       partial update {name,url,events,secret,enabled}
//	DELETE /{id}       remove
//	POST   /{id}/test  send a synthetic test event, returns delivery result
func (m *Manager) Routes() http.Handler {
	r := chi.NewRouter()

	r.Get("/", func(rw http.ResponseWriter, _ *http.Request) {
		list := m.store.List()
		out := make([]webhookView, 0, len(list))
		for _, w := range list {
			out = append(out, m.view(w))
		}
		writeJSON(rw, http.StatusOK, out)
	})

	r.Post("/", func(rw http.ResponseWriter, req *http.Request) {
		var in struct {
			Name   string   `json:"name"`
			URL    string   `json:"url"`
			Events []string `json:"events"`
			Secret string   `json:"secret"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(rw, req.Body, 1<<20)).Decode(&in); err != nil {
			writeErr(rw, http.StatusBadRequest, err)
			return
		}
		w, err := m.store.Add(in.Name, in.URL, in.Events, in.Secret)
		if err != nil {
			writeErr(rw, http.StatusBadRequest, err)
			return
		}
		writeJSON(rw, http.StatusCreated, m.view(w))
	})

	r.Put("/{id}", func(rw http.ResponseWriter, req *http.Request) {
		var in struct {
			Name    *string   `json:"name"`
			URL     *string   `json:"url"`
			Events  *[]string `json:"events"`
			Secret  *string   `json:"secret"`
			Enabled *bool     `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(rw, req.Body, 1<<20)).Decode(&in); err != nil {
			writeErr(rw, http.StatusBadRequest, err)
			return
		}
		w, err := m.store.Update(chi.URLParam(req, "id"), Patch{
			Name: in.Name, URL: in.URL, Events: in.Events, Secret: in.Secret, Enabled: in.Enabled,
		})
		if errors.Is(err, ErrNotFound) {
			writeErr(rw, http.StatusNotFound, err)
			return
		}
		if err != nil {
			writeErr(rw, http.StatusBadRequest, err)
			return
		}
		writeJSON(rw, http.StatusOK, m.view(w))
	})

	r.Delete("/{id}", func(rw http.ResponseWriter, req *http.Request) {
		err := m.store.Remove(chi.URLParam(req, "id"))
		if errors.Is(err, ErrNotFound) {
			writeErr(rw, http.StatusNotFound, err)
			return
		}
		if err != nil {
			writeErr(rw, http.StatusInternalServerError, err)
			return
		}
		writeJSON(rw, http.StatusOK, map[string]bool{"ok": true})
	})

	r.Post("/{id}/test", func(rw http.ResponseWriter, req *http.Request) {
		st, err := m.Test(req.Context(), chi.URLParam(req, "id"))
		if errors.Is(err, ErrNotFound) {
			writeErr(rw, http.StatusNotFound, err)
			return
		}
		writeJSON(rw, http.StatusOK, st)
	})

	return r
}
