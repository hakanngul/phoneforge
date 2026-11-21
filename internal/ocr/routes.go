package ocr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"phoneforge/internal/device"
)

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: msg, Code: code})
}

type findBody struct {
	Text          string `json:"text"`
	Exact         bool   `json:"exact"`
	Regex         bool   `json:"regex"`
	CaseSensitive bool   `json:"caseSensitive"`
	Index         int    `json:"index"`
	Words         bool   `json:"words"`
}

// Routes returns the OCR HTTP handler (mount under /api/v1/ocr).
func Routes(get func(id string) (device.Device, error)) http.Handler {
	r := chi.NewRouter()

	r.Get("/status", func(w http.ResponseWriter, _ *http.Request) {
		eng, ok := Available()
		writeJSON(w, http.StatusOK, map[string]any{"engine": eng, "available": ok})
	})

	// shoot resolves the device, screenshots it and runs OCR.
	shoot := func(w http.ResponseWriter, req *http.Request, words bool) (device.Device, device.Screenshot, []Result, bool) {
		dev, err := get(chi.URLParam(req, "id"))
		if err != nil {
			if errors.Is(err, device.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "not_found", err.Error())
			} else {
				writeErr(w, http.StatusBadGateway, "device_error", err.Error())
			}
			return nil, device.Screenshot{}, nil, false
		}
		ctx, cancel := context.WithTimeout(req.Context(), 90*time.Second)
		defer cancel()
		shot, err := dev.Screenshot(ctx)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "screenshot_failed", err.Error())
			return nil, shot, nil, false
		}
		res, err := RecognizeOpts(ctx, shot.PNG, Options{Words: words})
		if err != nil {
			code := http.StatusInternalServerError
			if errors.Is(err, ErrUnavailable) {
				code = http.StatusServiceUnavailable
			}
			writeErr(w, code, "ocr_failed", err.Error())
			return nil, shot, nil, false
		}
		if res == nil {
			res = []Result{}
		}
		return dev, shot, res, true
	}

	parse := func(w http.ResponseWriter, req *http.Request, needText bool) (findBody, bool) {
		var b findBody
		raw, err := io.ReadAll(http.MaxBytesReader(w, req.Body, 1<<16))
		if err != nil {
			writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large or unreadable")
			return b, false
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &b); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid_request", "body must be JSON like {\"text\":\"Login\"}")
				return b, false
			}
		}
		if needText && b.Text == "" {
			writeErr(w, http.StatusBadRequest, "invalid_request", "text is required")
			return b, false
		}
		return b, true
	}

	r.Post("/devices/{id}", func(w http.ResponseWriter, req *http.Request) {
		b, ok := parse(w, req, false)
		if !ok {
			return
		}
		_, shot, res, ok := shoot(w, req, b.Words)
		if !ok {
			return
		}
		eng, _ := Available()
		writeJSON(w, http.StatusOK, map[string]any{"engine": eng, "width": shot.Width, "height": shot.Height, "results": res})
	})

	r.Post("/devices/{id}/find", func(w http.ResponseWriter, req *http.Request) {
		b, ok := parse(w, req, true)
		if !ok {
			return
		}
		if _, err := Find(nil, b.Text, FindOptions{Regex: b.Regex}); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		_, shot, res, ok := shoot(w, req, b.Words)
		if !ok {
			return
		}
		matches, _ := Find(res, b.Text, FindOptions{Exact: b.Exact, Regex: b.Regex, CaseSensitive: b.CaseSensitive})
		if matches == nil {
			matches = []Result{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"width": shot.Width, "height": shot.Height, "matches": matches})
	})

	r.Post("/devices/{id}/tap", func(w http.ResponseWriter, req *http.Request) {
		b, ok := parse(w, req, true)
		if !ok {
			return
		}
		if _, err := Find(nil, b.Text, FindOptions{Regex: b.Regex}); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		dev, shot, res, ok := shoot(w, req, b.Words)
		if !ok {
			return
		}
		matches, _ := Find(res, b.Text, FindOptions{Exact: b.Exact, Regex: b.Regex, CaseSensitive: b.CaseSensitive})
		if len(matches) == 0 {
			writeErr(w, http.StatusNotFound, "text_not_found", "no on-screen text matched "+b.Text)
			return
		}
		if b.Index < 0 || b.Index >= len(matches) {
			writeErr(w, http.StatusBadRequest, "invalid_request", "index out of range; matches: "+itoa(len(matches)))
			return
		}
		m := matches[b.Index]
		x, y := m.Bounds.Center()
		// OCR boxes are in screenshot pixels; taps use the device's logical space.
		sx, sy := Scale(shot.PNG, shot.Width, shot.Height)
		x, y = int(float64(x)*sx+0.5), int(float64(y)*sy+0.5)
		if err := dev.Tap(req.Context(), x, y); err != nil {
			writeErr(w, http.StatusBadGateway, "tap_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tapped": m, "x": x, "y": y, "matches": len(matches)})
	})

	return r
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
