package mobflow

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

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
	// platform is the cache platform of a request: the ?platform= query, else
	// the platform the case pins (platform-"any" cases keep the default "").
	platform := func(req *http.Request) string {
		if p := req.URL.Query().Get("platform"); p != "" {
			return p
		}
		if e, err := st.Get(chi.URLParam(req, "project"), chi.URLParam(req, "name")); err == nil && e.Case != nil {
			return casePlatform(e.Case)
		}
		return ""
	}

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
	r.Get("/cases/{project}/{name}/lint", func(w http.ResponseWriter, req *http.Request) {
		project, name := chi.URLParam(req, "project"), chi.URLParam(req, "name")
		e, err := st.Get(project, name)
		if err != nil {
			storeErr(w, err)
			return
		}
		if e.Case == nil {
			writeJSON(w, http.StatusOK, struct {
				OK       bool      `json:"ok"`
				Warnings []Warning `json:"warnings"`
			}{false, []Warning{}})
			return
		}
		cache, err := st.GetCache(project, name, platform(req))
		if err != nil && !errors.Is(err, ErrNotFound) {
			storeErr(w, err)
			return
		}
		warnings := Lint(e.Case, cache)
		writeJSON(w, http.StatusOK, struct {
			OK       bool      `json:"ok"`
			Warnings []Warning `json:"warnings"`
		}{len(warnings) == 0, warnings})
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
		// Same content checks as an agent submission: no literal secret
		// values, no undeclared type_secret ids.
		if e, gerr := st.Get(project, name); gerr == nil && e.Case != nil && len(e.Errors) == 0 {
			if verrs, _ := checkAgainstCase(e.Case, in.Segments, s.Secrets); len(verrs) > 0 {
				storeErr(w, verrs)
				return
			}
		}
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
	r.Get("/cases/{project}/{name}/cache/history/{entry}", func(w http.ResponseWriter, req *http.Request) {
		c, err := st.GetHistory(chi.URLParam(req, "project"), chi.URLParam(req, "name"), platform(req), chi.URLParam(req, "entry"))
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Cache *Cache `json:"cache"`
		}{c})
	})
	r.Get("/cases/{project}/{name}/stats", func(w http.ResponseWriter, req *http.Request) {
		project, name := chi.URLParam(req, "project"), chi.URLParam(req, "name")
		if _, err := st.Get(project, name); err != nil {
			storeErr(w, err)
			return
		}
		limit := 0
		if v := req.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				writeErr(w, http.StatusBadRequest, "invalid_request", "limit must be a positive integer")
				return
			}
			limit = n
		}
		stats, err := s.Stats(CaseID(project, name), limit)
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, stats)
	})
	// Restore only re-activates a version the server itself wrote and archived
	// (every write path is checked), so it skips checkAgainstCase.
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
