package mobtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// MaxHistory is the number of runs kept on disk.
const MaxHistory = 500

var runIDRe = regexp.MustCompile(`^[0-9a-f]{16}-[0-9a-f]{8}$`)

// History is an append-only run log: one JSON file per run under <root>/.runs,
// newest first when listed, capped at MaxHistory.
type History struct {
	dir string
	cap int
	mu  sync.Mutex
}

// NewHistory opens (creating if needed) the history under root.
func NewHistory(root string) (*History, error) {
	dir := filepath.Join(root, ".runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &History{dir: dir, cap: MaxHistory}, nil
}

// Append stores rec (assigning an ID when empty) and prunes the oldest runs.
func (h *History) Append(rec RunRecord) (RunRecord, error) {
	if rec.ID == "" {
		rec.ID = newRunID(rec.StartedAt)
	}
	if !runIDRe.MatchString(rec.ID) {
		return rec, fmt.Errorf("%w: bad run id", ErrInvalid)
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return rec, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	tmp, err := os.CreateTemp(h.dir, ".tmp-*")
	if err != nil {
		return rec, err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return rec, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return rec, err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(h.dir, rec.ID+".json")); err != nil {
		os.Remove(tmp.Name())
		return rec, err
	}
	names := h.names()
	for i := h.cap; i < len(names); i++ { // names are newest first
		_ = os.Remove(filepath.Join(h.dir, names[i]))
	}
	return rec, nil
}

// names returns run file names, newest first.
func (h *History) names() []string {
	ents, err := os.ReadDir(h.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasSuffix(n, ".json") && runIDRe.MatchString(strings.TrimSuffix(n, ".json")) {
			out = append(out, n)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// List returns runs newest first, optionally filtered by project; limit <= 0
// means 50.
func (h *History) List(projectID string, limit int) ([]RunRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > MaxHistory {
		limit = MaxHistory
	}
	h.mu.Lock()
	names := h.names()
	h.mu.Unlock()
	out := []RunRecord{}
	for _, n := range names {
		if len(out) >= limit {
			break
		}
		rec, err := h.read(n)
		if err != nil {
			continue
		}
		if projectID != "" && rec.ProjectID != projectID {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// Get returns one run by id.
func (h *History) Get(id string) (RunRecord, error) {
	if !runIDRe.MatchString(id) {
		return RunRecord{}, fmt.Errorf("%w: run %q", ErrNotFound, id)
	}
	rec, err := h.read(id + ".json")
	if err != nil {
		return RunRecord{}, fmt.Errorf("%w: run %q", ErrNotFound, id)
	}
	return rec, nil
}

func (h *History) read(name string) (RunRecord, error) {
	b, err := os.ReadFile(filepath.Join(h.dir, name))
	if err != nil {
		return RunRecord{}, err
	}
	var rec RunRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return RunRecord{}, err
	}
	return rec, nil
}
