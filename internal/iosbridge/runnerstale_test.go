package iosbridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const infoPlistFixture = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>%s</string>
<key>CFBundleName</key><string>WebDriverAgentRunner-Runner</string>
</dict></plist>`

func writeRunnerApp(t *testing.T, app, id string) {
	t.Helper()
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(fmt.Sprintf(infoPlistFixture, id))
	if err := os.WriteFile(filepath.Join(app, "Info.plist"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerAppReason(t *testing.T) {
	dir := t.TempDir()
	want := ExpectedRunnerBundleID("")
	if want != "com.pocketlab.WebDriverAgentRunner.xctrunner" || ExpectedRunnerBundleID("com.acme") != "com.acme.WebDriverAgentRunner.xctrunner" {
		t.Fatalf("expected id %q", want)
	}
	good := filepath.Join(dir, "good.app")
	writeRunnerApp(t, good, want)
	if r := RunnerAppReason(good, want); r != "" {
		t.Fatalf("good: %q", r)
	}
	old := filepath.Join(dir, "old.app")
	writeRunnerApp(t, old, "com.phoneforge.WebDriverAgentRunner.xctrunner")
	if r := RunnerAppReason(old, want); r != ReasonStaleBundleID {
		t.Fatalf("old: %q", r)
	}
	if r := RunnerAppReason(filepath.Join(dir, "none.app"), want); r != ReasonBadPlist {
		t.Fatalf("missing plist: %q", r)
	}
}

func TestEnvironmentStaleRunner(t *testing.T) {
	b := newTestBuilder(t, &fakeRunner{})
	writeRunnerApp(t, b.RunnerApp(), "com.phoneforge.WebDriverAgentRunner.xctrunner")
	e := b.EnvironmentQuick()
	if e.RunnerBuilt || e.RunnerReason != ReasonStaleBundleID || e.RunnerApp != "" {
		t.Fatalf("stale: %+v", e)
	}
	writeRunnerApp(t, b.RunnerApp(), ExpectedRunnerBundleID(""))
	if e := b.EnvironmentQuick(); !e.RunnerBuilt || e.RunnerReason != "" {
		t.Fatalf("fresh: %+v", e)
	}
	// custom prefix makes the default-id app stale
	b.Env = func(k string) string {
		if k == "PL_BUNDLE_PREFIX" {
			return "com.acme"
		}
		return ""
	}
	if e := b.EnvironmentQuick(); e.RunnerBuilt || e.RunnerReason != ReasonStaleBundleID {
		t.Fatalf("prefix: %+v", e)
	}
}

func TestEnvironmentStaleSim(t *testing.T) {
	b := newTestBuilder(t, &fakeRunner{})
	prod := filepath.Join(b.Home, "build-sim", "Build", "Products")
	if err := os.MkdirAll(prod, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prod, "WebDriverAgentRunner_iphonesimulator27.0-arm64.xctestrun"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(prod, "Debug-iphonesimulator", "WebDriverAgentRunner-Runner.app")
	writeRunnerApp(t, app, "com.phoneforge.WebDriverAgentRunner.xctrunner")
	if e := b.EnvironmentQuick(); e.SimBuilt || e.SimReason != ReasonStaleBundleID {
		t.Fatalf("stale sim: %+v", e)
	}
	writeRunnerApp(t, app, ExpectedRunnerBundleID(""))
	if e := b.EnvironmentQuick(); !e.SimBuilt || e.SimReason != "" {
		t.Fatalf("fresh sim: %+v", e)
	}
}

func TestInstallRunnerRefusesStale(t *testing.T) {
	t.Setenv("PL_BUNDLE_PREFIX", "")
	app := filepath.Join(t.TempDir(), "WebDriverAgentRunner-Runner.app")
	writeRunnerApp(t, app, "com.phoneforge.WebDriverAgentRunner.xctrunner")
	// The refusal happens before any device access, so no phone is touched.
	err := InstallRunner("no-such-udid", app)
	var is *Issue
	if !errors.As(err, &is) || is.Code != CodeRunnerStale || is.Hint != "Rebuild WDA" {
		t.Fatalf("want RUNNER_STALE, got %v", err)
	}
	if err := InstallRunner("no-such-udid", filepath.Join(t.TempDir(), "missing.app")); !errors.Is(err, ErrRunnerNotBuilt) {
		t.Fatalf("missing: %v", err)
	}
}

func TestManagerClearError(t *testing.T) {
	m := NewManagerWithDeps(t.TempDir(), Deps{Installer: errInstaller{}})
	_, _ = m.Start(context.Background(), "U1", Options{})
	if st := m.Status("U1"); st.State != StateError || st.Error == "" {
		t.Fatalf("setup: %+v", st)
	}
	m.ClearError("U1")
	if st := m.Status("U1"); st.State != StateStopped || st.Error != "" {
		t.Fatalf("after clear: %+v", st)
	}
}

type errInstaller struct{}

func (errInstaller) Installed(context.Context, string, string) (bool, error) { return false, nil }
