package aiconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"phoneforge/internal/agent/llm"
	"strings"

	"github.com/go-chi/chi/v5"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func builtinDefaultModel(provider string) string {
	switch provider {
	case llm.ProviderAnthropic:
		if v := strings.TrimSpace(os.Getenv("ANTHROPIC_MODEL")); v != "" {
			return v
		}
		return llm.DefaultAnthropicModel
	case llm.ProviderOllama:
		if v := strings.TrimSpace(os.Getenv("OLLAMA_MODEL")); v != "" {
			return v
		}
		return llm.DefaultOllamaModel
	}
	return ""
}

// Routes returns the handler to mount at /api/v1/ai.
func (m *Manager) Routes() http.Handler {
	r := chi.NewRouter()

	r.Get("/agents", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"agents":  m.DetectCached(req.Context(), req.URL.Query().Get("refresh") == "1"),
			"mcpUrl":  m.mcpURL,
			"mcpName": MCPName,
		})
	})

	// Which LLM the built-in agent would use right now.
	r.Get("/builtin", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"provider":     m.builtinProvider(),
			"defaultModel": builtinDefaultModel(m.builtinProvider()),
		})
	})

	// Models installed on the local Ollama server (empty list when unreachable).
	r.Get("/ollama/models", func(w http.ResponseWriter, req *http.Request) {
		host := m.ollamaHost()
		models, err := m.ollamaModels(req.Context(), host)
		if models == nil {
			models = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"host": host, "reachable": err == nil, "models": models})
	})

	r.Get("/settings", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, m.Settings())
	})
	r.Put("/settings", func(w http.ResponseWriter, req *http.Request) {
		var in Settings
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := m.SetSettings(in); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, m.Settings())
	})

	action := func(fn func(*Manager, *http.Request, string) error, done string) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			name := chi.URLParam(req, "name")
			if _, ok := specFor(name); !ok {
				writeErr(w, http.StatusNotFound, errUnknownAgent)
				return
			}
			if err := fn(m, req, name); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": done})
		}
	}
	r.Post("/agents/{name}/connect", action(func(m *Manager, r *http.Request, n string) error { return m.Connect(r.Context(), n) }, "connected"))
	r.Post("/agents/{name}/disconnect", action(func(m *Manager, r *http.Request, n string) error { return m.Disconnect(r.Context(), n) }, "disconnected"))

	r.Post("/runs", func(w http.ResponseWriter, req *http.Request) {
		var in RunRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		id, err := m.StartRun(in)
		if errors.Is(err, ErrBuiltin) {
			writeJSON(w, http.StatusOK, map[string]any{"builtin": true, "id": ""})
			return
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "builtin": false})
	})

	r.Get("/runs/{id}/events", func(w http.ResponseWriter, req *http.Request) {
		run := m.getRun(chi.URLParam(req, "id"))
		if run == nil {
			writeErr(w, http.StatusNotFound, errors.New("run not found"))
			return
		}
		fl, ok := w.(http.Flusher)
		if !ok {
			writeErr(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		next := 0
		if le := req.Header.Get("Last-Event-ID"); le != "" {
			var n int
			if _, err := fmt.Sscanf(le, "%d", &n); err == nil && n >= 0 {
				next = n + 1
			}
		}
		for {
			evs, done, ch := run.snapshot(next)
			for _, e := range evs {
				b, _ := json.Marshal(e)
				fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Type, strings.TrimSpace(string(b)))
				next = e.Seq + 1
			}
			fl.Flush()
			if done && len(evs) == 0 {
				return
			}
			if len(evs) > 0 {
				continue
			}
			select {
			case <-ch:
			case <-req.Context().Done():
				return
			}
		}
	})

	r.Post("/runs/{id}/cancel", func(w http.ResponseWriter, req *http.Request) {
		if !m.CancelRun(chi.URLParam(req, "id")) {
			writeErr(w, http.StatusNotFound, errors.New("run not found"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	return r
}
