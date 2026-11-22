package slim

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is the sidecar entry for one slimmed simulator.
type Record struct {
	UDID           string    `json:"udid"`
	Profile        string    `json:"profile"`
	ProfileVersion int       `json:"profileVersion"`
	RuntimeID      string    `json:"runtimeId"`
	Groups         []string  `json:"groups"`
	Disabled       []string  `json:"disabled"`
	Apps           []string  `json:"apps,omitempty"`
	AppliedAt      time.Time `json:"appliedAt"`
	BaselineMB     float64   `json:"baselineMB,omitempty"`
	SlimMB         float64   `json:"slimMB,omitempty"`
}

// Store persists Records in ~/.pocketlab/iossim-slim.json (separate from
// iossim-state.json). An empty path keeps them in memory.
type Store struct {
	mu   sync.Mutex
	path string
	recs map[string]Record
}

// NewStore loads path (missing or corrupt files start empty).
func NewStore(path string) *Store {
	s := &Store{path: path, recs: map[string]Record{}}
	if path == "" {
		return s
	}
	var f struct {
		Sims map[string]Record `json:"sims"`
	}
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &f) == nil {
		for k, v := range f.Sims {
			s.recs[k] = v
		}
	}
	return s
}

// Get returns the record for udid.
func (s *Store) Get(udid string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[udid]
	return r, ok
}

// Put stores r.
func (s *Store) Put(r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs[r.UDID] = r
	return s.saveLocked()
}

// Delete removes the record for udid.
func (s *Store) Delete(udid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.recs[udid]; !ok {
		return nil
	}
	delete(s.recs, udid)
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(map[string]any{"sims": s.recs}, "", "  ")
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
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ErrNotSlimmed means a simulator has no slim record.
var ErrNotSlimmed = errors.New("slim: simulator is not slimmed")
