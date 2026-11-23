package mobflow

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event types streamed over SSE.
const (
	EvRun        = "run"
	EvStep       = "step"
	EvSubstep    = "substep"
	EvAssert     = "assert"
	EvScreenshot = "screenshot"
	EvLog        = "log"
	EvDone       = "done"
)

// Event is one run event. Seq is 0-based and doubles as the SSE id.
type Event struct {
	Seq  int       `json:"seq"`
	Type string    `json:"type"`
	Data any       `json:"data"`
	Time time.Time `json:"time"`
}

// broker is the in-memory event log of one run: late subscribers get the
// buffered events first, then the live ones.
type broker struct {
	mu      sync.Mutex
	events  []Event
	done    bool
	changed chan struct{}
}

func newBroker() *broker { return &broker{changed: make(chan struct{})} }

func (b *broker) publish(typ string, data any) {
	b.mu.Lock()
	b.events = append(b.events, Event{Seq: len(b.events), Type: typ, Data: data, Time: time.Now().UTC()})
	if typ == EvDone {
		b.done = true
	}
	ch := b.changed
	b.changed = make(chan struct{})
	b.mu.Unlock()
	close(ch)
}

// snapshot returns the events from index from, whether the stream ended, and
// a channel closed on the next publish.
func (b *broker) snapshot(from int) ([]Event, bool, <-chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Event
	if from < len(b.events) {
		out = append(out, b.events[from:]...)
	}
	return out, b.done, b.changed
}

const sseKeepAlive = 20 * time.Second

func writeSSE(w http.ResponseWriter, e Event) bool {
	b, err := json.Marshal(e.Data)
	if err != nil {
		b = []byte("null")
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Type, b)
	return err == nil
}

// serveSSE streams the events of a broker until the done event or until the
// client disconnects.
func serveSSE(w http.ResponseWriter, r *http.Request, b *broker) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	tick := time.NewTicker(sseKeepAlive)
	defer tick.Stop()
	from := 0
	for {
		evs, done, ch := b.snapshot(from)
		for _, e := range evs {
			if !writeSSE(w, e) {
				return
			}
			from = e.Seq + 1
		}
		fl.Flush()
		if done && len(evs) == 0 {
			return
		}
		if done {
			continue // drain, then the next snapshot ends the stream
		}
		select {
		case <-r.Context().Done():
			return
		case <-ch:
		case <-tick.C:
			if _, err := fmt.Fprint(w, ":keep-alive\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}
