package mobflow

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"pocketlab/internal/mobtest"
)

// MaxRuns is the number of flow runs kept on disk (same cap as .mob history).
const MaxRuns = mobtest.MaxHistory

const runsDirName = ".flowruns"

var (
	runIDRe  = regexp.MustCompile(`^[0-9a-f]{16}-[0-9a-f]{8}$`)
	fileRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	logLimit = int64(4 << 20)
)

// RunStore keeps runs under <root>/.flowruns/<runId>/: run.json (atomic
// writes), step-<id>.png screenshots and agent.log.
type RunStore struct {
	dir string
	cap int
	mu  sync.Mutex
}

// NewRunStore opens (creating if needed) the runs directory under root.
func NewRunStore(root string) (*RunStore, error) {
	dir := filepath.Join(root, runsDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &RunStore{dir: dir, cap: MaxRuns}, nil
}

func newRunID(t time.Time) string {
	return fmt.Sprintf("%016x-%s", uint64(t.UnixNano()), randHex(4))
}

// Dir returns the directory of run id.
func (s *RunStore) Dir(id string) string { return filepath.Join(s.dir, id) }

// Save writes run.json atomically, creating the run directory on first use.
func (s *RunStore) Save(r *Run) error {
	if !runIDRe.MatchString(r.ID) {
		return fmt.Errorf("%w: bad run id", ErrInvalid)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.Dir(r.ID)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(d, "run.json"), b)
}

// WriteArtifact stores a file in the run directory (name must be a plain file name).
func (s *RunStore) WriteArtifact(id, name string, data []byte) error {
	if !runIDRe.MatchString(id) || !fileRe.MatchString(name) {
		return fmt.Errorf("%w: bad artifact name", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.Dir(id)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(d, name), data)
}

// AppendLog appends one line to agent.log (capped at a few MB).
func (s *RunStore) AppendLog(id, line string) error {
	if !runIDRe.MatchString(id) {
		return fmt.Errorf("%w: bad run id", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.Dir(id)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	p := filepath.Join(d, "agent.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > logLimit {
		return nil
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.TrimRight(line, "\r\n") + "\n")
	return err
}

// ArtifactPath resolves a file name to a regular file inside the run
// directory; anything else (separators, dot files, symlinks) is ErrNotFound.
func (s *RunStore) ArtifactPath(id, name string) (string, error) {
	if !runIDRe.MatchString(id) || !fileRe.MatchString(name) {
		return "", fmt.Errorf("%w: artifact %q", ErrNotFound, name)
	}
	p := filepath.Join(s.Dir(id), name)
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%w: artifact %q", ErrNotFound, name)
	}
	return p, nil
}

// Get reads one run.
func (s *RunStore) Get(id string) (*Run, error) {
	if !runIDRe.MatchString(id) {
		return nil, fmt.Errorf("%w: run %q", ErrNotFound, id)
	}
	b, err := os.ReadFile(filepath.Join(s.Dir(id), "run.json"))
	if err != nil {
		return nil, fmt.Errorf("%w: run %q", ErrNotFound, id)
	}
	var r Run
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("run %q is corrupt: %w", id, err)
	}
	return &r, nil
}

func (s *RunStore) ids() []string {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && runIDRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// List returns runs newest first, optionally only those of caseID; limit <= 0
// means 50.
func (s *RunStore) List(caseID string, limit int) []*Run {
	if limit <= 0 {
		limit = 50
	}
	if limit > MaxRuns {
		limit = MaxRuns
	}
	s.mu.Lock()
	ids := s.ids()
	s.mu.Unlock()
	out := []*Run{}
	for _, id := range ids {
		if len(out) >= limit {
			break
		}
		r, err := s.Get(id)
		if err != nil || (caseID != "" && r.CaseID != caseID) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Prune removes the oldest run directories beyond the cap.
func (s *RunStore) Prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.ids()
	for i := s.cap; i < len(ids); i++ {
		_ = os.RemoveAll(s.Dir(ids[i]))
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
