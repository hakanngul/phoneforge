package mobflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Cache sidecars hold the agent-recorded deterministic DSL of a case.
//
// One cache per platform: the default sidecar is
// <project>/<name>.mobflow.cache.json. A platform-specific sidecar
// <project>/<name>.mobflow.cache.<platform>.json (platform = android|ios) may
// exist beside it; lookups for a platform try the specific file first and fall
// back to the default. Writes always target exactly the file of the requested
// platform ("" = default). Superseded versions are archived to
// <project>/.cache.history/<name>/<timestamp>-v<N>.json (for a platform file the
// directory is <name>.<platform>) and only the last historyKeep are kept.

const historyKeep = 10

// cachePlatforms lists the sidecar variants: "" is the default one.
var cachePlatforms = []string{"", "android", "ios"}

var historyEntryRe = regexp.MustCompile(`^(\d{8}T\d{6}\.\d{3})-v(\d+)$`)

// Cache states reported by Staleness.
const (
	StateFresh   = "fresh"
	StateStale   = "stale"
	StateMissing = "missing"
)

// Segment is the recorded DSL for one case step.
type Segment struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Title      string            `json:"title,omitempty"`
	Steps      []json.RawMessage `json:"steps"` // raw DSL step objects
	StepHash   string            `json:"stepHash"`
	RecordedAt time.Time         `json:"recordedAt"`
	HealedAt   *time.Time        `json:"healedAt,omitempty"`
}

// Cache is the content of one sidecar file.
type Cache struct {
	Version   int       `json:"version"`
	CaseHash  string    `json:"caseHash"`
	Platform  string    `json:"platform,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
	Segments  []Segment `json:"segments"`
}

// SegmentValidator is the extension point for validating a segment's DSL
// (slice 3 plugs the deterministic dsl validation in). A non-nil error rejects
// the cache write.
type SegmentValidator func(seg Segment, step Step) error

// HistoryEntry describes one archived cache version.
type HistoryEntry struct {
	ID         string    `json:"id"`
	Version    int       `json:"version"`
	ArchivedAt time.Time `json:"archivedAt"`
}

func cachePath(dir, name, platform string) string {
	if platform == "" {
		return filepath.Join(dir, name+caseExt+".cache.json")
	}
	return filepath.Join(dir, name+caseExt+".cache."+platform+".json")
}

func historyDir(dir, name, platform string) string {
	if platform != "" {
		name += "." + platform
	}
	return filepath.Join(dir, ".cache.history", name)
}

func checkPlatform(p string) error {
	if p == "" || p == "android" || p == "ios" {
		return nil
	}
	return fmt.Errorf("%w: platform must be android or ios", ErrInvalid)
}

func readCacheFile(path, platform string) (*Cache, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var c Cache
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("cache %s is corrupt: %w", filepath.Base(path), err)
	}
	c.Platform = platform
	if c.Segments == nil {
		c.Segments = []Segment{}
	}
	return &c, nil
}

// GetCache returns the cache for platform ("" = default only; android|ios =
// platform-specific first, then the default). The returned Cache.Platform names
// the sidecar that was used.
func (s *Store) GetCache(project, name, platform string) (*Cache, error) {
	if err := checkPlatform(platform); err != nil {
		return nil, err
	}
	p, err := s.casePath(project, name)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(p)
	order := []string{""}
	if platform != "" {
		order = []string{platform, ""}
	}
	for _, plat := range order {
		c, err := readCacheFile(cachePath(dir, name, plat), plat)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		return c, err
	}
	return nil, fmt.Errorf("%w: no cache for %q", ErrNotFound, CaseID(project, name))
}

func (s *Store) cacheSummary(project, name string, c *Case) CacheSummary {
	cache, err := s.GetCache(project, name, casePlatform(c))
	if err != nil {
		return CacheSummary{}
	}
	sum := CacheSummary{Present: true, Platform: cache.Platform, Version: cache.Version}
	for _, st := range Staleness(c, cache) {
		switch st {
		case StateFresh:
			sum.Fresh++
		case StateStale:
			sum.Stale++
		default:
			sum.Missing++
		}
	}
	return sum
}

// Staleness reports, for every step of c, whether its cached segment is fresh,
// stale (step content changed, or the caseHash differs so everything is
// stale) or missing (no segment). A nil cache makes every step missing.
func Staleness(c *Case, cache *Cache) map[string]string {
	out := make(map[string]string, len(c.Steps))
	if cache == nil {
		for _, st := range c.Steps {
			out[st.ID] = StateMissing
		}
		return out
	}
	if cache.CaseHash != CaseHash(c) {
		for _, st := range c.Steps {
			out[st.ID] = StateStale
		}
		return out
	}
	segs := make(map[string]Segment, len(cache.Segments))
	for _, sg := range cache.Segments {
		segs[sg.ID] = sg
	}
	hashes := c.StepHashes()
	for _, st := range c.Steps {
		switch sg, ok := segs[st.ID]; {
		case !ok:
			out[st.ID] = StateMissing
		case sg.StepHash == hashes[st.ID]:
			out[st.ID] = StateFresh
		default:
			out[st.ID] = StateStale
		}
	}
	return out
}

// validCaseFor loads the stored case and requires it to be valid.
func (s *Store) validCaseFor(project, name string) (*Case, error) {
	e, err := s.read(project, name)
	if err != nil {
		return nil, err
	}
	if e.Case == nil || len(e.Errors) > 0 {
		return nil, fmt.Errorf("%w: case %q is invalid: %s", ErrInvalid, e.ID, e.Errors)
	}
	return e.Case, nil
}

// PutCache validates in against the stored case and writes it as the cache of
// platform ("" = default). The server owns stepHash, caseHash, version and
// updatedAt: client-supplied values are overwritten. A segment's type and
// title default to the step's. The previous version is archived first.
// Validation problems are returned as ValidationErrors with Step = segment id.
func (s *Store) PutCache(project, name, platform string, in Cache) (*Cache, error) {
	if err := checkPlatform(platform); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.validCaseFor(project, name)
	if err != nil {
		return nil, err
	}
	segs, verrs := s.checkSegments(c, in.Segments)
	if len(verrs) > 0 {
		return nil, verrs
	}
	dir, _ := s.projectDir(project)
	path := cachePath(dir, name, platform)
	prev, perr := readCacheFile(path, platform)
	next := 1
	switch {
	case perr == nil:
		if err := archive(dir, name, platform, prev); err != nil {
			return nil, err
		}
		next = prev.Version + 1
	case !errors.Is(perr, ErrNotFound):
		return nil, perr
	}
	out := &Cache{
		Version: next, CaseHash: CaseHash(c), Platform: platform,
		UpdatedAt: time.Now().UTC().Truncate(time.Millisecond), Segments: segs,
	}
	if err := writeCache(path, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) checkSegments(c *Case, in []Segment) ([]Segment, ValidationErrors) {
	var errs ValidationErrors
	add := func(id, msg string, a ...any) {
		errs = append(errs, ValidationError{Step: id, Field: "segments", Message: fmt.Sprintf(msg, a...)})
	}
	hashes := c.StepHashes()
	seen := map[string]bool{}
	now := time.Now().UTC().Truncate(time.Millisecond)
	out := make([]Segment, 0, len(in))
	for i, sg := range in {
		label := sg.ID
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
		}
		step, ok := c.Step(sg.ID)
		switch {
		case !ok:
			add(label, "segment id does not match any case step")
			continue
		case seen[sg.ID]:
			add(label, "duplicate segment")
			continue
		}
		seen[sg.ID] = true
		if len(sg.Steps) == 0 {
			add(label, "segment has no steps")
		}
		for j, raw := range sg.Steps {
			var obj map[string]json.RawMessage
			if json.Unmarshal(raw, &obj) != nil || obj == nil {
				add(label, "steps[%d] must be a JSON object", j)
			}
		}
		if sg.Type != "" && sg.Type != step.Type {
			add(label, "segment type %q does not match step type %q", sg.Type, step.Type)
		}
		sg.Type = step.Type
		if sg.Title == "" {
			sg.Title = step.Title
		}
		sg.StepHash = hashes[sg.ID] // server-owned
		if sg.RecordedAt.IsZero() {
			sg.RecordedAt = now
		}
		if s.Validator != nil {
			if err := s.Validator(sg, step); err != nil {
				add(label, "%s", err)
			}
		}
		out = append(out, sg)
	}
	return out, errs
}

func writeCache(path string, c *Cache) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'))
}

// archive copies cache into the history directory and prunes it.
func archive(dir, name, platform string, cache *Cache) error {
	hd := historyDir(dir, name, platform)
	if err := os.MkdirAll(hd, 0o700); err != nil {
		return err
	}
	id := fmt.Sprintf("%s-v%d", time.Now().UTC().Format("20060102T150405.000"), cache.Version)
	for n := 1; ; n++ { // never overwrite an entry archived in the same millisecond
		if _, err := os.Lstat(filepath.Join(hd, id+".json")); err != nil {
			break
		}
		id = fmt.Sprintf("%s-v%d", time.Now().UTC().Add(time.Duration(n)*time.Millisecond).Format("20060102T150405.000"), cache.Version)
	}
	if err := writeCache(filepath.Join(hd, id+".json"), cache); err != nil {
		return err
	}
	return prune(hd)
}

func prune(hd string) error {
	ents, err := listHistoryDir(hd)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents[min(historyKeep, len(ents)):] { // newest first
		if err := os.Remove(filepath.Join(hd, e.ID+".json")); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func listHistoryDir(hd string) ([]HistoryEntry, error) {
	ents, err := os.ReadDir(hd)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []HistoryEntry{}, nil
		}
		return nil, err
	}
	out := []HistoryEntry{}
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		m := historyEntryRe.FindStringSubmatch(id)
		if m == nil {
			continue
		}
		ts, err := time.Parse("20060102T150405.000", m[1])
		if err != nil {
			continue
		}
		v, _ := strconv.Atoi(m[2])
		out = append(out, HistoryEntry{ID: id, Version: v, ArchivedAt: ts.UTC()})
	}
	sort.Slice(out, func(i, j int) bool { // newest first
		if !out[i].ArchivedAt.Equal(out[j].ArchivedAt) {
			return out[i].ArchivedAt.After(out[j].ArchivedAt)
		}
		return out[i].Version > out[j].Version
	})
	return out, nil
}

// ListHistory returns the archived versions of a cache, newest first.
func (s *Store) ListHistory(project, name, platform string) ([]HistoryEntry, error) {
	if err := checkPlatform(platform); err != nil {
		return nil, err
	}
	p, err := s.casePath(project, name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(p); err != nil {
		return nil, fmt.Errorf("%w: case %q", ErrNotFound, CaseID(project, name))
	}
	return listHistoryDir(historyDir(filepath.Dir(p), name, platform))
}

// GetHistory returns the content of one archived cache version.
func (s *Store) GetHistory(project, name, platform, entry string) (*Cache, error) {
	if err := checkPlatform(platform); err != nil {
		return nil, err
	}
	if !historyEntryRe.MatchString(entry) {
		return nil, fmt.Errorf("%w: bad history entry id", ErrInvalid)
	}
	p, err := s.casePath(project, name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(p); err != nil {
		return nil, fmt.Errorf("%w: case %q", ErrNotFound, CaseID(project, name))
	}
	c, err := readCacheFile(filepath.Join(historyDir(filepath.Dir(p), name, platform), entry+".json"), platform)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%w: history entry %q", ErrNotFound, entry)
	}
	return c, err
}

// RestoreHistory makes an archived version the current cache. The current one
// is archived first and the restored content gets the next version number.
func (s *Store) RestoreHistory(project, name, platform, entry string) (*Cache, error) {
	if err := checkPlatform(platform); err != nil {
		return nil, err
	}
	if !historyEntryRe.MatchString(entry) {
		return nil, fmt.Errorf("%w: bad history entry id", ErrInvalid)
	}
	p, err := s.casePath(project, name)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Lstat(p); err != nil {
		return nil, fmt.Errorf("%w: case %q", ErrNotFound, CaseID(project, name))
	}
	dir := filepath.Dir(p)
	old, err := readCacheFile(filepath.Join(historyDir(dir, name, platform), entry+".json"), platform)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: history entry %q", ErrNotFound, entry)
		}
		return nil, err
	}
	path := cachePath(dir, name, platform)
	next := old.Version + 1
	cur, cerr := readCacheFile(path, platform)
	switch {
	case cerr == nil:
		if err := archive(dir, name, platform, cur); err != nil {
			return nil, err
		}
		next = cur.Version + 1
	case !errors.Is(cerr, ErrNotFound):
		return nil, cerr
	}
	old.Version, old.UpdatedAt = next, time.Now().UTC().Truncate(time.Millisecond)
	if err := writeCache(path, old); err != nil {
		return nil, err
	}
	return old, nil
}

// DeleteCache removes the sidecar of platform ("" = default). The removed
// version is archived so it can be restored.
func (s *Store) DeleteCache(project, name, platform string) error {
	if err := checkPlatform(platform); err != nil {
		return err
	}
	p, err := s.casePath(project, name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Dir(p)
	path := cachePath(dir, name, platform)
	cur, err := readCacheFile(path, platform)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: no cache for %q", ErrNotFound, CaseID(project, name))
		}
		return err
	}
	if err := archive(dir, name, platform, cur); err != nil {
		return err
	}
	return os.Remove(path)
}
