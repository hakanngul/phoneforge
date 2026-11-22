package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"pocketlab/internal/brand"
	"pocketlab/internal/events"
)

const (
	queueSize    = 256
	workerCount  = 4
	logPrefix    = "webhooks:"
	signatureHdr = "X-Pocketlab-Signature"
)

// Payload is the JSON body POSTed to a webhook.
type Payload struct {
	ID      string    `json:"id"`
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Source  string    `json:"source"`
}

// DeliveryStatus records the outcome of the most recent delivery attempt.
type DeliveryStatus struct {
	OK    bool      `json:"ok"`
	Code  int       `json:"code,omitempty"`
	Time  time.Time `json:"time"`
	Error string    `json:"error,omitempty"`
}

type job struct {
	hook Webhook
	ev   events.Event
}

// Manager subscribes to the event bus and delivers events to webhooks.
type Manager struct {
	store  *Store
	client *http.Client
	queue  chan job

	mu     sync.RWMutex
	status map[string]DeliveryStatus
}

// NewManager creates a Manager backed by store. Call Start to begin delivery.
func NewManager(store *Store) *Manager {
	return &Manager{
		store:  store,
		client: &http.Client{Timeout: 5 * time.Second},
		queue:  make(chan job, queueSize),
		status: make(map[string]DeliveryStatus),
	}
}

// Match reports whether ev passes w's Events filter: empty matches all;
// otherwise an entry may equal the level, be a substring of the message, or
// be a path.Match glob of the message (all case-insensitive).
func Match(w Webhook, ev events.Event) bool {
	if len(w.Events) == 0 {
		return true
	}
	msg := strings.ToLower(ev.Message)
	for _, f := range w.Events {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		if f == strings.ToLower(ev.Level) || strings.Contains(msg, f) {
			return true
		}
		if ok, err := path.Match(f, msg); err == nil && ok {
			return true
		}
	}
	return false
}

// Start launches the delivery workers and the event loop. It returns
// immediately; everything stops when ctx is cancelled.
func (m *Manager) Start(ctx context.Context) {
	ch, unsub := events.Subscribe()
	for i := 0; i < workerCount; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-m.queue:
					m.deliver(ctx, j.hook, j.ev)
				}
			}
		}()
	}
	go func() {
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				m.dispatch(ev)
			}
		}
	}()
}

// dispatch enqueues ev for every matching enabled webhook without blocking.
func (m *Manager) dispatch(ev events.Event) {
	// Never forward our own log lines: avoids feedback loops.
	if strings.HasPrefix(ev.Message, logPrefix) {
		return
	}
	for _, w := range m.store.List() {
		if !w.Enabled || !Match(w, ev) {
			continue
		}
		select {
		case m.queue <- job{hook: w, ev: ev}:
		default:
			events.Errorf("%s queue full, dropped event for %q", logPrefix, w.Name)
		}
	}
}

func newEventID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Sign returns hex(HMAC-SHA256(body, secret)).
func Sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Deliver synchronously POSTs ev to w, records and returns the status.
func (m *Manager) Deliver(ctx context.Context, w Webhook, ev events.Event) DeliveryStatus {
	return m.deliver(ctx, w, ev)
}

func (m *Manager) deliver(ctx context.Context, w Webhook, ev events.Event) DeliveryStatus {
	st := DeliveryStatus{Time: time.Now().UTC()}
	body, _ := json.Marshal(Payload{
		ID: newEventID(), Time: ev.Time, Level: ev.Level, Message: ev.Message, Source: "pocketlab",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		st.Error = err.Error()
	} else {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Pocketlab-Webhook/1")
		if w.Secret != "" {
			sig := Sign(body, w.Secret)
			req.Header.Set(signatureHdr, sig)
			req.Header.Set(brand.LegacySignatureHeader, sig) // receivers written before the rename
		}
		resp, err := m.client.Do(req)
		if err != nil {
			st.Error = err.Error()
		} else {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			st.Code = resp.StatusCode
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				st.OK = true
			} else {
				st.Error = fmt.Sprintf("unexpected status %d", resp.StatusCode)
			}
		}
	}
	m.mu.Lock()
	m.status[w.ID] = st
	m.mu.Unlock()
	return st
}

// Test sends a synthetic event to the webhook with the given ID.
func (m *Manager) Test(ctx context.Context, id string) (DeliveryStatus, error) {
	w, ok := m.store.Get(id)
	if !ok {
		return DeliveryStatus{}, ErrNotFound
	}
	return m.deliver(ctx, w, events.Event{
		Time: time.Now().UTC(), Level: "info", Message: "Pocketlab webhook test event",
	}), nil
}

// Status returns the last delivery status for id, if any delivery happened.
func (m *Manager) Status(id string) (DeliveryStatus, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, ok := m.status[id]
	return st, ok
}

// Statuses returns a copy of all recorded delivery statuses keyed by ID.
func (m *Manager) Statuses() map[string]DeliveryStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]DeliveryStatus, len(m.status))
	for k, v := range m.status {
		out[k] = v
	}
	return out
}
