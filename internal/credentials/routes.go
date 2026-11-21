package credentials

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
)

const maxBody = 1 << 20

// Routes returns the credentials HTTP API. No response ever contains a secret
// value:
//
//	GET    /secrets             -> {"names":[...]}
//	PUT    /secrets/{name}      body {"value":"..."}
//	DELETE /secrets/{name}
//	GET    /app-cards           -> {"cards":[masked...]}
//	POST   /app-cards           body {id?,bundleId,label,fields,values,secrets}
//	DELETE /app-cards/{id}
func Routes(sec *SecretStore, cards *CardStore) http.Handler {
	r := chi.NewRouter()

	r.Get("/secrets", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"names": sec.Names()})
	})
	r.Put("/secrets/{name}", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Value string `json:"value"`
		}
		if !decode(w, req, &body) {
			return
		}
		name := chi.URLParam(req, "name")
		if err := sec.Set(name, body.Value); err != nil {
			writeErr(w, http.StatusBadRequest, "could not store secret")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "stored": true})
	})
	r.Delete("/secrets/{name}", func(w http.ResponseWriter, req *http.Request) {
		if err := sec.Delete(chi.URLParam(req, "name")); err != nil {
			writeErr(w, http.StatusInternalServerError, "could not delete secret")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r.Get("/app-cards", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"cards": cards.List()})
	})
	r.Post("/app-cards", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			AppCard
			Secrets map[string]string `json:"secrets"`
		}
		if !decode(w, req, &body) {
			return
		}
		c, err := cards.Add(body.AppCard, body.Secrets)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, c)
	})
	r.Delete("/app-cards/{id}", func(w http.ResponseWriter, req *http.Request) {
		err := cards.Delete(chi.URLParam(req, "id"))
		switch {
		case errors.Is(err, ErrNotFound):
			writeErr(w, http.StatusNotFound, "card not found")
		case err != nil:
			writeErr(w, http.StatusInternalServerError, "could not delete card")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	return r
}

func decode(w http.ResponseWriter, req *http.Request, v any) bool {
	req.Body = http.MaxBytesReader(w, req.Body, maxBody)
	if err := json.NewDecoder(req.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
