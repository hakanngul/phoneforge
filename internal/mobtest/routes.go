package mobtest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"phoneforge/internal/device"
	"phoneforge/internal/dsl"
)

const runTimeout = 10 * time.Minute

// Service ties the project store, the run history and the runner together.
type Service struct {
	Store   *Store
	History *History
	// Secrets resolves type_secret ids for runs (optional).
	Secrets dsl.SecretResolver
}

// NewService opens a service rooted at root (see DefaultRoot).
func NewService(root string) (*Service, error) {
	st, err := NewStore(root)
	if err != nil {
		return nil, err
	}
	h, err := NewHistory(st.Root())
	if err != nil {
		return nil, err
	}
	return &Service{Store: st, History: h}, nil
}

// RunScript runs a stored script on dev and records it in the history.
func (s *Service) RunScript(ctx context.Context, dev device.Device, projectID, name string) (RunRecord, error) {
	src, err := s.Store.ReadScript(projectID, name)
	if err != nil {
		return RunRecord{}, err
	}
	rec, err := RunWith(ctx, dev, src, KindOf(name), RunOptions{
		Secrets: s.Secrets,
		ReadFile: func(n string) ([]byte, error) {
			if KindOf(n) != "mob" {
				return nil, errors.New("only .mob files can be included")
			}
			return s.Store.ReadScript(projectID, n)
		},
	})
	if err != nil {
		return RunRecord{}, err
	}
	rec.ProjectID, rec.Script = projectID, name
	if saved, herr := s.History.Append(rec); herr == nil {
		rec = saved
	}
	return rec, nil
}

type apiError struct {
	Error  string       `json:"error"`
	Code   string       `json:"code"`
	Errors []ParseError `json:"errors,omitempty"`
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
	switch {
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

// slim returns rec without the (large) failure screenshots.
func slim(rec RunRecord) RunRecord {
	rec.Result.Steps = stripShots(rec.Result.Steps)
	return rec
}

func stripShots(in []dsl.StepResult) []dsl.StepResult {
	if in == nil {
		return nil
	}
	out := make([]dsl.StepResult, len(in))
	for i, s := range in {
		s.Screenshot = ""
		s.ThenResults = stripShots(s.ThenResults)
		s.ElseResults = stripShots(s.ElseResults)
		s.BodyResults = stripShots(s.BodyResults)
		out[i] = s
	}
	return out
}

// Routes returns the HTTP handler for projects, scripts, validation and runs.
// resolve maps a device id to a connected device.
func (s *Service) Routes(resolve func(deviceID string) (device.Device, error)) http.Handler {
	r := chi.NewRouter()

	r.Get("/projects", func(w http.ResponseWriter, _ *http.Request) {
		ps, err := s.Store.ListProjects()
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ps)
	})
	r.Post("/projects", func(w http.ResponseWriter, req *http.Request) {
		b, ok := readBody(w, req, 1<<16)
		if !ok {
			return
		}
		var in struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(b, &in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", "body must be JSON like {\"name\":\"My tests\"}")
			return
		}
		p, err := s.Store.CreateProject(in.Name)
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, p)
	})
	r.Delete("/projects/{id}", func(w http.ResponseWriter, req *http.Request) {
		if err := s.Store.DeleteProject(chi.URLParam(req, "id")); err != nil {
			storeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	r.Get("/projects/{id}/scripts", func(w http.ResponseWriter, req *http.Request) {
		list, err := s.Store.ListScripts(chi.URLParam(req, "id"))
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
	r.Get("/projects/{id}/scripts/{name}", func(w http.ResponseWriter, req *http.Request) {
		name := chi.URLParam(req, "name")
		b, err := s.Store.ReadScript(chi.URLParam(req, "id"), name)
		if err != nil {
			storeErr(w, err)
			return
		}
		ct := "text/plain; charset=utf-8"
		if KindOf(name) == "json" {
			ct = "application/json; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(b)
	})
	r.Put("/projects/{id}/scripts/{name}", func(w http.ResponseWriter, req *http.Request) {
		b, ok := readBody(w, req, maxScriptBytes+1)
		if !ok {
			return
		}
		info, created, err := s.Store.WriteScript(chi.URLParam(req, "id"), chi.URLParam(req, "name"), b)
		if err != nil {
			storeErr(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, info)
	})
	r.Delete("/projects/{id}/scripts/{name}", func(w http.ResponseWriter, req *http.Request) {
		if err := s.Store.DeleteScript(chi.URLParam(req, "id"), chi.URLParam(req, "name")); err != nil {
			storeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	r.Post("/projects/{id}/scripts/{name}/run", func(w http.ResponseWriter, req *http.Request) {
		b, ok := readBody(w, req, 1<<16)
		if !ok {
			return
		}
		var in struct {
			DeviceID string `json:"deviceId"`
		}
		if err := json.Unmarshal(b, &in); err != nil || in.DeviceID == "" {
			writeErr(w, http.StatusBadRequest, "invalid_request", "body must be JSON like {\"deviceId\":\"...\"}")
			return
		}
		id, name := chi.URLParam(req, "id"), chi.URLParam(req, "name")
		if _, err := s.Store.ReadScript(id, name); err != nil { // 404/400 before touching the device
			storeErr(w, err)
			return
		}
		dev, err := resolve(in.DeviceID)
		if err != nil {
			if errors.Is(err, device.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "device_not_found", err.Error())
				return
			}
			writeErr(w, http.StatusBadGateway, "device_error", err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), runTimeout)
		defer cancel()
		rec, err := s.RunScript(ctx, dev, id, name)
		if err != nil {
			var pe ParseErrors
			if errors.As(err, &pe) {
				writeJSON(w, http.StatusBadRequest, apiError{Error: "script is invalid: " + pe.Error(), Code: "invalid_script", Errors: pe})
				return
			}
			if errors.Is(err, ErrInvalid) || errors.Is(err, ErrNotFound) {
				storeErr(w, err)
				return
			}
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error(), Code: "invalid_script", Errors: []ParseError{{Message: err.Error()}}})
			return
		}
		writeJSON(w, http.StatusOK, rec)
	})

	r.Post("/validate", func(w http.ResponseWriter, req *http.Request) {
		b, ok := readBody(w, req, maxScriptBytes+1)
		if !ok {
			return
		}
		kind := req.URL.Query().Get("kind")
		switch kind {
		case "":
			kind = DetectKind(b)
		case "mob", "json":
		default:
			writeErr(w, http.StatusBadRequest, "invalid_request", "kind must be mob or json")
			return
		}
		opts := ParseOptions{IgnoreRun: true}
		if pid := req.URL.Query().Get("projectId"); pid != "" {
			if _, err := s.Store.GetProject(pid); err != nil {
				storeErr(w, err)
				return
			}
			opts = ParseOptions{ReadFile: func(n string) ([]byte, error) {
				if KindOf(n) != "mob" {
					return nil, errors.New("only .mob files can be included")
				}
				return s.Store.ReadScript(pid, n)
			}}
		}
		errs := Validate(b, kind, opts)
		writeJSON(w, http.StatusOK, struct {
			OK     bool         `json:"ok"`
			Errors []ParseError `json:"errors"`
		}{len(errs) == 0, errs})
	})

	r.Get("/runs", func(w http.ResponseWriter, req *http.Request) {
		limit := 0
		if v := req.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeErr(w, http.StatusBadRequest, "invalid_request", "limit must be a non-negative integer")
				return
			}
			limit = n
		}
		recs, err := s.History.List(req.URL.Query().Get("projectId"), limit)
		if err != nil {
			storeErr(w, err)
			return
		}
		for i := range recs {
			recs[i] = slim(recs[i])
		}
		writeJSON(w, http.StatusOK, recs)
	})
	r.Get("/runs/{id}", func(w http.ResponseWriter, req *http.Request) {
		rec, err := s.History.Get(chi.URLParam(req, "id"))
		if err != nil {
			storeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rec)
	})
	return r
}

// Validate checks script text of the given kind and returns every problem.
func Validate(script []byte, kind string, opts ParseOptions) []ParseError {
	if kind == "json" {
		if _, err := dsl.Parse(script); err != nil {
			return []ParseError{{Line: jsonErrLine(script, err), Message: strings.TrimPrefix(err.Error(), "dsl: ")}}
		}
		return []ParseError{}
	}
	s, errs := ParseWith(script, opts)
	if len(errs) == 0 {
		if err := dsl.Validate(s); err != nil {
			errs = append(errs, ParseError{Message: strings.TrimPrefix(err.Error(), "dsl: ")})
		}
	}
	if errs == nil {
		errs = []ParseError{}
	}
	return errs
}

func jsonErrLine(src []byte, err error) int {
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	var off int64 = -1
	switch {
	case errors.As(err, &se):
		off = se.Offset
	case errors.As(err, &te):
		off = te.Offset
	}
	if off < 0 {
		return 0
	}
	if off > int64(len(src)) {
		off = int64(len(src))
	}
	return 1 + strings.Count(string(src[:off]), "\n")
}
