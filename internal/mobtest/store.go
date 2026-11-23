package mobtest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"pocketlab/internal/brand"
)

// Errors returned by Store; routes map them to HTTP statuses.
var (
	// ErrInvalid marks a malformed id, name or body.
	ErrInvalid = errors.New("invalid request")
	// ErrNotFound marks an unknown project or script.
	ErrNotFound = errors.New("not found")
	// ErrExists marks a name that is already taken.
	ErrExists = errors.New("already exists")
)

const (
	metaFile       = ".project.json"
	maxScriptBytes = 1 << 20
)

var (
	idRe         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	scriptNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

// Project is a test project: a directory of scripts.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"createdAt"`
}

// ScriptInfo describes one script file in a project.
type ScriptInfo struct {
	Name       string    `json:"name"`
	Kind       string    `json:"kind"` // "mob" or "json"
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

// Store keeps test projects on disk under a root directory.
type Store struct {
	root string
	mu   sync.Mutex
}

// DefaultRoot is ~/.pocketlab/tests.
func DefaultRoot() (string, error) {
	if _, err := os.UserHomeDir(); err != nil {
		return "", err
	}
	return filepath.Join(brand.Home(), "tests"), nil
}

// NewStore opens (creating if needed) a store rooted at dir.
func NewStore(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	return &Store{root: abs}, nil
}

// Root returns the store's root directory.
func (s *Store) Root() string { return s.root }

// ValidProjectID reports whether id is a legal project id.
func ValidProjectID(id string) bool { return idRe.MatchString(id) && !strings.Contains(id, "..") }

// ValidScriptName reports whether name is a legal script file name:
// [A-Za-z0-9._-], no leading dot, no "..", and a .mob or .json extension.
func ValidScriptName(name string) bool {
	if !scriptNameRe.MatchString(name) || strings.Contains(name, "..") {
		return false
	}
	// Flow cache sidecars (<name>.mobflow.cache*.json) belong to internal/mobflow.
	if strings.Contains(name, ".mobflow.") {
		return false
	}
	return KindOf(name) != ""
}

// KindOf returns "mob" or "json" from a file name, or "".
func KindOf(name string) string {
	switch {
	case strings.HasSuffix(name, ".mob"):
		return "mob"
	case strings.HasSuffix(name, ".json"):
		return "json"
	}
	return ""
}

func slug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	if out == "" {
		out = "project"
	}
	return out
}

func (s *Store) projectDir(id string) (string, error) {
	if !ValidProjectID(id) {
		return "", fmt.Errorf("%w: bad project id", ErrInvalid)
	}
	dir := filepath.Join(s.root, id)
	if filepath.Dir(dir) != s.root {
		return "", fmt.Errorf("%w: bad project id", ErrInvalid)
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%w: project %q", ErrNotFound, id)
	}
	if _, err := os.Lstat(filepath.Join(dir, metaFile)); err != nil {
		return "", fmt.Errorf("%w: project %q", ErrNotFound, id)
	}
	return dir, nil
}

func (s *Store) readMeta(dir string) (Project, error) {
	b, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return Project{}, err
	}
	var p Project
	if err := json.Unmarshal(b, &p); err != nil {
		return Project{}, err
	}
	p.Path = dir
	return p, nil
}

// CreateProject makes a new project directory named after name.
func (s *Store) CreateProject(name string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return Project{}, fmt.Errorf("%w: project name must be 1-100 characters", ErrInvalid)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return Project{}, fmt.Errorf("%w: project name has control characters", ErrInvalid)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	base := slug(name)
	id := base
	for n := 2; ; n++ {
		if _, err := os.Lstat(filepath.Join(s.root, id)); os.IsNotExist(err) {
			break
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
	dir := filepath.Join(s.root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return Project{}, err
	}
	p := Project{ID: id, Name: name, Path: dir, CreatedAt: time.Now().UTC().Truncate(time.Second)}
	b, _ := json.MarshalIndent(p, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, metaFile), b, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return Project{}, err
	}
	return p, nil
}

// ListProjects returns all projects, oldest first.
func (s *Store) ListProjects() ([]Project, error) {
	ents, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	out := []Project{}
	for _, e := range ents {
		if !e.IsDir() || !ValidProjectID(e.Name()) {
			continue
		}
		p, err := s.readMeta(filepath.Join(s.root, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// GetProject returns one project.
func (s *Store) GetProject(id string) (Project, error) {
	dir, err := s.projectDir(id)
	if err != nil {
		return Project{}, err
	}
	p, err := s.readMeta(dir)
	if err != nil {
		return Project{}, fmt.Errorf("%w: project %q", ErrNotFound, id)
	}
	return p, nil
}

// DeleteProject removes a project and all its scripts.
func (s *Store) DeleteProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.projectDir(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (s *Store) scriptPath(projectID, name string) (string, error) {
	dir, err := s.projectDir(projectID)
	if err != nil {
		return "", err
	}
	if !ValidScriptName(name) {
		return "", fmt.Errorf("%w: script names use only A-Z a-z 0-9 . _ - and end in .mob or .json", ErrInvalid)
	}
	p := filepath.Join(dir, name)
	if filepath.Dir(p) != dir {
		return "", fmt.Errorf("%w: bad script name", ErrInvalid)
	}
	return p, nil
}

// ListScripts lists the *.mob and *.json files of a project.
func (s *Store) ListScripts(projectID string) ([]ScriptInfo, error) {
	dir, err := s.projectDir(projectID)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []ScriptInfo{}
	for _, e := range ents {
		if !ValidScriptName(e.Name()) {
			continue
		}
		fi, err := os.Lstat(filepath.Join(dir, e.Name()))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		out = append(out, ScriptInfo{Name: e.Name(), Kind: KindOf(e.Name()), Size: fi.Size(), ModifiedAt: fi.ModTime().UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ReadScript returns a script's text.
func (s *Store) ReadScript(projectID, name string) ([]byte, error) {
	p, err := s.scriptPath(projectID, name)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: script %q", ErrNotFound, name)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("%w: script %q", ErrNotFound, name)
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxScriptBytes+1))
}

// WriteScript creates or replaces a script. created reports a new file.
func (s *Store) WriteScript(projectID, name string, data []byte) (info ScriptInfo, created bool, err error) {
	if len(data) > maxScriptBytes {
		return ScriptInfo{}, false, fmt.Errorf("%w: script exceeds %d bytes", ErrInvalid, maxScriptBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.scriptPath(projectID, name)
	if err != nil {
		return ScriptInfo{}, false, err
	}
	if fi, lerr := os.Lstat(p); lerr == nil {
		if !fi.Mode().IsRegular() {
			return ScriptInfo{}, false, fmt.Errorf("%w: %q is not a regular file", ErrInvalid, name)
		}
	} else {
		created = true
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return ScriptInfo{}, false, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return ScriptInfo{}, false, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return ScriptInfo{}, false, err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return ScriptInfo{}, false, err
	}
	if err := os.Rename(tmpName, p); err != nil {
		os.Remove(tmpName)
		return ScriptInfo{}, false, err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return ScriptInfo{}, false, err
	}
	return ScriptInfo{Name: name, Kind: KindOf(name), Size: fi.Size(), ModifiedAt: fi.ModTime().UTC()}, created, nil
}

// DeleteScript removes a script file.
func (s *Store) DeleteScript(projectID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.scriptPath(projectID, name)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: script %q", ErrNotFound, name)
	}
	return os.Remove(p)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
