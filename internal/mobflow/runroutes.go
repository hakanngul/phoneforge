package mobflow

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func decodeBody(w http.ResponseWriter, req *http.Request, v any) bool {
	b, ok := readBody(w, req, 8<<20)
	if !ok {
		return false
	}
	if len(b) == 0 {
		return true
	}
	if err := json.Unmarshal(b, v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "body must be a JSON object: "+err.Error())
		return false
	}
	return true
}

type stepBody struct {
	Text        string `json:"text"`
	Expectation string `json:"expectation"`
	Passed      *bool  `json:"passed"`
	Detail      string `json:"detail"`
	Notes       string `json:"notes"`
	Result      string `json:"result"`
	Error       string `json:"error"`
}

type cacheBody struct {
	Segments []Segment `json:"segments"`
}

// mountRuns adds the run endpoints (including the agent step endpoints) to r.
func (s *Service) mountRuns(r chi.Router) {
	r.Post("/runs", func(w http.ResponseWriter, req *http.Request) {
		var in StartRunRequest
		if !decodeBody(w, req, &in) {
			return
		}
		run, err := s.StartRun(in)
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"run": run})
	})
	r.Get("/runs", func(w http.ResponseWriter, req *http.Request) {
		limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
		runs, err := s.ListRuns(req.URL.Query().Get("case"), limit)
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, runs)
	})
	r.Get("/runs/{id}", func(w http.ResponseWriter, req *http.Request) {
		run, err := s.GetRun(chi.URLParam(req, "id"))
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})
	r.Get("/runs/{id}/events", func(w http.ResponseWriter, req *http.Request) {
		b, err := s.broker(chi.URLParam(req, "id"))
		if err != nil {
			storeErr(w, err)
			return
		}
		serveSSE(w, req, b)
	})
	r.Get("/runs/{id}/artifacts/{file}", func(w http.ResponseWriter, req *http.Request) {
		rt, err := s.runtime()
		if err != nil {
			storeErr(w, err)
			return
		}
		p, err := rt.store.ArtifactPath(chi.URLParam(req, "id"), chi.URLParam(req, "file"))
		if err != nil {
			storeErr(w, err)
			return
		}
		if ct := mime.TypeByExtension(filepath.Ext(p)); ct != "" {
			w.Header().Set("Content-Type", ct)
		} else {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, req, p)
	})
	r.Post("/runs/{id}/cancel", func(w http.ResponseWriter, req *http.Request) {
		run, err := s.Cancel(chi.URLParam(req, "id"))
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})

	// Agent step endpoints, addressed by run id or (active) by case.
	byRun := func(req *http.Request) (string, error) { return chi.URLParam(req, "id"), nil }
	byCase := func(req *http.Request) (string, error) {
		return s.ActiveRun(chi.URLParam(req, "project"), chi.URLParam(req, "name"))
	}
	for prefix, resolve := range map[string]func(*http.Request) (string, error){
		"/runs/{id}":                     byRun,
		"/cases/{project}/{name}/active": byCase,
	} {
		resolve := resolve
		step := func(h func(runID, stepID string, b stepBody) (*Run, error)) http.HandlerFunc {
			return func(w http.ResponseWriter, req *http.Request) {
				var b stepBody
				if !decodeBody(w, req, &b) {
					return
				}
				id, err := resolve(req)
				if err != nil {
					storeErr(w, err)
					return
				}
				run, err := h(id, chi.URLParam(req, "stepId"), b)
				if err != nil {
					storeErr(w, err)
					return
				}
				writeJSON(w, http.StatusOK, run)
			}
		}
		r.Post(prefix+"/steps/{stepId}/start", step(func(id, sid string, _ stepBody) (*Run, error) {
			return s.StepStart(id, sid)
		}))
		r.Post(prefix+"/steps/{stepId}/substep", step(func(id, sid string, b stepBody) (*Run, error) {
			return s.Substep(id, sid, b.Text)
		}))
		r.Post(prefix+"/steps/{stepId}/assert", step(func(id, sid string, b stepBody) (*Run, error) {
			if b.Passed == nil {
				return nil, fmt.Errorf("%w: passed (true or false) is required", ErrInvalid)
			}
			text := b.Expectation
			if text == "" {
				text = b.Text
			}
			return s.Assert(id, sid, AssertResult{Text: text, Passed: *b.Passed, Detail: b.Detail})
		}))
		r.Post(prefix+"/steps/{stepId}/done", step(func(id, sid string, b stepBody) (*Run, error) {
			if b.Passed == nil {
				return nil, fmt.Errorf("%w: passed (true or false) is required", ErrInvalid)
			}
			notes := b.Notes
			if notes == "" {
				notes = b.Result
			}
			return s.StepDone(id, sid, *b.Passed, notes, b.Error)
		}))
		r.Post(prefix+"/cache", func(w http.ResponseWriter, req *http.Request) {
			var b cacheBody
			if !decodeBody(w, req, &b) {
				return
			}
			id, err := resolve(req)
			if err != nil {
				storeErr(w, err)
				return
			}
			res, err := s.SubmitCache(id, b.Segments)
			if err != nil {
				storeErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, res)
		})
	}
}
