package webauto

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

func writeJSON(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	_ = json.NewEncoder(rw).Encode(v)
}

func writeErr(rw http.ResponseWriter, code int, err error) {
	writeJSON(rw, code, map[string]string{"error": err.Error()})
}

// Routes returns the web-automation API with its own Manager.
func Routes(adbPath string, getSerial func(deviceID string) (string, error)) http.Handler {
	return NewRoutes(NewManager(adbPath), getSerial)
}

// NewRoutes builds the API around an existing Manager (so the caller can
// CloseAll on shutdown). Mount at /web. Endpoints (under /devices/{id}):
//
//	GET  /targets
//	POST /navigate {targetId?, url}
//	POST /eval     {targetId?, script}
//	POST /click    {targetId?, selector}
//	POST /type     {targetId?, selector, text}
//	POST /wait     {targetId?, selector, timeoutMs}
//	GET  /html?targetId=
//	GET  /screenshot?targetId=   (image/png)
func NewRoutes(m *Manager, getSerial func(deviceID string) (string, error)) http.Handler {
	r := chi.NewRouter()

	type body struct {
		TargetID  string `json:"targetId"`
		URL       string `json:"url"`
		Script    string `json:"script"`
		Selector  string `json:"selector"`
		Text      string `json:"text"`
		TimeoutMs int    `json:"timeoutMs"`
	}

	// withPage resolves the device, decodes the body (when hasBody) and attaches.
	withPage := func(hasBody bool, fn func(ctx context.Context, p *Page, b body) (any, error)) http.HandlerFunc {
		return func(rw http.ResponseWriter, req *http.Request) {
			serial, err := getSerial(chi.URLParam(req, "id"))
			if err != nil {
				writeErr(rw, http.StatusNotFound, err)
				return
			}
			var b body
			if hasBody {
				if err := json.NewDecoder(http.MaxBytesReader(rw, req.Body, 8<<20)).Decode(&b); err != nil {
					writeErr(rw, http.StatusBadRequest, err)
					return
				}
			} else {
				b.TargetID = req.URL.Query().Get("targetId")
			}
			ctx, cancel := context.WithTimeout(req.Context(), 60*time.Second)
			defer cancel()
			p, err := m.Session(serial).Attach(ctx, b.TargetID)
			if err != nil {
				writeErr(rw, http.StatusBadGateway, err)
				return
			}
			out, err := fn(ctx, p, b)
			if err != nil {
				writeErr(rw, http.StatusBadGateway, err)
				return
			}
			writeJSON(rw, http.StatusOK, out)
		}
	}
	ok := map[string]bool{"ok": true}

	r.Get("/devices/{id}/targets", func(rw http.ResponseWriter, req *http.Request) {
		serial, err := getSerial(chi.URLParam(req, "id"))
		if err != nil {
			writeErr(rw, http.StatusNotFound, err)
			return
		}
		ts, err := m.Session(serial).Targets(req.Context())
		if err != nil {
			writeErr(rw, http.StatusBadGateway, err)
			return
		}
		if ts == nil {
			ts = []Target{}
		}
		writeJSON(rw, http.StatusOK, ts)
	})
	r.Post("/devices/{id}/navigate", withPage(true, func(ctx context.Context, p *Page, b body) (any, error) {
		if b.URL == "" {
			return nil, errBad("url required")
		}
		return ok, p.Navigate(ctx, b.URL)
	}))
	r.Post("/devices/{id}/eval", withPage(true, func(ctx context.Context, p *Page, b body) (any, error) {
		v, err := p.Evaluate(ctx, b.Script)
		return map[string]json.RawMessage{"result": v}, err
	}))
	r.Post("/devices/{id}/click", withPage(true, func(ctx context.Context, p *Page, b body) (any, error) {
		return ok, p.Click(ctx, b.Selector)
	}))
	r.Post("/devices/{id}/type", withPage(true, func(ctx context.Context, p *Page, b body) (any, error) {
		return ok, p.Type(ctx, b.Selector, b.Text)
	}))
	r.Post("/devices/{id}/wait", withPage(true, func(ctx context.Context, p *Page, b body) (any, error) {
		return ok, p.WaitFor(ctx, b.Selector, time.Duration(b.TimeoutMs)*time.Millisecond)
	}))
	r.Get("/devices/{id}/html", withPage(false, func(ctx context.Context, p *Page, _ body) (any, error) {
		h, err := p.HTML(ctx)
		return map[string]string{"html": h}, err
	}))
	r.Get("/devices/{id}/screenshot", func(rw http.ResponseWriter, req *http.Request) {
		serial, err := getSerial(chi.URLParam(req, "id"))
		if err != nil {
			writeErr(rw, http.StatusNotFound, err)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 60*time.Second)
		defer cancel()
		p, err := m.Session(serial).Attach(ctx, req.URL.Query().Get("targetId"))
		if err != nil {
			writeErr(rw, http.StatusBadGateway, err)
			return
		}
		png, err := p.Screenshot(ctx)
		if err != nil {
			writeErr(rw, http.StatusBadGateway, err)
			return
		}
		rw.Header().Set("Content-Type", "image/png")
		_, _ = rw.Write(png)
	})
	return r
}

type errBad string

func (e errBad) Error() string { return string(e) }
