package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const keepAliveInterval = 20 * time.Second

// Handler returns an http.Handler serving the event stream over SSE. It
// replays Recent() events first, then streams live events until the client
// disconnects, sending a ":keep-alive" comment about every 20 seconds.
// Only GET is allowed; other methods get 405. If the ResponseWriter does not
// support flushing, it responds with 500.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		// Subscribe first, then replay, so no event is missed; an event
		// published in between may appear twice, harmless for a log view.
		ch, unsub := Subscribe()
		defer unsub()

		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		for _, e := range Recent() {
			if !writeEvent(w, e) {
				return
			}
		}
		flusher.Flush()

		ticker := time.NewTicker(keepAliveInterval)
		defer ticker.Stop()
		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-ch:
				if !ok || !writeEvent(w, e) {
					return
				}
				flusher.Flush()
			case <-ticker.C:
				if _, err := fmt.Fprint(w, ":keep-alive\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})
}

func writeEvent(w http.ResponseWriter, e Event) bool {
	b, err := json.Marshal(e)
	if err != nil {
		return true
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err == nil
}
