package mobflow

import (
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

	"pocketlab/internal/mobtest"
)

// Errors shared with the .mob store so callers can map them uniformly.
var (
	ErrInvalid  = mobtest.ErrInvalid
	ErrNotFound = mobtest.ErrNotFound
	ErrExists   = mobtest.ErrExists
)

const caseExt = ".mobflow"

// nameRe is deliberately stricter than the .mob script names: no dots, so
// "<name>.<platform>" cache and history names are unambiguous.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)

// ValidName reports whether name is a legal case file name (without extension).
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Store keeps .mobflow cases and their cache sidecars next to the .mob test
// projects: <root>/<project>/<name>.mobflow (see cache.go for the sidecars).
// The project directories are the ones managed by mobtest.Store.
type Store struct {
	projects *mobtest.Store
	mu       sync.Mutex

	// Validator is run on every segment accepted by PutCache. NewStore sets
	// ValidateSegment (deterministic DSL rules); tests may replace it.
	Validator SegmentValidator
}

// DefaultRoot is the same root the .mob store uses (~/.pocketlab/tests).
func DefaultRoot() (string, error) { return mobtest.DefaultRoot() }

// NewStore opens a store rooted at dir (creating it if needed).
func NewStore(dir string) (*Store, error) {
	ps, err := mobtest.NewStore(dir)
	if err != nil {
		return nil, err
	}
	return &Store{projects: ps, Validator: ValidateSegment}, nil
}

// Root returns the root directory.
func (s *Store) Root() string { return s.projects.Root() }

// Projects exposes the underlying .mob project store (for project creation).
func (s *Store) Projects() *mobtest.Store { return s.projects }

// CaseID is the id of a case: "<project>/<name>".
func CaseID(project, name string) string { return project + "/" + name }

// SplitCaseID splits "<project>/<name>"; ok is false for anything else.
func SplitCaseID(id string) (project, name string, ok bool) {
	project, name, ok = strings.Cut(id, "/")
	if !ok || !mobtest.ValidProjectID(project) || !ValidName(name) {
		return "", "", false
	}
	return project, name, true
}

// CacheSummary condenses a cache's freshness for list views.
type CacheSummary struct {
	Present  bool   `json:"present"`
	Platform string `json:"platform,omitempty"`
	Version  int    `json:"version,omitempty"`
	Fresh    int    `json:"fresh"`
	Stale    int    `json:"stale"`
	Missing  int    `json:"missing"`
}

// Info is the list-view metadata of a case.
type Info struct {
	ID         string       `json:"id"`
	Project    string       `json:"project"`
	Name       string       `json:"name"`  // file name without extension
	Title      string       `json:"title"` // the case's own name
	BundleID   string       `json:"bundleId,omitempty"`
	Platform   string       `json:"platform,omitempty"`
	Steps      int          `json:"steps"`
	Valid      bool         `json:"valid"`
	ModifiedAt time.Time    `json:"modifiedAt"`
	Cache      CacheSummary `json:"cache"`
}

// Entry is a stored case with its source text. Case is nil when the file
// cannot be parsed; Errors lists every problem of an invalid file.
type Entry struct {
	Info
	Source string           `json:"source"`
	Case   *Case            `json:"case,omitempty"`
	Errors ValidationErrors `json:"errors,omitempty"`
}

func (s *Store) projectDir(project string) (string, error) {
	p, err := s.projects.GetProject(project)
	if err != nil {
		return "", err
	}
	return p.Path, nil
}

func (s *Store) casePath(project, name string) (string, error) {
	dir, err := s.projectDir(project)
	if err != nil {
		return "", err
	}
	if !ValidName(name) {
		return "", fmt.Errorf("%w: case names use only A-Z a-z 0-9 _ -", ErrInvalid)
	}
	return filepath.Join(dir, name+caseExt), nil
}

// casePlatform picks the cache platform for a case: android or ios when the
// case pins one, otherwise "" (the default cache).
func casePlatform(c *Case) string {
	for _, p := range []string{c.Device.Platform, c.App.Platform} {
		if p == "android" || p == "ios" {
			return p
		}
	}
	return ""
}

func (s *Store) info(project, name string, fi os.FileInfo, c *Case, errs ValidationErrors) Info {
	in := Info{
		ID: CaseID(project, name), Project: project, Name: name,
		Valid: c != nil && len(errs) == 0, ModifiedAt: fi.ModTime().UTC(),
	}
	if c != nil {
		in.Title, in.BundleID, in.Platform, in.Steps = c.Name, c.App.BundleID, casePlatform(c), len(c.Steps)
		if in.Valid {
			in.Cache = s.cacheSummary(project, name, c)
		}
	}
	return in
}

func (s *Store) read(project, name string) (Entry, error) {
	p, err := s.casePath(project, name)
	if err != nil {
		return Entry{}, err
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return Entry{}, fmt.Errorf("%w: case %q", ErrNotFound, CaseID(project, name))
	}
	f, err := os.Open(p)
	if err != nil {
		return Entry{}, fmt.Errorf("%w: case %q", ErrNotFound, CaseID(project, name))
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCaseBytes+1))
	if err != nil {
		return Entry{}, err
	}
	c, errs := Parse(b)
	return Entry{Info: s.info(project, name, fi, c, errs), Source: string(b), Case: c, Errors: errs}, nil
}

// Get returns one case. A file that exists but is invalid is returned with its
// Errors set rather than as an error.
func (s *Store) Get(project, name string) (Entry, error) { return s.read(project, name) }

// List returns the cases of one project, or of every project when project is
// empty, sorted by id. The Source and Case fields are not filled.
func (s *Store) List(project string) ([]Info, error) {
	var ids []string
	if project != "" {
		if _, err := s.projectDir(project); err != nil {
			return nil, err
		}
		ids = []string{project}
	} else {
		ps, err := s.projects.ListProjects()
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			ids = append(ids, p.ID)
		}
	}
	out := []Info{}
	for _, pid := range ids {
		dir, err := s.projectDir(pid)
		if err != nil {
			continue
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			name, ok := strings.CutSuffix(e.Name(), caseExt)
			if !ok || !ValidName(name) {
				continue
			}
			en, err := s.read(pid, name)
			if err != nil {
				continue
			}
			out = append(out, en.Info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Save validates and atomically writes a case. src may be YAML or JSON; JSON is
// stored as YAML. created reports a new file. Validation problems are returned
// as ValidationErrors (check with errors.As); nothing is written then.
func (s *Store) Save(project, name string, src []byte) (e Entry, created bool, err error) {
	p, err := s.casePath(project, name)
	if err != nil {
		return Entry{}, false, err
	}
	c, errs := Parse(src)
	if len(errs) > 0 {
		return Entry{}, false, errs
	}
	if isJSON(src) {
		if src, err = Marshal(c); err != nil {
			return Entry{}, false, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if fi, lerr := os.Lstat(p); lerr == nil {
		if !fi.Mode().IsRegular() {
			return Entry{}, false, fmt.Errorf("%w: %q is not a regular file", ErrInvalid, name)
		}
	} else {
		created = true
	}
	if err := writeAtomic(p, src); err != nil {
		return Entry{}, false, err
	}
	e, err = s.read(project, name)
	return e, created, err
}

// Delete removes a case together with its cache sidecars and history.
func (s *Store) Delete(project, name string) error {
	p, err := s.casePath(project, name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: case %q", ErrNotFound, CaseID(project, name))
	}
	if err := os.Remove(p); err != nil {
		return err
	}
	dir := filepath.Dir(p)
	var errs []error
	for _, plat := range cachePlatforms {
		if err := os.Remove(cachePath(dir, name, plat)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
		if err := os.RemoveAll(historyDir(dir, name, plat)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func isJSON(b []byte) bool {
	t := strings.TrimLeft(string(b), " \t\r\n\uFEFF")
	return strings.HasPrefix(t, "{")
}

// writeAtomic writes data to path via a temp file in the same directory.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
