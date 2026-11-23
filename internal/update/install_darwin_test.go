//go:build darwin

package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureID = "com.pocketlab.desktop"

func needTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"codesign", "ditto", "plutil"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

func sh(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// makeApp builds a minimal ad-hoc signed Pocketlab.app inside dir.
func makeApp(t *testing.T, dir, bundleID, version string) string {
	t.Helper()
	app := filepath.Join(dir, "Pocketlab.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		t.Skip("/usr/bin/true unreadable")
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "pocketlab-desktop"), bin, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>pocketlab-desktop</string>
<key>CFBundleIdentifier</key><string>%s</string>
<key>CFBundleName</key><string>Pocketlab</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>%s</string>
<key>CFBundleVersion</key><string>%s</string>
</dict></plist>
`, bundleID, version, version)
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	sh(t, "codesign", "--force", "-s", "-", app)
	return app
}

func zipApp(t *testing.T, app string) string {
	t.Helper()
	z := filepath.Join(t.TempDir(), "Pocketlab.zip")
	sh(t, "ditto", "-c", "-k", "--keepParent", app, z)
	return z
}

func version(t *testing.T, app string) string {
	t.Helper()
	v, err := plistString(app, "CFBundleShortVersionString")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func inspect(t *testing.T, app string) BundleInfo {
	t.Helper()
	bi, err := InspectBundle(app)
	if err != nil {
		t.Fatal(err)
	}
	return bi
}

func TestExtract(t *testing.T) {
	needTools(t)
	z := zipApp(t, makeApp(t, t.TempDir(), fixtureID, "0.4.1"))
	dest := filepath.Join(t.TempDir(), "x")
	app, err := Extract(z, dest)
	if err != nil || filepath.Base(app) != "Pocketlab.app" || version(t, app) != "0.4.1" {
		t.Fatalf("%q %v", app, err)
	}
	if _, err := Extract(z, dest); err == nil {
		t.Fatal("a non-empty destination must be refused")
	}

	// two apps at the top level
	src := t.TempDir()
	a := makeApp(t, filepath.Join(src, "a"), fixtureID, "0.4.1")
	b := makeApp(t, filepath.Join(src, "b"), fixtureID, "0.4.1")
	_ = os.Rename(b, filepath.Join(src, "Other.app"))
	_ = os.Rename(a, filepath.Join(src, "Pocketlab.app"))
	_ = os.RemoveAll(filepath.Join(src, "a"))
	_ = os.RemoveAll(filepath.Join(src, "b"))
	z2 := filepath.Join(t.TempDir(), "two.zip")
	sh(t, "ditto", "-c", "-k", src, z2)
	if _, err := Extract(z2, filepath.Join(t.TempDir(), "y")); err == nil {
		t.Fatal("exactly one top-level app is required")
	}

	// a symlink escaping the extraction directory
	evil := makeApp(t, t.TempDir(), fixtureID, "0.4.1")
	if err := os.Symlink("../../../../../../etc", filepath.Join(evil, "Contents", "escape")); err != nil {
		t.Fatal(err)
	}
	z3 := zipApp(t, evil)
	if _, err := Extract(z3, filepath.Join(t.TempDir(), "z")); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("escaping symlink must be rejected: %v", err)
	}
	abs := makeApp(t, t.TempDir(), fixtureID, "0.4.1")
	_ = os.Symlink("/etc/passwd", filepath.Join(abs, "Contents", "abs"))
	if _, err := Extract(zipApp(t, abs), filepath.Join(t.TempDir(), "w")); err == nil {
		t.Fatal("absolute symlink must be rejected")
	}
}

func TestInspectBundle(t *testing.T) {
	needTools(t)
	bi := inspect(t, makeApp(t, t.TempDir(), fixtureID, "0.4.1"))
	if bi.BundleID != fixtureID || bi.Version != "0.4.1" || bi.TeamID != "" || bi.Notarized {
		t.Fatalf("%+v", bi)
	}
	if _, err := InspectBundle(t.TempDir()); err == nil {
		t.Fatal("not a bundle")
	}
}

func TestVerifyCandidate(t *testing.T) {
	needTools(t)
	cur := inspect(t, makeApp(t, t.TempDir(), fixtureID, "0.4.0"))
	candApp := makeApp(t, t.TempDir(), fixtureID, "0.4.1")
	cand := inspect(t, candApp)

	if err := VerifyCandidate(cur, cand, "0.4.1", true); err != nil {
		t.Fatalf("valid candidate: %v", err)
	}
	if err := VerifyCandidate(cur, cand, "v0.4.1", true); err != nil {
		t.Fatalf("leading v: %v", err)
	}
	// ad-hoc has no Team ID: refused unless allowAdhoc
	if err := VerifyCandidate(cur, cand, "0.4.1", false); !errors.Is(err, ErrBundleVerify) {
		t.Fatalf("ad-hoc candidate must be rejected in production: %v", err)
	}
	// version differs from the manifest
	if err := VerifyCandidate(cur, cand, "0.4.2", true); !errors.Is(err, ErrBundleVerify) {
		t.Fatalf("version mismatch: %v", err)
	}
	// not newer / downgrade
	same := inspect(t, makeApp(t, t.TempDir(), fixtureID, "0.4.0"))
	if err := VerifyCandidate(cur, same, "0.4.0", true); !errors.Is(err, ErrBundleVerify) {
		t.Fatalf("same version: %v", err)
	}
	old := inspect(t, makeApp(t, t.TempDir(), fixtureID, "0.3.0"))
	if err := VerifyCandidate(cur, old, "0.3.0", true); !errors.Is(err, ErrBundleVerify) {
		t.Fatalf("downgrade: %v", err)
	}
	// other bundle id
	other := inspect(t, makeApp(t, t.TempDir(), "com.evil.app", "0.4.1"))
	if err := VerifyCandidate(cur, other, "0.4.1", true); !errors.Is(err, ErrBundleVerify) {
		t.Fatalf("bundle id: %v", err)
	}
	// differing Team IDs are rejected even with allowAdhoc
	a, b := cand, cur
	a.TeamID, b.TeamID = "AAAAAAAAAA", "BBBBBBBBBB"
	if err := VerifyCandidate(b, a, "0.4.1", true); !errors.Is(err, ErrBundleVerify) {
		t.Fatalf("team id: %v", err)
	}
	// a notarized running app requires a notarized candidate (ad-hoc fixture is not)
	n := cur
	n.Notarized = true
	if err := VerifyCandidate(n, cand, "0.4.1", true); !errors.Is(err, ErrBundleVerify) {
		t.Fatalf("notarization: %v", err)
	}
	// tampered after signing
	bin := filepath.Join(candApp, "Contents", "MacOS", "pocketlab-desktop")
	f, _ := os.OpenFile(bin, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.Write([]byte("tamper"))
	_ = f.Close()
	if err := VerifyCandidate(cur, cand, "0.4.1", true); !errors.Is(err, ErrBundleVerify) {
		t.Fatalf("tampered bundle must fail codesign --verify: %v", err)
	}
}

func TestPreflight(t *testing.T) {
	if err := Preflight("/private/var/folders/ab/xyz/AppTranslocation/ABCD-EF/d/Pocketlab.app"); !errors.Is(err, ErrTranslocated) {
		t.Fatalf("translocated: %v", err)
	}
	if err := Preflight(filepath.Join(t.TempDir(), "Pocketlab.app")); err != nil {
		t.Fatalf("writable parent: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	if err := Preflight(filepath.Join(parent, "Pocketlab.app")); !errors.Is(err, ErrNotWritable) {
		t.Fatalf("read-only parent: %v", err)
	}
	ok, reason := NewPlatformInstaller(InstallerOptions{CurrentApp: filepath.Join(parent, "Pocketlab.app")}).Supported()
	if ok || reason == "" {
		t.Fatalf("Supported: %v %q", ok, reason)
	}
}

func TestRunningAppOutsideBundle(t *testing.T) {
	if _, err := RunningApp(); err == nil {
		t.Skip("test binary is unexpectedly inside an .app")
	}
}

type layout struct {
	root, apps, staging, backup, current string
	inst                                 Installer
	zip                                  string
}

func newLayout(t *testing.T, oldVer, newVer string) *layout {
	t.Helper()
	root := t.TempDir()
	l := &layout{root: root, apps: filepath.Join(root, "Applications"), staging: filepath.Join(root, "updates"), backup: filepath.Join(root, "updates", "backup")}
	_ = os.MkdirAll(l.apps, 0o755)
	l.current = makeApp(t, l.apps, fixtureID, oldVer)
	l.zip = zipApp(t, makeApp(t, t.TempDir(), fixtureID, newVer))
	l.inst = NewPlatformInstaller(InstallerOptions{CurrentApp: l.current, StagingDir: l.staging, BackupDir: l.backup, AllowAdhoc: true})
	return l
}

func TestInstallerSwapAndRollback(t *testing.T) {
	needTools(t)
	l := newLayout(t, "0.4.0", "0.4.1")
	if ok, why := l.inst.Supported(); !ok {
		t.Fatalf("supported: %q", why)
	}
	cand, err := l.inst.Prepare(l.zip, &Manifest{Version: "0.4.1"})
	if err != nil {
		t.Fatal(err)
	}
	backup, err := l.inst.Apply(cand, "0.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if backup != filepath.Join(l.backup, "Pocketlab-0.4.0.app") {
		t.Fatalf("backup path %q", backup)
	}
	if version(t, l.current) != "0.4.1" || version(t, backup) != "0.4.0" {
		t.Fatalf("versions: current %s backup %s", version(t, l.current), version(t, backup))
	}
	sh(t, "codesign", "--verify", "--deep", "--strict", l.current)
	sh(t, "codesign", "--verify", "--deep", "--strict", backup)
	if _, err := os.Stat(cand); !os.IsNotExist(err) {
		t.Fatal("the candidate path should now hold nothing (it was swapped and moved)")
	}
	// no stray hidden siblings next to the app
	ents, _ := os.ReadDir(l.apps)
	if len(ents) != 1 {
		t.Fatalf("applications dir: %v", ents)
	}

	// a failed swap target leaves the app alone: new Pocketlab update while a
	// non-newer candidate is rejected earlier by Prepare
	if _, err := l.inst.Prepare(l.zip, &Manifest{Version: "0.4.1"}); err == nil {
		t.Fatal("candidate equal to the installed version must be rejected")
	}

	// rollback
	if err := l.inst.Rollback(backup); err != nil {
		t.Fatal(err)
	}
	if version(t, l.current) != "0.4.0" {
		t.Fatalf("after rollback: %s", version(t, l.current))
	}
	sh(t, "codesign", "--verify", "--deep", "--strict", l.current)
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatal("backup should be consumed by the rollback")
	}
	rolled := filepath.Join(l.backup, "Pocketlab-0.4.1-rolledback.app")
	if version(t, rolled) != "0.4.1" {
		t.Fatalf("rolledback copy: %v", rolled)
	}
	ents, _ = os.ReadDir(l.apps)
	if len(ents) != 1 {
		t.Fatalf("applications dir: %v", ents)
	}
}

func TestInstallerKeepsOnlyNewestBackup(t *testing.T) {
	needTools(t)
	l := newLayout(t, "0.4.0", "0.4.1")
	apply := func(zip, ver string) string {
		cand, err := l.inst.Prepare(zip, &Manifest{Version: ver})
		if err != nil {
			t.Fatal(err)
		}
		b, err := l.inst.Apply(cand, ver)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	apply(l.zip, "0.4.1")
	b2 := apply(zipApp(t, makeApp(t, t.TempDir(), fixtureID, "0.4.2")), "0.4.2")
	if version(t, l.current) != "0.4.2" || version(t, b2) != "0.4.1" {
		t.Fatal("second update")
	}
	ents, _ := os.ReadDir(l.backup)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "Pocketlab-0.4.1.app" {
		t.Fatalf("only the newest backup should remain: %v", names)
	}
}

func TestInstallerRejectsBadCandidates(t *testing.T) {
	needTools(t)
	l := newLayout(t, "0.4.0", "0.4.1")
	if _, err := l.inst.Prepare(l.zip, &Manifest{Version: "0.4.9"}); !errors.Is(err, ErrBundleVerify) {
		t.Fatalf("manifest/bundle version mismatch: %v", err)
	}
	// production mode refuses an ad-hoc candidate
	strict := NewPlatformInstaller(InstallerOptions{CurrentApp: l.current, StagingDir: l.staging, BackupDir: l.backup})
	if _, err := strict.Prepare(l.zip, &Manifest{Version: "0.4.1"}); !errors.Is(err, ErrBundleVerify) {
		t.Fatalf("ad-hoc must not self-update: %v", err)
	}
	if version(t, l.current) != "0.4.0" {
		t.Fatal("the installed app must be untouched")
	}
}

func TestInstallerApplyNotWritable(t *testing.T) {
	needTools(t)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	l := newLayout(t, "0.4.0", "0.4.1")
	cand, err := l.inst.Prepare(l.zip, &Manifest{Version: "0.4.1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(l.apps, 0o555)
	t.Cleanup(func() { _ = os.Chmod(l.apps, 0o755) })
	if _, err := l.inst.Apply(cand, "0.4.1"); !errors.Is(err, ErrNotWritable) {
		t.Fatalf("%v", err)
	}
	if version(t, l.current) != "0.4.0" {
		t.Fatal("untouched")
	}
}

func TestInstallerTranslocated(t *testing.T) {
	needTools(t)
	root := t.TempDir()
	app := makeApp(t, filepath.Join(root, "AppTranslocation", "UUID", "d"), fixtureID, "0.4.0")
	inst := NewPlatformInstaller(InstallerOptions{CurrentApp: app, StagingDir: filepath.Join(root, "s"), BackupDir: filepath.Join(root, "b"), AllowAdhoc: true})
	if ok, why := inst.Supported(); ok || !strings.Contains(why, "Applications") {
		t.Fatalf("%v %q", ok, why)
	}
	if _, err := inst.Apply("/nonexistent.app", "0.4.1"); !errors.Is(err, ErrTranslocated) {
		t.Fatalf("%v", err)
	}
}
