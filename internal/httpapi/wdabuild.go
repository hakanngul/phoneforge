package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"pocketlab/internal/device"
	"pocketlab/internal/events"
	"pocketlab/internal/iosbridge"
)

// writeIssue answers with a structured, user-actionable error:
// {"error": message, "code": CODE, "hint": "...", "lines": [...]}.
func writeIssue(w http.ResponseWriter, status int, is *iosbridge.Issue) {
	writeJSON(w, status, map[string]any{"error": is.Message, "code": is.Code, "hint": is.Hint, "lines": is.Lines})
}

func issueStatus(is *iosbridge.Issue) int {
	switch is.Code {
	case iosbridge.CodeRequiresMac:
		return http.StatusNotImplemented
	case iosbridge.CodeTeamInvalid:
		return http.StatusBadRequest
	}
	return http.StatusConflict
}

// WDABuildRoutes serves the in-app WebDriverAgent build (mounted at /ios/wda):
//
//	GET  /[?quick=1]       environment pre-flight (quick: built flags only) + build status
//	POST /build            {"mode":"device"|"sim","teamId":"ABCDE12345","saveTeam":true} -> 202 status
//	GET  /build?since=N    build status with log lines from index N
//	GET  /build/stream     SSE: "status" events (state/phase/detail) and "log" events
//	POST /build/cancel     stop the running build
//	PUT  /team             {"teamId":"ABCDE12345"} saves the Team ID in settings
func WDABuildRoutes(b *iosbridge.Builder) http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		st := b.StatusSince(-1)
		if req.URL.Query().Get("quick") != "" { // built flags only: no xcodebuild/security calls
			writeJSON(w, http.StatusOK, map[string]any{"env": b.EnvironmentQuick(), "build": st})
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 45*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, map[string]any{"env": b.Environment(ctx), "build": st})
	})
	r.Post("/build", func(w http.ResponseWriter, req *http.Request) {
		var o iosbridge.BuildOptions
		if req.Body != nil {
			if err := json.NewDecoder(io.LimitReader(req.Body, 1<<14)).Decode(&o); err != nil && !errors.Is(err, io.EOF) {
				writeErr(w, "BAD_REQUEST", http.StatusBadRequest, err.Error())
				return
			}
		}
		if o.TeamID != "" {
			o.SaveTeam = true // an explicitly entered team is remembered
		}
		st, err := b.Start(o)
		if err != nil {
			var is *iosbridge.Issue
			if errors.As(err, &is) {
				writeIssue(w, issueStatus(is), is)
				return
			}
			writeErr(w, "BAD_REQUEST", http.StatusBadRequest, err.Error())
			return
		}
		events.Info("WDA build started (" + st.Mode + ")")
		writeJSON(w, http.StatusAccepted, st)
	})
	r.Get("/build", func(w http.ResponseWriter, req *http.Request) {
		since := -1
		if v := req.URL.Query().Get("since"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "since must be an integer")
				return
			}
			since = n
		}
		writeJSON(w, http.StatusOK, b.StatusSince(since))
	})
	r.Get("/build/stream", func(w http.ResponseWriter, req *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			writeErr(w, "INTERNAL", http.StatusInternalServerError, "streaming unsupported")
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		next := 0
		lastKey := ""
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			st := b.StatusSince(next)
			for _, l := range st.Lines {
				j, _ := json.Marshal(l)
				fmt.Fprintf(w, "event: log\ndata: %s\n\n", j)
			}
			next = st.Next
			st.Lines = nil
			key := st.State + "|" + st.Phase + "|" + st.Detail
			if key != lastKey {
				lastKey = key
				j, _ := json.Marshal(st)
				fmt.Fprintf(w, "event: status\ndata: %s\n\n", j)
			}
			fl.Flush()
			if st.State != iosbridge.BuildRunning {
				return
			}
			select {
			case <-req.Context().Done():
				return
			case <-tick.C:
			}
		}
	})
	r.Post("/build/cancel", func(w http.ResponseWriter, req *http.Request) {
		if !b.Cancel() {
			writeErr(w, "NOT_RUNNING", http.StatusConflict, "no build is running")
			return
		}
		writeJSON(w, http.StatusAccepted, b.Status())
	})
	r.Put("/team", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			TeamID string `json:"teamId"`
		}
		if err := json.NewDecoder(io.LimitReader(req.Body, 1<<12)).Decode(&body); err != nil {
			writeErr(w, "BAD_REQUEST", http.StatusBadRequest, err.Error())
			return
		}
		id, err := b.SaveTeam(body.TeamID)
		if err != nil {
			var is *iosbridge.Issue
			if errors.As(err, &is) {
				writeIssue(w, issueStatus(is), is)
				return
			}
			writeErr(w, "INTERNAL", http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"teamId": id})
	})
	return r
}

// iosBridgeReadiness: GET /devices/{id}/iosbridge/readiness probes pairing/trust
// and Developer Mode on the phone (read-only).
func (a *api) iosBridgeReadiness(w http.ResponseWriter, r *http.Request, dev device.Device) {
	udid, ok := a.udidFor(dev)
	if !ok {
		notIOS(w)
		return
	}
	if a.ios.isSim(udid) {
		writeJSON(w, http.StatusOK, iosbridge.Readiness{Reachable: true})
		return
	}
	check := a.ios.Readiness
	if check == nil {
		check = iosbridge.CheckReadiness
	}
	writeJSON(w, http.StatusOK, check(udid))
}

// issueFromErr returns a structured issue for err when it is one or can be
// recognised as a phone-side/signing problem.
func issueFromErr(err error) *iosbridge.Issue {
	var is *iosbridge.Issue
	if errors.As(err, &is) {
		return is
	}
	return iosbridge.ClassifyInstallError(err)
}
