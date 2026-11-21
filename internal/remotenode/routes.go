package remotenode

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// nodeView is the public representation of a Node; the token is never echoed.
type nodeView struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Addr    string    `json:"addr"`
	Token   string    `json:"token"` // "set" or ""
	AddedAt time.Time `json:"addedAt"`
}

func view(n Node) nodeView {
	t := ""
	if n.Token != "" {
		t = "set"
	}
	return nodeView{ID: n.ID, Name: n.Name, Addr: n.Addr, Token: t, AddedAt: n.AddedAt}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

// Routes returns the node-management router (mount at e.g. /api/v1/nodes).
func (s *Store) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		nodes := s.List()
		out := make([]nodeView, 0, len(nodes))
		for _, n := range nodes {
			out = append(out, view(n))
		}
		writeJSON(w, http.StatusOK, out)
	})
	r.Post("/", func(w http.ResponseWriter, req *http.Request) {
		var b struct {
			Name  string `json:"name"`
			Addr  string `json:"addr"`
			Token string `json:"token"`
		}
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<16)).Decode(&b); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		n, err := s.Add(b.Name, b.Addr, b.Token)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, view(n))
	})
	r.Delete("/{id}", func(w http.ResponseWriter, req *http.Request) {
		err := s.Remove(chi.URLParam(req, "id"))
		switch {
		case errors.Is(err, ErrNodeNotFound):
			writeErr(w, http.StatusNotFound, err.Error())
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err.Error())
		default:
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		}
	})
	r.Post("/{id}/test", func(w http.ResponseWriter, req *http.Request) {
		n, ok := s.Get(chi.URLParam(req, "id"))
		if !ok {
			writeErr(w, http.StatusNotFound, ErrNodeNotFound.Error())
			return
		}
		b, err := do(req.Context(), n, http.MethodGet, "/api/v1/health", nil)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		var h struct {
			Version     string `json:"version"`
			DeviceCount int    `json:"deviceCount"`
		}
		if err := json.Unmarshal(b, &h); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "bad health response: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": h.Version, "deviceCount": h.DeviceCount})
	})
	return r
}
