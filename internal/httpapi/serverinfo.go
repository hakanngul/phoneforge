package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"pocketlab/internal/brand"
	"pocketlab/internal/lifecycle"
)

// OriginHeader optionally names who is asking the server to stop (web-ui,
// desktop, cli, ...). A JSON body {"origin": "...", "reason": "..."} works too.
const OriginHeader = "X-Pocketlab-Origin"

// cleanOrigin trims and restricts a client-supplied label so the history stays readable.
func cleanOrigin(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 64 {
			break
		}
	}
	return b.String()
}

// noteShutdown records who asked for the shutdown, before the server stops.
func (a *api) noteShutdown(r *http.Request) {
	if a.life == nil {
		return
	}
	origin := cleanOrigin(brand.HeaderAny(r.Header.Get, OriginHeader, brand.LegacyOriginHeader))
	reason := ""
	if r.Body != nil && r.ContentLength != 0 {
		var body struct {
			Origin string `json:"origin"`
			Reason string `json:"reason"`
		}
		if b, err := io.ReadAll(io.LimitReader(r.Body, 4<<10)); err == nil && json.Unmarshal(b, &body) == nil {
			if origin == "" {
				origin = cleanOrigin(body.Origin)
			}
			reason = cleanOrigin(body.Reason)
		}
	}
	if origin == "" {
		origin = "unknown"
	}
	msg := "shutdown requested via API by " + origin
	if reason != "" {
		msg += " (" + reason + ")"
	}
	ua := r.UserAgent()
	if len(ua) > 60 {
		ua = ua[:60]
	}
	msg += fmt.Sprintf(" [from %s, %s]", r.RemoteAddr, ua)
	a.life.SetStopReason(msg, origin, "")
}

func (a *api) serverStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.life.Status())
}

func (a *api) serverHistory(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if n > 500 {
			n = 500
		}
		limit = n
	}
	ev := a.life.History(limit)
	if ev == nil {
		ev = []lifecycle.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": ev, "file": a.life.Status().EventsFile})
}
