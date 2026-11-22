package brand

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func mapEnv(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLegacyEnvName(t *testing.T) {
	cases := map[string]string{
		"POCKETLAB_HOME": "PHONEFORGE_HOME",
		"PL_TEAM_ID":     "PF_TEAM_ID",
		"HOME":           "",
		"PATH":           "",
	}
	for in, want := range cases {
		if got := LegacyEnvName(in); got != want {
			t.Errorf("LegacyEnvName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLookupPrefersNewThenLegacy(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		key  string
		want string
	}{
		{"new only", map[string]string{"POCKETLAB_ADDR": "new"}, "POCKETLAB_ADDR", "new"},
		{"old only", map[string]string{"PHONEFORGE_ADDR": "old"}, "POCKETLAB_ADDR", "old"},
		{"both -> new", map[string]string{"POCKETLAB_ADDR": "new", "PHONEFORGE_ADDR": "old"}, "POCKETLAB_ADDR", "new"},
		{"empty new -> old", map[string]string{"POCKETLAB_ADDR": "", "PHONEFORGE_ADDR": "old"}, "POCKETLAB_ADDR", "old"},
		{"short old", map[string]string{"PF_WDA_HOME": "/w"}, "PL_WDA_HOME", "/w"},
		{"short both", map[string]string{"PL_WDA_HOME": "/n", "PF_WDA_HOME": "/w"}, "PL_WDA_HOME", "/n"},
		{"unset", map[string]string{}, "PL_WDA_HOME", ""},
		{"non-brand name has no fallback", map[string]string{"PF_X": "x"}, "SOMETHING", ""},
	} {
		if got := Lookup(mapEnv(tc.env), tc.key); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestGetenvFallsBackToLegacy(t *testing.T) {
	t.Setenv("POCKETLAB_TESTVAR", "")
	t.Setenv("PHONEFORGE_TESTVAR", "legacy")
	if got := Getenv("POCKETLAB_TESTVAR"); got != "legacy" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("POCKETLAB_TESTVAR", "fresh")
	if got := Getenv("POCKETLAB_TESTVAR"); got != "fresh" {
		t.Fatalf("got %q", got)
	}
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := logw
	logw = &buf
	t.Cleanup(func() { logw = old })
	return &buf
}

func TestDataDirUnderMigratesOldOnly(t *testing.T) {
	buf := captureLog(t)
	home := t.TempDir()
	writeFile(t, filepath.Join(home, LegacyDirName, "settings.json"), `{"a":1}`)
	writeFile(t, filepath.Join(home, LegacyDirName, "tests", "t1.mob"), "tap x")

	got := DataDirUnder(home)
	if got != filepath.Join(home, DirName) {
		t.Fatalf("dir = %s", got)
	}
	if b, err := os.ReadFile(filepath.Join(got, "settings.json")); err != nil || string(b) != `{"a":1}` {
		t.Fatalf("settings not migrated: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(got, "tests", "t1.mob")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, LegacyDirName)); !os.IsNotExist(err) {
		t.Fatalf("old dir should be renamed away, err=%v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("migration was not logged")
	}
	// Second call is a no-op and logs nothing more.
	buf.Reset()
	DataDirUnder(home)
	if buf.Len() != 0 {
		t.Fatalf("second call logged: %s", buf.String())
	}
}

func TestDataDirUnderBothExistConflictKeepsNew(t *testing.T) {
	buf := captureLog(t)
	home := t.TempDir()
	writeFile(t, filepath.Join(home, LegacyDirName, "settings.json"), "old")
	writeFile(t, filepath.Join(home, DirName, "settings.json"), "new")

	got := DataDirUnder(home)
	if b, _ := os.ReadFile(filepath.Join(got, "settings.json")); string(b) != "new" {
		t.Fatalf("new dir content = %q", b)
	}
	// The conflicting legacy file is archived, never deleted.
	m, _ := filepath.Glob(filepath.Join(home, LegacyDirName+".migrated-*", "settings.json"))
	if len(m) != 1 {
		t.Fatalf("legacy leftover not archived: %v", m)
	}
	if b, _ := os.ReadFile(m[0]); string(b) != "old" {
		t.Fatalf("archived content = %q", b)
	}
	if _, err := os.Stat(filepath.Join(home, LegacyDirName)); !os.IsNotExist(err) {
		t.Fatal("legacy dir should be renamed away")
	}
	if buf.Len() == 0 {
		t.Fatal("expected a log line")
	}
}

func TestMergeOnlyAutoCreatedNew(t *testing.T) {
	captureLog(t)
	home := t.TempDir()
	old, nw := filepath.Join(home, LegacyDirName), filepath.Join(home, DirName)
	for f, c := range map[string]string{
		"aiconfig.json": "ai", "wda/x/y": "wda", "signing/k": "sig", "settings.json": "s",
		"tests/proj/a.mob": "tap", "bin/plocr": "oldbin", "bin/plocr.sha": "oldsha",
		"server-events.jsonl": "old1\nold2\n", "server.state": "oldstate",
	} {
		writeFile(t, filepath.Join(old, f), c)
	}
	writeFile(t, filepath.Join(nw, "bin", "plocr"), "newbin")
	writeFile(t, filepath.Join(nw, "bin", "plocr.warm"), "warm")
	if err := os.MkdirAll(filepath.Join(nw, "tests", ".runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nw, "desktop.json"), "{}")
	writeFile(t, filepath.Join(nw, "server-events.jsonl"), "new1\n")
	writeFile(t, filepath.Join(nw, "server.state"), "newstate")

	DataDirUnder(home)
	want := map[string]string{
		"aiconfig.json": "ai", "wda/x/y": "wda", "signing/k": "sig", "settings.json": "s",
		"tests/proj/a.mob": "tap", "bin/plocr": "oldbin", "bin/plocr.sha": "oldsha",
		"bin/plocr.warm": "warm", "desktop.json": "{}",
		"server-events.jsonl": "old1\nold2\nnew1\n", "server.state": "newstate",
	}
	for f, c := range want {
		if b, err := os.ReadFile(filepath.Join(nw, f)); err != nil || string(b) != c {
			t.Errorf("%s = %q (%v), want %q", f, b, err, c)
		}
	}
	// Only the live server.state conflict is left behind, in the archive.
	m, _ := filepath.Glob(filepath.Join(home, LegacyDirName+".migrated-*", "server.state"))
	if len(m) != 1 {
		t.Fatalf("server.state leftover missing: %v", m)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("legacy dir should be gone")
	}
	// Idempotent: nothing more happens.
	DataDirUnder(home)
	if b, _ := os.ReadFile(filepath.Join(nw, "server-events.jsonl")); string(b) != "old1\nold2\nnew1\n" {
		t.Fatalf("second run changed events: %q", b)
	}
}

func TestMergeFullyMovedRemovesEmptyLegacy(t *testing.T) {
	captureLog(t)
	home := t.TempDir()
	writeFile(t, filepath.Join(home, LegacyDirName, "a.json"), "a")
	writeFile(t, filepath.Join(home, DirName, "b.json"), "b")
	DataDirUnder(home)
	if b, _ := os.ReadFile(filepath.Join(home, DirName, "a.json")); string(b) != "a" {
		t.Fatalf("a.json = %q", b)
	}
	if m, _ := filepath.Glob(filepath.Join(home, LegacyDirName+"*")); len(m) != 0 {
		t.Fatalf("nothing should remain: %v", m)
	}
}

func TestDataDirOverrideSkipsMigration(t *testing.T) {
	captureLog(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POCKETLAB_HOME", filepath.Join(home, "elsewhere"))
	writeFile(t, filepath.Join(home, LegacyDirName, "settings.json"), "old")
	DataDir()
	if _, err := os.Stat(filepath.Join(home, LegacyDirName, "settings.json")); err != nil {
		t.Fatalf("legacy dir must be untouched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, DirName)); !os.IsNotExist(err) {
		t.Fatal("no migration expected")
	}
	if Home() != filepath.Join(home, "elsewhere") {
		t.Fatalf("Home = %s", Home())
	}
}

func TestHomeFollowsHOMEChange(t *testing.T) {
	captureLog(t)
	a, b := t.TempDir(), t.TempDir()
	t.Setenv("POCKETLAB_HOME", "")
	t.Setenv("PHONEFORGE_HOME", "")
	t.Setenv("HOME", a)
	if Home() != filepath.Join(a, DirName) {
		t.Fatal("home a")
	}
	t.Setenv("HOME", b)
	if Home() != filepath.Join(b, DirName) {
		t.Fatalf("stale cached home: %s", Home())
	}
}

func TestDataDirUnderNeitherExist(t *testing.T) {
	captureLog(t)
	home := t.TempDir()
	got := DataDirUnder(home)
	if got != filepath.Join(home, DirName) {
		t.Fatalf("dir = %s", got)
	}
	// Nothing is created by the lookup itself; callers MkdirAll when they write.
	if _, err := os.Stat(filepath.Join(home, LegacyDirName)); !os.IsNotExist(err) {
		t.Fatal("legacy dir must not be created")
	}
	if err := os.MkdirAll(got, 0o700); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(got); err != nil || !st.IsDir() {
		t.Fatal("new dir should be creatable")
	}
}

func TestMigrateCopyFallback(t *testing.T) {
	buf := captureLog(t)
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	writeFile(t, filepath.Join(oldDir, "a", "b.txt"), "hello")
	newDir := filepath.Join(root, "new")
	if err := copyTree(oldDir, newDir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(newDir, "a", "b.txt")); string(b) != "hello" {
		t.Fatalf("copy = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(oldDir, "a", "b.txt")); string(b) != "hello" {
		t.Fatal("copy must leave the source intact")
	}
	_ = buf
}

func TestHomeFromOverrides(t *testing.T) {
	if got := HomeFrom(mapEnv(map[string]string{"POCKETLAB_HOME": "/x"})); got != "/x" {
		t.Fatal(got)
	}
	if got := HomeFrom(mapEnv(map[string]string{"PHONEFORGE_HOME": "/old"})); got != "/old" {
		t.Fatal(got)
	}
	if got := HomeFrom(mapEnv(map[string]string{"POCKETLAB_HOME": "/n", "PHONEFORGE_HOME": "/old"})); got != "/n" {
		t.Fatal(got)
	}
}

func TestHeaderAny(t *testing.T) {
	h := map[string]string{"X-PhoneForge-Action": "shutdown"}
	get := func(k string) string { return h[k] }
	if HeaderAny(get, "X-Pocketlab-Action", LegacyActionHeader) != "shutdown" {
		t.Fatal("legacy header not honoured")
	}
	h["X-Pocketlab-Action"] = "x"
	if HeaderAny(get, "X-Pocketlab-Action", LegacyActionHeader) != "x" {
		t.Fatal("new header must win")
	}
}
