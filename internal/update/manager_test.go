package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeInstaller struct {
	mu          sync.Mutex
	unsupp      string // non-empty: Supported() false with this reason
	prepareErr  error
	applyErr    error
	relaunchErr error
	backup      string
	calls       *[]string
	prepared    string
}

func (i *fakeInstaller) log(s string) {
	if i.calls != nil {
		i.mu.Lock()
		*i.calls = append(*i.calls, s)
		i.mu.Unlock()
	}
}
func (i *fakeInstaller) Supported() (bool, string) { return i.unsupp == "", i.unsupp }
func (i *fakeInstaller) Prepare(archive string, m *Manifest) (string, error) {
	i.log("prepare")
	i.prepared = archive
	return "/cand.app", i.prepareErr
}
func (i *fakeInstaller) Apply(c, v string) (string, error) {
	i.log("apply")
	return i.backup, i.applyErr
}
func (i *fakeInstaller) Relaunch() error { i.log("relaunch"); return i.relaunchErr }
func (i *fakeInstaller) Rollback(b string) error {
	i.log("rollback:" + b)
	return nil
}
func (i *fakeInstaller) AppPath() string { return "/Applications/Pocketlab.app" }

type env struct {
	t      *testing.T
	f      *feed
	dir    string
	calls  []string
	events []string
	inst   *fakeInstaller
	busy   string
	mu     sync.Mutex
	m      *Manager
}

func (e *env) rec(s string) {
	e.mu.Lock()
	e.calls = append(e.calls, s)
	e.mu.Unlock()
}

func (e *env) options() ManagerOptions {
	return ManagerOptions{
		Dir: e.dir, CurrentVersion: "0.4.0", Checker: e.f.config("0.4.0"), Installer: e.inst,
		ManualURL: func(m *Manifest) string { return "https://example.com/manual/" + m.Version },
		Hooks: Hooks{
			Busy:               func() (bool, string) { return e.busy != "", e.busy },
			BeforeInstall:      func(v string) error { e.rec("before:" + v); return nil },
			AfterFailedInstall: func() { e.rec("afterfail") },
			Quit:               func() { e.rec("quit") },
			Record: func(kind, reason, msg string) {
				e.mu.Lock()
				e.events = append(e.events, kind)
				e.mu.Unlock()
				e.rec("record:" + kind)
			},
		},
		FirstCheckDelay: 10 * time.Millisecond, Interval: 20 * time.Millisecond,
	}
}

func newEnv(t *testing.T, latest string) *env {
	t.Helper()
	e := &env{t: t, f: newFeed(t, latest), dir: t.TempDir()}
	e.inst = &fakeInstaller{backup: filepath.Join(e.dir, "backup", "Pocketlab-0.4.0.app"), calls: &e.calls}
	e.m = NewManager(e.options())
	return e
}

func (e *env) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func TestManagerCheckNowAndStatus(t *testing.T) {
	e := newEnv(t, "0.4.2")
	st := e.m.Status()
	if st.State != StateIdle || st.Available != nil || st.Current != "0.4.0" || !st.Supported {
		t.Fatalf("initial: %+v", st)
	}
	st, err := e.m.CheckNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateAvailable || st.Available == nil || st.Available.Version != "0.4.2" ||
		st.Available.NotesMD != "notes for 0.4.2" || st.Available.Size != int64(len(e.f.archive)) || st.LastCheck == nil {
		t.Fatalf("after check: %+v", st)
	}
	if st.Skipped || st.RemindUntil != nil || st.Progress != nil || st.Error != "" || st.ManualURL != "" {
		t.Fatalf("after check: %+v", st)
	}
	// up to date
	e.f.setManifest(e.f.manifest("0.4.0", "stable"))
	e.f.mu.Lock()
	e.f.etag = `"e9"`
	e.f.mu.Unlock()
	st, _ = e.m.CheckNow(context.Background())
	if st.State != StateUpToDate || st.Available != nil {
		t.Fatalf("up to date: %+v", st)
	}
	// error state keeps the message
	e.f.mu.Lock()
	e.f.status = 500
	e.f.mu.Unlock()
	st, err = e.m.CheckNow(context.Background())
	if err == nil || st.State != StateError || st.Error == "" {
		t.Fatalf("error state: %+v %v", st, err)
	}
}

func TestManagerSkipRemindPersist(t *testing.T) {
	e := newEnv(t, "0.4.2")
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	opts := e.options()
	opts.Now = func() time.Time { return now }
	e.m = NewManager(opts)
	if _, err := e.m.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.m.Skip("0.4.2")
	e.m.Remind(6 * time.Hour)

	m2 := NewManager(opts) // reload from disk, no network
	reqs := e.f.reqs.Load()
	st := m2.Status()
	if st.State != StateAvailable || st.Available == nil || !st.Skipped {
		t.Fatalf("skip not persisted: %+v", st)
	}
	if st.RemindUntil == nil || !st.RemindUntil.Equal(now.Add(6*time.Hour)) {
		t.Fatalf("remind not persisted: %+v", st.RemindUntil)
	}
	if e.f.reqs.Load() != reqs {
		t.Fatal("loading state must not hit the network")
	}
	// remind expires
	now = now.Add(7 * time.Hour)
	if m2.Status().RemindUntil != nil {
		t.Fatal("expired reminder should be hidden")
	}
	// a newer version is not skipped
	e.f.setManifest(e.f.manifest("0.4.3", "stable"))
	e.f.mu.Lock()
	e.f.etag = `"e3"`
	e.f.mu.Unlock()
	st, _ = m2.CheckNow(context.Background())
	if st.Skipped || st.Available.Version != "0.4.3" {
		t.Fatalf("newer version must not be skipped: %+v", st)
	}
}

func TestManagerNoNetworkWhenAutoCheckOff(t *testing.T) {
	e := newEnv(t, "0.4.2")
	if err := e.m.SetSettings(Settings{AutoCheck: false, Channel: "stable"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.m.Start(ctx)
	time.Sleep(300 * time.Millisecond)
	if n := e.f.reqs.Load(); n != 0 {
		t.Fatalf("auto-check off must make zero requests, got %d", n)
	}
	// a manual check still works
	if _, err := e.m.CheckNow(ctx); err != nil || e.f.reqs.Load() == 0 {
		t.Fatalf("manual check: %v reqs=%d", err, e.f.reqs.Load())
	}
	// switching auto-check on (live, no restart) triggers checks
	n := e.f.reqs.Load()
	if err := e.m.SetSettings(Settings{AutoCheck: true, Channel: "stable"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for e.f.reqs.Load() == n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if e.f.reqs.Load() == n {
		t.Fatal("enabling auto-check should start checking")
	}
	// and switching it off again stops them
	_ = e.m.SetSettings(Settings{AutoCheck: false, Channel: "stable"})
	time.Sleep(100 * time.Millisecond)
	n = e.f.reqs.Load()
	time.Sleep(200 * time.Millisecond)
	if e.f.reqs.Load() != n {
		t.Fatal("requests continued after auto-check was turned off")
	}
	cancel()
	e.m.Wait()
}

func TestManagerAutoDownloadNeverInstalls(t *testing.T) {
	e := newEnv(t, "0.4.2")
	_ = e.m.SetSettings(Settings{AutoCheck: true, Channel: "stable", AutoDownload: true})
	if _, err := e.m.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.m.Wait()
	st := e.m.Status()
	if st.State != StateReady {
		t.Fatalf("auto-download should leave the update ready: %+v", st)
	}
	for _, c := range e.snapshot() {
		if c == "apply" || c == "quit" {
			t.Fatalf("auto-download must never install: %v", e.snapshot())
		}
	}
	if _, err := os.Stat(filepath.Join(e.dir, "updates", "0.4.2", "Pocketlab-0.4.2-macos-arm64.zip")); err != nil {
		t.Fatal(err)
	}
}

func TestManagerDisabled(t *testing.T) {
	e := newEnv(t, "0.4.2")
	opts := e.options()
	opts.Disabled, opts.DisabledReason = true, "PL_UPDATE_DISABLE is set"
	m := NewManager(opts)
	st := m.Status()
	if !st.Disabled || st.Supported || st.State != StateIdle {
		t.Fatalf("%+v", st)
	}
	if _, err := m.CheckNow(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if err := m.Install(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()
	m.Wait()
	if e.f.reqs.Load() != 0 {
		t.Fatal("disabled manager must not touch the network")
	}
}

func TestManagerInstallFlow(t *testing.T) {
	e := newEnv(t, "0.4.2")
	if _, err := e.m.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"prepare", "record:update_started", "before:0.4.2", "apply", "record:update_started", "relaunch", "quit"}
	got := e.snapshot()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order:\n got %v\nwant %v", got, want)
	}
	// the archive handed to Prepare was fully verified by Download
	if filepath.Base(e.inst.prepared) != "Pocketlab-0.4.2-macos-arm64.zip" {
		t.Fatalf("prepared %q", e.inst.prepared)
	}
	st := LoadState(e.dir)
	p := st.Pending
	if p == nil || p.From != "0.4.0" || p.To != "0.4.2" || p.Launches != 0 || p.Healthy ||
		p.BackupPath != e.inst.backup || p.AppPath != "/Applications/Pocketlab.app" || p.StartedAt.IsZero() {
		t.Fatalf("pending: %+v", p)
	}
	if st.Backup == nil || st.Backup.From != "0.4.0" || st.Backup.To != "0.4.2" {
		t.Fatalf("backup: %+v", st.Backup)
	}
}

func TestManagerInstallBusyRefuses(t *testing.T) {
	e := newEnv(t, "0.4.2")
	e.busy = "a test run is in progress"
	_, _ = e.m.CheckNow(context.Background())
	err := e.m.Install(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "busy: a test run is in progress") {
		t.Fatalf("want busy error, got %v", err)
	}
	for _, c := range e.snapshot() {
		if c == "apply" || strings.HasPrefix(c, "before:") || c == "quit" {
			t.Fatalf("a busy install must touch nothing: %v", e.snapshot())
		}
	}
	// still installable once idle
	e.busy = ""
	if err := e.m.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerVerifyFailureStopsNothing(t *testing.T) {
	e := newEnv(t, "0.4.2")
	e.inst.prepareErr = errors.New("codesign failed")
	_, _ = e.m.CheckNow(context.Background())
	err := e.m.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatal(err)
	}
	for _, c := range e.snapshot() {
		if strings.HasPrefix(c, "before:") || c == "apply" || c == "record:update_started" {
			t.Fatalf("verification must happen before anything is stopped: %v", e.snapshot())
		}
	}
	if st := e.m.Status(); st.State != StateError || st.Error == "" {
		t.Fatalf("%+v", st)
	}
	if _, err := os.Stat(e.inst.prepared); !os.IsNotExist(err) {
		t.Fatal("an archive that failed verification must be deleted")
	}
}

func TestManagerApplyFailureRestartsServer(t *testing.T) {
	e := newEnv(t, "0.4.2")
	e.inst.applyErr = errors.New("swap failed")
	_, _ = e.m.CheckNow(context.Background())
	if err := e.m.Install(context.Background()); err == nil {
		t.Fatal("want error")
	}
	got := strings.Join(e.snapshot(), ",")
	if !strings.Contains(got, "before:0.4.2,apply,afterfail,record:update_failed") || strings.Contains(got, "quit") || strings.Contains(got, "relaunch") {
		t.Fatalf("calls: %s", got)
	}
	if LoadState(e.dir).Pending != nil {
		t.Fatal("no pending install after a failed apply")
	}
}

func TestManagerUnsupportedInstall(t *testing.T) {
	e := newEnv(t, "0.4.2")
	e.inst.unsupp = "the folder is not writable"
	_, _ = e.m.CheckNow(context.Background())
	st := e.m.Status()
	if st.Supported || st.ManualURL != "https://example.com/manual/0.4.2" || !strings.Contains(st.Error, "not writable") {
		t.Fatalf("%+v", st)
	}
	if err := e.m.Install(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestManagerSingleOperation(t *testing.T) {
	e := newEnv(t, "0.4.2")
	if err := e.m.begin(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CheckNow(context.Background()); !errors.Is(err, ErrBusyOp) {
		t.Fatalf("second operation must be refused: %v", err)
	}
	e.m.end()
	if _, err := e.m.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerSettings(t *testing.T) {
	e := newEnv(t, "0.4.2")
	if err := e.m.SetSettings(Settings{AutoCheck: true, Channel: "nightly"}); err == nil {
		t.Fatal("invalid channel accepted")
	}
	_, _ = e.m.CheckNow(context.Background())
	if err := e.m.SetSettings(Settings{AutoCheck: false, Channel: "beta", AutoDownload: true}); err != nil {
		t.Fatal(err)
	}
	st := e.m.Status()
	if st.Settings.Channel != "beta" || st.Settings.AutoCheck || !st.Settings.AutoDownload {
		t.Fatalf("%+v", st.Settings)
	}
	if st.Available != nil {
		t.Fatal("switching channel drops the stale offer")
	}
	if got := LoadState(e.dir).Settings; got.Channel != "beta" || got.AutoCheck {
		t.Fatalf("not persisted: %+v", got)
	}
}

func TestManagerBetaChannelLive(t *testing.T) {
	e := newEnv(t, "0.4.1")
	e.f.addRelease("v0.4.1", false, false, e.f.manifest("0.4.1", "stable"))
	e.f.addRelease("v0.5.0-beta.1", false, true, e.f.manifest("0.5.0-beta.1", "beta"))
	opts := e.options()
	opts.Checker.ReleasesAPI = e.f.srv.URL + "/releases"
	opts.Checker.ManifestURL = e.f.url()
	e.m = NewManager(opts)
	st, _ := e.m.CheckNow(context.Background())
	if st.Available == nil || st.Available.Version != "0.4.1" {
		t.Fatalf("stable: %+v", st)
	}
	_ = e.m.SetSettings(Settings{AutoCheck: true, Channel: "beta"})
	st, err := e.m.CheckNow(context.Background())
	if err != nil || st.Available == nil || st.Available.Version != "0.5.0-beta.1" {
		t.Fatalf("beta: %+v %v", st, err)
	}
}

func writeState(t *testing.T, dir string, p *Pending) {
	t.Helper()
	st := LoadState(dir)
	st.Pending = p
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
}

func (e *env) reopen(version string) *Manager {
	opts := e.options()
	opts.CurrentVersion = version
	opts.Checker.CurrentVersion = version
	e.m = NewManager(opts)
	return e.m
}

func TestManagerStartupPending(t *testing.T) {
	e := newEnv(t, "0.4.2")
	writeState(t, e.dir, &Pending{From: "0.4.0", To: "0.4.2", BackupPath: "/b"})

	// first launch of the new version: update_ok, no rollback offer
	m := e.reopen("0.4.2")
	m.Startup()
	if got := LoadState(e.dir).Pending; got == nil || got.Launches != 1 {
		t.Fatalf("pending: %+v", got)
	}
	if st := m.Status(); st.Rollback != nil {
		t.Fatalf("no rollback offer after one launch: %+v", st.Rollback)
	}
	if c := countOf(e.snapshot(), "record:update_ok"); c != 1 {
		t.Fatalf("update_ok recorded %d times", c)
	}

	// second launch without a healthy marker: offer rollback, no second update_ok
	m = e.reopen("0.4.2")
	m.Startup()
	st := m.Status()
	if st.Rollback == nil || st.Rollback.From != "0.4.0" || st.Rollback.To != "0.4.2" || st.Rollback.Reason == "" {
		t.Fatalf("rollback offer: %+v", st.Rollback)
	}
	if c := countOf(e.snapshot(), "record:update_ok"); c != 1 {
		t.Fatalf("update_ok recorded %d times", c)
	}

	// healthy: pending cleared, offer gone, backup bookkeeping kept
	m.MarkHealthy()
	if LoadState(e.dir).Pending != nil || m.Status().Rollback != nil {
		t.Fatal("MarkHealthy must clear the pending install")
	}
	m = e.reopen("0.4.2")
	m.Startup()
	if m.Status().Rollback != nil {
		t.Fatal("no offer once healthy")
	}
}

func TestManagerStartupHealthyOnFirstLaunch(t *testing.T) {
	e := newEnv(t, "0.4.2")
	writeState(t, e.dir, &Pending{From: "0.4.0", To: "0.4.2"})
	m := e.reopen("0.4.2")
	m.Startup()
	m.MarkHealthy()
	m = e.reopen("0.4.2")
	m.Startup()
	if m.Status().Rollback != nil || countOf(e.snapshot(), "record:update_ok") != 1 {
		t.Fatal("healthy first launch must not offer a rollback")
	}
}

func TestManagerStartupVersionUnchanged(t *testing.T) {
	e := newEnv(t, "0.4.2")
	writeState(t, e.dir, &Pending{From: "0.4.0", To: "0.4.2"})
	m := e.reopen("0.4.0") // still the old version after the install
	m.Startup()
	st := m.Status()
	if st.State != StateError || !strings.Contains(st.Error, "did not take effect") {
		t.Fatalf("%+v", st)
	}
	if countOf(e.snapshot(), "record:update_failed") != 1 || LoadState(e.dir).Pending != nil {
		t.Fatalf("calls %v", e.snapshot())
	}
	// a manual update to something unrelated just clears the marker
	writeState(t, e.dir, &Pending{From: "0.3.0", To: "0.3.5"})
	m = e.reopen("0.9.0")
	m.Startup()
	if LoadState(e.dir).Pending != nil {
		t.Fatal("unrelated pending must be cleared")
	}
}

func TestManagerRollback(t *testing.T) {
	e := newEnv(t, "0.4.2")
	backup := filepath.Join(e.dir, "backup", "Pocketlab-0.4.0.app")
	_ = os.MkdirAll(backup, 0o755)
	writeState(t, e.dir, &Pending{From: "0.4.0", To: "0.4.2", BackupPath: backup, Launches: 1})
	m := e.reopen("0.4.2")
	m.Startup()
	if err := m.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(e.snapshot(), ",")
	if !strings.Contains(got, "before:0.4.0,rollback:"+backup+",record:rollback") || !strings.HasSuffix(got, "relaunch,quit") {
		t.Fatalf("calls: %s", got)
	}
	st := LoadState(e.dir)
	if st.Pending != nil || st.Backup != nil || st.SkipVersion != "0.4.2" {
		t.Fatalf("state after rollback: %+v", st)
	}
	// nothing to roll back to
	if err := m.Rollback(context.Background()); err == nil {
		t.Fatal("rollback without a backup must fail")
	}
	// a missing backup file is reported
	st.Backup = &Backup{Path: filepath.Join(e.dir, "gone"), From: "0.4.0", To: "0.4.2"}
	_ = st.Save(e.dir)
	if err := e.reopen("0.4.2").Rollback(context.Background()); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("%v", err)
	}
}

func TestManagerManualRollbackAfterHealthy(t *testing.T) {
	e := newEnv(t, "0.4.2")
	backup := filepath.Join(e.dir, "Pocketlab-0.4.0.app")
	_ = os.MkdirAll(backup, 0o755)
	writeState(t, e.dir, &Pending{From: "0.4.0", To: "0.4.2", BackupPath: backup})
	st := LoadState(e.dir)
	st.Backup = &Backup{Path: backup, From: "0.4.0", To: "0.4.2"}
	_ = st.Save(e.dir)
	m := e.reopen("0.4.2")
	m.Startup()
	m.MarkHealthy()
	if err := m.Rollback(context.Background()); err != nil {
		t.Fatalf("manual rollback after healthy: %v", err)
	}
}

func countOf(l []string, s string) int {
	n := 0
	for _, x := range l {
		if x == s {
			n++
		}
	}
	return n
}
