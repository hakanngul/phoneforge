package mobflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `version: 1
name: Checkout happy path
app: {bundleId: com.shop.app, platform: any}
device: {platform: any, minOS: "14"}
params:
  email: {default: "qa@example.com"}
  item:  {default: "Blue mug"}
secrets: [shop_password]
steps:
  - {id: s1, type: login,  title: Sign in,          do: "Sign in with ${email} and secret shop_password"}
  - {id: s2, type: act,    title: Add item to cart, do: "Search for ${item}, open it, tap Add to cart", expect: "Cart badge shows 1"}
  - {id: s3, type: assert, title: Cart total,       do: "Open the cart", expect: "A total price is displayed"}
`

func mustParse(t *testing.T, src string) *Case {
	t.Helper()
	c, errs := Parse([]byte(src))
	if len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	return c
}

func hasErr(errs ValidationErrors, step, sub string) bool {
	for _, e := range errs {
		if e.Step == step && strings.Contains(e.Message, sub) {
			return true
		}
	}
	return false
}

func TestParseRoundTrip(t *testing.T) {
	c := mustParse(t, sample)
	if c.Name != "Checkout happy path" || len(c.Steps) != 3 || c.Params["item"].Default != "Blue mug" || c.Device.MinOS != "14" {
		t.Fatalf("unexpected case: %+v", c)
	}
	out, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	c2 := mustParse(t, string(out))
	if CaseHash(c) != CaseHash(c2) || len(c2.Steps) != 3 || c2.Steps[1].Expect != c.Steps[1].Expect {
		t.Fatalf("round trip changed the case: %+v", c2)
	}
}

func TestParseJSON(t *testing.T) {
	c := mustParse(t, sample)
	b, _ := json.Marshal(c)
	c2 := mustParse(t, string(b))
	if CaseHash(c) != CaseHash(c2) {
		t.Fatal("json parse differs")
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name, src, step, want string
	}{
		{"version", strings.Replace(sample, "version: 1", "version: 2", 1), "", "must be 1"},
		{"no name", strings.Replace(sample, "name: Checkout happy path", `name: ""`, 1), "", "required"},
		{"dup id", strings.Replace(sample, "id: s3", "id: s1", 1), "s1", "duplicate"},
		{"bad id", strings.Replace(sample, "id: s3", "id: 's 3'", 1), "s 3", "must match"},
		{"unknown type", strings.Replace(sample, "type: act", "type: zap", 1), "s2", "unknown type"},
		{"no do", strings.Replace(sample, `do: "Open the cart", `, "", 1), "s3", "do"},
		{"assert no expect", strings.Replace(sample, `, expect: "A total price is displayed"`, "", 1), "s3", "expect"},
		{"undeclared param", strings.Replace(sample, "${item}", "${nope}", 1), "s2", "undeclared parameter ${nope}"},
		{"undeclared in expect", strings.Replace(sample, "Cart badge shows 1", "Shows ${ghost}", 1), "s2", "${ghost}"},
		{"bad secret", strings.Replace(sample, "[shop_password]", "['bad id']", 1), "", "not a valid identifier"},
		{"no steps", "version: 1\nname: x\nsteps: []\n", "", "at least one step"},
		{"unknown key", sample + "bogus: 1\n", "", "bogus"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := Parse([]byte(tc.src))
			if len(errs) == 0 {
				t.Fatal("expected errors")
			}
			if !hasErr(errs, tc.step, tc.want) && !strings.Contains(errs.Error(), tc.want) {
				t.Fatalf("want %q in %v", tc.want, errs)
			}
		})
	}
	// All errors are reported, not just the first.
	_, errs := Parse([]byte("version: 3\nname: ''\nsteps:\n  - {id: a, type: act}\n  - {id: a, type: assert, do: x}\n"))
	if len(errs) < 5 {
		t.Fatalf("expected many errors, got %v", errs)
	}
}

func TestHashing(t *testing.T) {
	c := mustParse(t, sample)
	names := c.ParamNames()
	s := c.Steps[1]
	base := StepHash(s, names)
	if base != StepHash(s, names) || len(base) != 64 {
		t.Fatal("hash not deterministic hex sha256")
	}
	titled := s
	titled.Title = "Totally different"
	if StepHash(titled, names) != base {
		t.Error("title edit changed the hash")
	}
	edited := s
	edited.Do += " twice"
	if StepHash(edited, names) == base {
		t.Error("do edit did not change the hash")
	}
	edited = s
	edited.Expect = "other"
	if StepHash(edited, names) == base {
		t.Error("expect edit did not change the hash")
	}
	if StepHash(s, []string{"item"}) != StepHash(s, []string{"zzz", "item", "item"}) {
		t.Error("unreferenced / duplicate param names should not matter")
	}
	if StepHash(Step{Type: "act", Do: "a|b"}, nil) == StepHash(Step{Type: "act", Do: "a", Expect: "b"}, nil) {
		t.Error("field boundary ambiguity")
	}

	ch := CaseHash(c)
	c2 := mustParse(t, strings.Replace(sample, "com.shop.app", "com.other", 1))
	if CaseHash(c2) == ch {
		t.Error("app change must change caseHash")
	}
	c3 := mustParse(t, strings.Replace(sample, "[shop_password]", "[other]", 1))
	if CaseHash(c3) == ch {
		t.Error("secrets change must change caseHash")
	}
	c4 := mustParse(t, strings.Replace(strings.Replace(sample, "id: s2", "id: sx", 1), "id: s3", "id: s2", 1))
	if CaseHash(c4) == ch {
		t.Error("step ids change must change caseHash")
	}
	c5 := mustParse(t, strings.Replace(sample, "title: Sign in", "title: Log in", 1))
	if CaseHash(c5) != ch {
		t.Error("title edit must not change caseHash")
	}
}

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.Projects().CreateProject("Shop")
	if err != nil {
		t.Fatal(err)
	}
	return st, p.ID
}

func TestStoreCRUD(t *testing.T) {
	st, pid := newTestStore(t)
	e, created, err := st.Save(pid, "checkout", []byte(sample))
	if err != nil || !created || e.ID != pid+"/checkout" || !e.Valid || e.Steps != 3 {
		t.Fatalf("save: %+v created=%v err=%v", e, created, err)
	}
	if _, created, err = st.Save(pid, "checkout", []byte(sample)); err != nil || created {
		t.Fatalf("resave: created=%v err=%v", created, err)
	}
	got, err := st.Get(pid, "checkout")
	if err != nil || got.Source != sample || got.Case == nil {
		t.Fatalf("get: %v", err)
	}
	// JSON input is stored as YAML.
	b, _ := json.Marshal(mustParse(t, sample))
	if _, _, err := st.Save(pid, "fromjson", b); err != nil {
		t.Fatal(err)
	}
	if g, _ := st.Get(pid, "fromjson"); strings.HasPrefix(strings.TrimSpace(g.Source), "{") {
		t.Errorf("json was not converted: %q", g.Source)
	}
	list, err := st.List(pid)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	if all, _ := st.List(""); len(all) != 2 {
		t.Errorf("list all: %v", all)
	}
	// Invalid save writes nothing and returns ValidationErrors.
	_, _, err = st.Save(pid, "bad", []byte("version: 1\nname: x\nsteps: []\n"))
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationErrors, got %v", err)
	}
	if _, err := st.Get(pid, "bad"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad case must not exist: %v", err)
	}
	// Traversal and bad names.
	for _, n := range []string{"../x", "a/b", "a.b", "", ".hidden", "a b"} {
		if _, _, err := st.Save(pid, n, []byte(sample)); !errors.Is(err, ErrInvalid) {
			t.Errorf("name %q: want ErrInvalid, got %v", n, err)
		}
	}
	if _, _, err := st.Save("../etc", "x", []byte(sample)); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad project: %v", err)
	}
	if _, _, err := st.Save("nope", "x", []byte(sample)); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: %v", err)
	}
	if err := st.Delete(pid, "checkout"); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete(pid, "checkout"); !errors.Is(err, ErrNotFound) {
		t.Errorf("double delete: %v", err)
	}
}

func TestMobScriptsUnaffected(t *testing.T) {
	st, pid := newTestStore(t)
	if _, _, err := st.Save(pid, "c", []byte(sample)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCache(pid, "c", "", Cache{Segments: []Segment{seg("s1")}}); err != nil {
		t.Fatal(err)
	}
	scripts, err := st.Projects().ListScripts(pid)
	if err != nil || len(scripts) != 0 {
		t.Fatalf("flow files leaked into .mob listing: %v %v", scripts, err)
	}
}

func seg(id string) Segment {
	return Segment{ID: id, StepHash: "client-supplied", Steps: []json.RawMessage{json.RawMessage(`{"action":"tap","text":"x"}`)}}
}

func TestCachePutHistoryRestore(t *testing.T) {
	st, pid := newTestStore(t)
	if _, err := st.PutCache(pid, "c", "", Cache{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cache for missing case: %v", err)
	}
	if _, _, err := st.Save(pid, "c", []byte(sample)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCache(pid, "c", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no cache yet: %v", err)
	}
	c1, err := st.PutCache(pid, "c", "", Cache{Version: 99, CaseHash: "forged", Segments: []Segment{seg("s1"), seg("s2")}})
	if err != nil {
		t.Fatal(err)
	}
	cs := mustParse(t, sample)
	if c1.Version != 1 || c1.CaseHash != CaseHash(cs) || c1.Segments[0].StepHash != StepHash(cs.Steps[0], cs.ParamNames()) {
		t.Fatalf("server must own version/hashes: %+v", c1)
	}
	if c1.Segments[0].Type != "login" || c1.Segments[0].Title != "Sign in" {
		t.Errorf("type/title not defaulted: %+v", c1.Segments[0])
	}
	got, err := st.GetCache(pid, "c", "")
	if err != nil || got.Version != 1 || len(got.Segments) != 2 {
		t.Fatalf("get: %+v %v", got, err)
	}
	for range 14 {
		if _, err := st.PutCache(pid, "c", "", Cache{Segments: []Segment{seg("s1")}}); err != nil {
			t.Fatal(err)
		}
	}
	cur, _ := st.GetCache(pid, "c", "")
	if cur.Version != 15 {
		t.Fatalf("version=%d want 15", cur.Version)
	}
	h, err := st.ListHistory(pid, "c", "")
	if err != nil || len(h) != 10 {
		t.Fatalf("history len=%d err=%v", len(h), err)
	}
	if h[0].Version != 14 || h[9].Version != 5 {
		t.Errorf("history should hold v14..v5, newest first: first=%d last=%d", h[0].Version, h[9].Version)
	}
	files, _ := os.ReadDir(filepath.Join(st.Root(), pid, ".cache.history", "c"))
	if len(files) != 10 {
		t.Errorf("files on disk: %d", len(files))
	}
	// Restore v14 (single s1 segment) -> version 16; current v15 archived.
	r, err := st.RestoreHistory(pid, "c", "", h[0].ID)
	if err != nil || r.Version != 16 {
		t.Fatalf("restore: %+v %v", r, err)
	}
	h2, _ := st.ListHistory(pid, "c", "")
	if len(h2) != 10 || h2[0].Version != 15 {
		t.Errorf("after restore: len=%d first=%d", len(h2), h2[0].Version)
	}
	if _, err := st.RestoreHistory(pid, "c", "", "../../x"); !errors.Is(err, ErrInvalid) {
		t.Errorf("traversal entry: %v", err)
	}
	if _, err := st.RestoreHistory(pid, "c", "", "20000101T000000.000-v1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown entry: %v", err)
	}
	// Delete cache archives then removes; case delete cleans history.
	if err := st.DeleteCache(pid, "c", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCache(pid, "c", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("cache should be gone: %v", err)
	}
	if err := st.Delete(pid, "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), pid, ".cache.history", "c")); !os.IsNotExist(err) {
		t.Errorf("history not deleted: %v", err)
	}
}

func TestCacheSegmentValidation(t *testing.T) {
	st, pid := newTestStore(t)
	if _, _, err := st.Save(pid, "c", []byte(sample)); err != nil {
		t.Fatal(err)
	}
	empty := Segment{ID: "s1"}
	bad := Segment{ID: "s1", Steps: []json.RawMessage{json.RawMessage(`"str"`)}}
	wrongType := seg("s2")
	wrongType.Type = "assert"
	for name, segs := range map[string][]Segment{
		"unknown id":  {seg("zz")},
		"duplicate":   {seg("s1"), seg("s1")},
		"empty steps": {empty},
		"not object":  {bad},
		"wrong type":  {wrongType},
	} {
		_, err := st.PutCache(pid, "c", "", Cache{Segments: segs})
		var ve ValidationErrors
		if !errors.As(err, &ve) {
			t.Errorf("%s: want ValidationErrors, got %v", name, err)
		}
	}
	if _, err := st.GetCache(pid, "c", ""); !errors.Is(err, ErrNotFound) {
		t.Error("rejected puts must not write")
	}
	st.Validator = func(sg Segment, _ Step) error { return errors.New("nope") }
	if _, err := st.PutCache(pid, "c", "", Cache{Segments: []Segment{seg("s1")}}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("validator hook not applied: %v", err)
	}
	if _, err := st.PutCache(pid, "c", "web", Cache{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad platform: %v", err)
	}
}

func TestCachePlatformLookup(t *testing.T) {
	st, pid := newTestStore(t)
	if _, _, err := st.Save(pid, "c", []byte(sample)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCache(pid, "c", "", Cache{Segments: []Segment{seg("s1")}}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetCache(pid, "c", "ios")
	if got == nil || got.Platform != "" {
		t.Fatalf("ios lookup should fall back to default: %+v", got)
	}
	if _, err := st.PutCache(pid, "c", "ios", Cache{Segments: []Segment{seg("s1"), seg("s2")}}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetCache(pid, "c", "ios")
	if got.Platform != "ios" || len(got.Segments) != 2 {
		t.Fatalf("ios-specific expected: %+v", got)
	}
	if got, _ = st.GetCache(pid, "c", "android"); got.Platform != "" || len(got.Segments) != 1 {
		t.Fatalf("android should use default: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), pid, "c.mobflow.cache.ios.json")); err != nil {
		t.Error(err)
	}
	if err := st.Delete(pid, "c"); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(filepath.Join(st.Root(), pid, "c.mobflow*")); len(m) != 0 {
		t.Errorf("leftovers: %v", m)
	}
}

func TestStaleness(t *testing.T) {
	st, pid := newTestStore(t)
	if _, _, err := st.Save(pid, "c", []byte(sample)); err != nil {
		t.Fatal(err)
	}
	c := mustParse(t, sample)
	if s := Staleness(c, nil); s["s1"] != StateMissing || s["s3"] != StateMissing {
		t.Errorf("nil cache: %v", s)
	}
	cache, err := st.PutCache(pid, "c", "", Cache{Segments: []Segment{seg("s1"), seg("s2")}})
	if err != nil {
		t.Fatal(err)
	}
	s := Staleness(c, cache)
	if s["s1"] != StateFresh || s["s2"] != StateFresh || s["s3"] != StateMissing {
		t.Fatalf("initial: %v", s)
	}
	// Edit one step -> only that segment is stale; title edit keeps fresh.
	edited := mustParse(t, strings.Replace(strings.Replace(sample, "tap Add to cart", "tap Buy", 1), "title: Sign in", "title: Hello", 1))
	s = Staleness(edited, cache)
	if s["s1"] != StateFresh || s["s2"] != StateStale || s["s3"] != StateMissing {
		t.Fatalf("after step edit: %v", s)
	}
	// App change -> everything stale.
	moved := mustParse(t, strings.Replace(sample, "com.shop.app", "com.other", 1))
	s = Staleness(moved, cache)
	for id, v := range s {
		if v != StateStale {
			t.Errorf("%s: %s, want stale", id, v)
		}
	}
	// List summary reflects the cache.
	list, _ := st.List(pid)
	if cs := list[0].Cache; !cs.Present || cs.Fresh != 2 || cs.Missing != 1 || cs.Version != 1 {
		t.Errorf("summary: %+v", cs)
	}
}
