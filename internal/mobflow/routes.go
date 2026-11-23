package mobflow

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
)

type apiError struct {
	Error  string           `json:"error"`
	Code   string           `json:"code"`
	Errors ValidationErrors `json:"errors,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: msg, Code: code})
}

func storeErr(w http.ResponseWriter, err error) {
	var ve ValidationErrors
	var se *StateError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, apiError{Error: ve.Error(), Code: "invalid_case", Errors: ve})
	case errors.As(err, &se):
		writeErr(w, http.StatusConflict, "invalid_state", err.Error())
	case errors.Is(err, ErrDeviceBusy):
		writeErr(w, http.StatusConflict, "device_busy", err.Error())
	case errors.Is(err, ErrNotImplemented):
		writeErr(w, http.StatusNotImplemented, "not_implemented", err.Error())
	case errors.Is(err, ErrInvalid):
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, ErrExists):
		writeErr(w, http.StatusConflict, "conflict", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large or unreadable")
		return nil, false
	}
	return b, true
}

type cacheView struct {
	Cache     *Cache            `json:"cache"`
	Staleness map[string]string `json:"staleness"`
}

func (s *Service) cacheView(project, name string, cache *Cache) (cacheView, error) {
	e, err := s.Store.Get(project, name)
	if err != nil {
		return cacheView{}, err
	}
	if e.Case == nil {
		return cacheView{}, ErrInvalid
	}
	return cacheView{Cache: cache, Staleness: Staleness(e.Case, cache)}, nil
}

// Routes returns the handler mounted at /api/v1/flows.
func (s *Service) Routes() http.Handler {
	r := chi.NewRouter()
	st := s.Store
	platform := func(req *http.Request) string { return req.URL.Query().Get("platform") }

	r.Get("/cases", func(w http.ResponseWriter, req *http.Request) {
		list, err := st.List(req.URL.Query().Get("project"))
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
	r.Post("/cases/validate", func(w http.ResponseWriter, req *http.Request) {
		b, ok := readBody(w, req, maxCaseBytes+1)
		if !ok {
			return
		}
		_, errs := Parse(b)
		if errs == nil {
			errs = ValidationErrors{}
		}
		writeJSON(w, http.StatusOK, struct {
			OK     bool             `json:"ok"`
			Errors ValidationErrors `json:"errors"`
		}{len(errs) == 0, errs})
	})
	r.Get("/cases/{project}/{name}", func(w http.ResponseWriter, req *http.Request) {
		e, err := st.Get(chi.URLParam(req, "project"), chi.URLParam(req, "name"))
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, e)
	})
	r.Put("/cases/{project}/{name}", func(w http.ResponseWriter, req *http.Request) {
		b, ok := readBody(w, req, maxCaseBytes+1)
		if !ok {
			return
		}
		e, created, err := st.Save(chi.URLParam(req, "project"), chi.URLParam(req, "name"), b)
		if err != nil {
			storeErr(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, e)
	})
	r.Delete("/cases/{project}/{name}", func(w http.ResponseWriter, req *http.Request) {
		if err := st.Delete(chi.URLParam(req, "project"), chi.URLParam(req, "name")); err != nil {
			storeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r.Get("/cases/{project}/{name}/cache", func(w http.ResponseWriter, req *http.Request) {
		project, name := chi.URLParam(req, "project"), chi.URLParam(req, "name")
		cache, err := st.GetCache(project, name, platform(req))
		if errors.Is(err, ErrNotFound) {
			if _, gerr := st.Get(project, name); gerr != nil {
				storeErr(w, gerr)
				return
			}
			cache, err = nil, nil // case exists, no cache yet
		}
		if err != nil {
			storeErr(w, err)
			return
		}
		v, err := s.cacheView(project, name, cache)
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	r.Put("/cases/{project}/{name}/cache", func(w http.ResponseWriter, req *http.Request) {
		b, ok := readBody(w, req, 8<<20)
		if !ok {
			return
		}
		var in Cache
		if err := json.Unmarshal(b, &in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", "body must be a cache JSON object")
			return
		}
		project, name := chi.URLParam(req, "project"), chi.URLParam(req, "name")
		cache, err := st.PutCache(project, name, platform(req), in)
		if err != nil {
			storeErr(w, err)
			return
		}
		v, err := s.cacheView(project, name, cache)
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	r.Delete("/cases/{project}/{name}/cache", func(w http.ResponseWriter, req *http.Request) {
		if err := st.DeleteCache(chi.URLParam(req, "project"), chi.URLParam(req, "name"), platform(req)); err != nil {
			storeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	r.Get("/cases/{project}/{name}/cache/history", func(w http.ResponseWriter, req *http.Request) {
		h, err := st.ListHistory(chi.URLParam(req, "project"), chi.URLParam(req, "name"), platform(req))
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, h)
	})
	r.Post("/cases/{project}/{name}/cache/history/{entry}/restore", func(w http.ResponseWriter, req *http.Request) {
		project, name := chi.URLParam(req, "project"), chi.URLParam(req, "name")
		cache, err := st.RestoreHistory(project, name, platform(req), chi.URLParam(req, "entry"))
		if err != nil {
			storeErr(w, err)
			return
		}
		v, err := s.cacheView(project, name, cache)
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	s.mountRuns(r)
	return r
}
