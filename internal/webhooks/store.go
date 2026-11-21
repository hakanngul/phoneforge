// Package webhooks lets users configure external URLs that receive a JSON
// POST whenever PhoneForge publishes an activity event.
package webhooks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned when a webhook ID does not exist.
var ErrNotFound = errors.New("webhook not found")

// Webhook is one configured delivery target.
type Webhook struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	// Events filters deliveries: entries are event levels ("error"), or
	// substrings/globs matched against the message. Empty means all events.
	Events []string `json:"events"`
	// Secret, when non-empty, is used to HMAC-SHA256 sign each body.
	Secret    string    `json:"secret,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

// Patch describes a partial update; nil fields are left unchanged.
type Patch struct {
	Name    *string
	URL     *string
	Events  *[]string
	Secret  *string
	Enabled *bool
}

// Store is a thread-safe, JSON-file-backed collection of webhooks.
type Store struct {
	mu   sync.RWMutex
	path string
	list []Webhook
}

// New opens (or creates lazily) the store at path. A missing file yields an
// empty store.
func New(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.list); err != nil {
		return nil, fmt.Errorf("webhooks: parse %s: %w", path, err)
	}
	return s, nil
}

func clone(w Webhook) Webhook {
	w.Events = append([]string{}, w.Events...)
	return w
}

// ValidateURL checks that raw is an absolute http(s) URL with a host.
func ValidateURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("url must use http or https")
	}
	if u.Host == "" {
		return errors.New("url must include a host")
	}
	return nil
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func cleanEvents(in []string) []string {
	out := []string{}
	for _, e := range in {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// List returns a copy of all webhooks in creation order.
func (s *Store) List() []Webhook {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Webhook, len(s.list))
	for i, w := range s.list {
		out[i] = clone(w)
	}
	return out
}

// Get returns the webhook with the given ID.
func (s *Store) Get(id string) (Webhook, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, w := range s.list {
		if w.ID == id {
			return clone(w), true
		}
	}
	return Webhook{}, false
}

// Add validates and stores a new enabled webhook.
func (s *Store) Add(name, rawURL string, events []string, secret string) (Webhook, error) {
	rawURL = strings.TrimSpace(rawURL)
	if err := ValidateURL(rawURL); err != nil {
		return Webhook{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = rawURL
	}
	w := Webhook{
		ID: newID(), Name: name, URL: rawURL, Events: cleanEvents(events),
		Secret: secret, Enabled: true, CreatedAt: time.Now().UTC(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, w)
	if err := s.saveLocked(); err != nil {
		s.list = s.list[:len(s.list)-1]
		return Webhook{}, err
	}
	return clone(w), nil
}

// Update applies a partial change to a webhook and persists it.
func (s *Store) Update(id string, p Patch) (Webhook, error) {
	if p.URL != nil {
		if err := ValidateURL(*p.URL); err != nil {
			return Webhook{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.list {
		if s.list[i].ID != id {
			continue
		}
		old := clone(s.list[i])
		w := &s.list[i]
		if p.Name != nil {
			w.Name = strings.TrimSpace(*p.Name)
		}
		if p.URL != nil {
			w.URL = strings.TrimSpace(*p.URL)
		}
		if p.Events != nil {
			w.Events = cleanEvents(*p.Events)
		}
		if p.Secret != nil {
			w.Secret = *p.Secret
		}
		if p.Enabled != nil {
			w.Enabled = *p.Enabled
		}
		if err := s.saveLocked(); err != nil {
			s.list[i] = old
			return Webhook{}, err
		}
		return clone(*w), nil
	}
	return Webhook{}, ErrNotFound
}

// Toggle enables or disables a webhook.
func (s *Store) Toggle(id string, enabled bool) (Webhook, error) {
	return s.Update(id, Patch{Enabled: &enabled})
}

// Remove deletes a webhook.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.list {
		if s.list[i].ID == id {
			old := s.list
			s.list = append(append([]Webhook{}, old[:i]...), old[i+1:]...)
			if err := s.saveLocked(); err != nil {
				s.list = old
				return err
			}
			return nil
		}
	}
	return ErrNotFound
}

// saveLocked atomically writes the list to disk. Caller holds s.mu.
func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
