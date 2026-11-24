//go:build darwin

package update

import (
	"archive/zip"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if len(s) > 400 {
			s = s[:400]
		}
		return s, fmt.Errorf("%s %s: %w: %s", name, args[0], err, s)
	}
	return s, nil
}

// RunningApp returns the .app bundle that contains the running executable.
func RunningApp() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	for p := filepath.Dir(exe); p != "/" && p != "."; p = filepath.Dir(p) {
		if strings.HasSuffix(p, ".app") {
			return p, nil
		}
	}
	return "", errors.New("the running executable is not inside an .app bundle")
}

func plistString(app, key string) (string, error) {
	return run("plutil", "-extract", key, "raw", "-o", "-", filepath.Join(app, "Contents", "Info.plist"))
}

// InspectBundle reads identity, version, Team ID and notarization status.
func InspectBundle(path string) (BundleInfo, error) {
	bi := BundleInfo{Path: path}
	var err error
	if bi.BundleID, err = plistString(path, "CFBundleIdentifier"); err != nil {
		return bi, err
	}
	if bi.Version, err = plistString(path, "CFBundleShortVersionString"); err != nil {
		return bi, err
	}
	bi.TeamID = bundleTeamID(path)
	bi.Notarized = exec.Command("spctl", "--assess", "--type", "execute", path).Run() == nil
	return bi, nil
}

// bundleTeamID returns the signing Team ID, "" when ad-hoc signed or unsigned.
func bundleTeamID(path string) string {
	// codesign -dv writes to stderr; a failure here just means unsigned.
	out, _ := exec.Command("codesign", "-dv", "--verbose=2", path).CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "TeamIdentifier="); ok {
			v = strings.TrimSpace(v)
			if v != "not set" {
				return v
			}
		}
	}
	return ""
}

// teamMismatch reports whether other's signing identity differs from cur's.
// Unless allowAdhoc (tests), both must carry the same non-empty Team ID.
func teamMismatch(cur, other BundleInfo, allowAdhoc bool) bool {
	if !allowAdhoc || (other.TeamID != "" && cur.TeamID != "") {
		return other.TeamID == "" || other.TeamID != cur.TeamID
	}
	return false
}

func codesignVerify(app string) error {
	if _, err := run("codesign", "--verify", "--deep", "--strict", app); err != nil {
		return fmt.Errorf("%w: %v", ErrBundleVerify, err)
	}
	return nil
}

// VerifyCandidate runs the install-time checks on an extracted bundle.
func VerifyCandidate(cur, cand BundleInfo, expectVersion string, allowAdhoc bool) error {
	fail := func(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrBundleVerify, fmt.Sprintf(f, a...)) }
	if err := codesignVerify(cand.Path); err != nil {
		return err
	}
	if cand.BundleID != cur.BundleID {
		return fail("bundle id %q differs from %q", cand.BundleID, cur.BundleID)
	}
	if strings.TrimPrefix(cand.Version, "v") != strings.TrimPrefix(expectVersion, "v") {
		return fail("bundle version %q does not match the manifest (%q)", cand.Version, expectVersion)
	}
	if CompareVersions(cand.Version, cur.Version) <= 0 {
		return fail("bundle version %q is not newer than %q", cand.Version, cur.Version)
	}
	if teamMismatch(cur, cand, allowAdhoc) {
		return fail("Team ID %q does not match the running app (%q)", cand.TeamID, cur.TeamID)
	}
	if cur.Notarized {
		if !cand.Notarized && exec.Command("spctl", "--assess", "--type", "execute", cand.Path).Run() != nil {
			return fail("Gatekeeper rejects the new app (not notarized)")
		}
		_ = exec.Command("xcrun", "stapler", "validate", cand.Path).Run() // best effort
	}
	return nil
}

// VerifyBackup runs the checks a kept previous app must pass before it may
// replace the running one: intact signature, same bundle id, same signing team.
func VerifyBackup(cur, bak BundleInfo, allowAdhoc bool) error {
	fail := func(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrBundleVerify, fmt.Sprintf(f, a...)) }
	if err := codesignVerify(bak.Path); err != nil {
		return err
	}
	if bak.BundleID != cur.BundleID {
		return fail("backup bundle id %q differs from %q", bak.BundleID, cur.BundleID)
	}
	if teamMismatch(cur, bak, allowAdhoc) {
		return fail("backup Team ID %q does not match the running app (%q)", bak.TeamID, cur.TeamID)
	}
	return nil
}

// checkZipEntries refuses archives holding symlinks, absolute paths or ".."
// components before anything is extracted.
func checkZipEntries(zipPath string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := f.Name
		if strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
			return fmt.Errorf("extract: archive entry %q has an absolute path", name)
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".." {
				return fmt.Errorf("extract: archive entry %q escapes the destination (..)", name)
			}
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("extract: archive entry %q is a symlink", name)
		}
	}
	return nil
}

// Extract unpacks zipPath into destDir (which must be empty or absent) and
// returns the single top-level .app inside it.
func Extract(zipPath, destDir string) (string, error) {
	if ents, err := os.ReadDir(destDir); err == nil && len(ents) > 0 {
		return "", fmt.Errorf("extract: %s is not empty", destDir)
	}
	if err := checkZipEntries(zipPath); err != nil {
		return "", err
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return "", err
	}
	if _, err := run("ditto", "-x", "-k", zipPath, destDir); err != nil {
		return "", err
	}
	ents, err := os.ReadDir(destDir)
	if err != nil {
		return "", err
	}
	var app string
	for _, e := range ents {
		switch n := e.Name(); {
		case n == "__MACOSX" || n == ".DS_Store":
		case strings.HasSuffix(n, ".app") && e.IsDir() && app == "":
			app = filepath.Join(destDir, n)
		default:
			return "", fmt.Errorf("extract: unexpected top-level entry %q (want exactly one .app)", n)
		}
	}
	if app == "" {
		return "", errors.New("extract: no .app in the archive")
	}
	root, _ := filepath.EvalSymlinks(destDir)
	err = filepath.WalkDir(destDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink == 0 {
			return err
		}
		t, err := os.Readlink(p)
		if err != nil {
			return err
		}
		if filepath.IsAbs(t) {
			return fmt.Errorf("extract: symlink %s points outside the archive", p)
		}
		resolved := filepath.Join(filepath.Dir(p), t)
		if r, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
			resolved = filepath.Join(r, t)
		}
		if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return fmt.Errorf("extract: symlink %s escapes the archive", p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return app, nil
}

// Preflight checks that currentApp can be replaced in place.
func Preflight(currentApp string) error {
	if strings.Contains(currentApp, "/AppTranslocation/") {
		return ErrTranslocated
	}
	f, err := os.CreateTemp(filepath.Dir(currentApp), ".pocketlab-write-test-*")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	name := f.Name()
	f.Close()
	if err := os.Remove(name); err != nil {
		return err
	}
	// The bundle itself must be replaceable too, not only its folder.
	if err := unix.Access(currentApp, unix.W_OK); err != nil {
		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}
	return nil
}

func device(p string) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil
}

// moveDir renames src to dst, falling back to ditto + remove across volumes.
func moveDir(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if _, err := run("ditto", src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}

// swapIn puts staged at current and returns where the previous app ended up.
func swapIn(staged, current string) (old string, err error) {
	if err := unix.RenamexNp(staged, current, unix.RENAME_SWAP); err == nil {
		return staged, nil
	}
	// Fallback: two renames, restoring on failure.
	old = filepath.Join(filepath.Dir(current), ".Pocketlab-old-"+fmt.Sprint(os.Getpid())+".app")
	_ = os.RemoveAll(old)
	if err := os.Rename(current, old); err != nil {
		return "", err
	}
	if err := os.Rename(staged, current); err != nil {
		_ = os.Rename(old, current)
		return "", err
	}
	return old, nil
}

// undoSwap reverses swapIn.
func undoSwap(current, old string) {
	if err := unix.RenamexNp(old, current, unix.RENAME_SWAP); err != nil {
		tmp := current + ".failed"
		if os.Rename(current, tmp) == nil {
			if os.Rename(old, current) == nil {
				_ = os.RemoveAll(tmp)
			} else {
				_ = os.Rename(tmp, current)
			}
		}
	}
}

// stageSameVolume returns a path on the same volume as currentApp holding src.
func stageSameVolume(src, currentApp, tag string) (staged string, copied bool, err error) {
	d1, err := device(src)
	if err != nil {
		return "", false, err
	}
	d2, err := device(filepath.Dir(currentApp))
	if err != nil {
		return "", false, err
	}
	if d1 == d2 {
		return src, false, nil
	}
	staged = filepath.Join(filepath.Dir(currentApp), ".Pocketlab-update-"+tag+".app")
	_ = os.RemoveAll(staged)
	if _, err := run("ditto", src, staged); err != nil {
		_ = os.RemoveAll(staged)
		return "", false, err
	}
	return staged, true, nil
}

func pruneBackups(dir, keepSuffix string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "Pocketlab-") && strings.HasSuffix(e.Name(), keepSuffix) {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// Swap replaces currentApp with the verified candidate and returns the path of
// the previous app, kept as <backupDir>/Pocketlab-<oldver>.app (only the newest
// backup is retained). The new app is re-verified after the swap; on failure
// the old app is put back.
func Swap(candidate, currentApp, backupDir string) (string, error) {
	oldVer := "unknown"
	if bi, err := plistString(currentApp, "CFBundleShortVersionString"); err == nil && bi != "" {
		oldVer = bi
	}
	newVer, _ := plistString(candidate, "CFBundleShortVersionString")
	// The backup is mandatory: make sure it has somewhere to go before touching the app.
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create the backup directory: %w", err)
	}
	staged, copied, err := stageSameVolume(candidate, currentApp, newVer)
	if err != nil {
		return "", err
	}
	oldPath, err := swapIn(staged, currentApp)
	if err != nil {
		if copied {
			_ = os.RemoveAll(staged)
		}
		return "", err
	}
	if err := codesignVerify(currentApp); err != nil {
		undoSwap(currentApp, oldPath)
		return "", err
	}
	// Only now that the installed app verified: drop quarantine on it.
	_, _ = run("xattr", "-dr", "com.apple.quarantine", currentApp)
	return keepOldApp(oldPath, currentApp, backupDir, oldVer), nil
}

// keepOldApp moves the replaced app out of the (to-be-cleaned) staging area and
// into backupDir as Pocketlab-<oldVer>.app. Older backups are pruned only after
// the new one is in place. If backupDir cannot take it, it goes next to the
// installed app instead; only if that fails too is oldPath returned unchanged
// (the caller must then not delete it).
func keepOldApp(oldPath, currentApp, backupDir, oldVer string) string {
	incoming := filepath.Join(backupDir, ".incoming-"+fmt.Sprint(os.Getpid())+".app")
	_ = os.RemoveAll(incoming)
	if err := moveDir(oldPath, incoming); err != nil {
		sibling := filepath.Join(filepath.Dir(currentApp), ".Pocketlab-old-"+oldVer+".app")
		_ = os.RemoveAll(sibling)
		if err := moveDir(oldPath, sibling); err == nil {
			return sibling
		}
		return oldPath
	}
	pruneBackups(backupDir, ".app")
	backup := filepath.Join(backupDir, "Pocketlab-"+oldVer+".app")
	if err := os.Rename(incoming, backup); err != nil {
		return incoming
	}
	return backup
}

// Rollback swaps backupApp back into currentApp. The replaced app is kept next
// to the backup as Pocketlab-<ver>-rolledback.app (older such copies are removed).
func Rollback(currentApp, backupApp string) error {
	if err := Preflight(currentApp); err != nil {
		return err
	}
	if err := codesignVerify(backupApp); err != nil {
		return err
	}
	curVer, _ := plistString(currentApp, "CFBundleShortVersionString")
	staged, copied, err := stageSameVolume(backupApp, currentApp, "rollback")
	if err != nil {
		return err
	}
	oldPath, err := swapIn(staged, currentApp)
	if err != nil {
		if copied {
			_ = os.RemoveAll(staged)
		}
		return err
	}
	if err := codesignVerify(currentApp); err != nil {
		undoSwap(currentApp, oldPath)
		return err
	}
	dir := filepath.Dir(backupApp)
	pruneBackups(dir, "-rolledback.app")
	if curVer == "" {
		curVer = "unknown"
	}
	if err := moveDir(oldPath, filepath.Join(dir, "Pocketlab-"+curVer+"-rolledback.app")); err != nil {
		_ = os.RemoveAll(oldPath)
	}
	if copied {
		_ = os.RemoveAll(backupApp) // the copy now lives at currentApp
	}
	return nil
}

// relaunchEnvPrefixes selects the environment variables a relaunched app keeps
// (data directory, address, update overrides...).
var relaunchEnvPrefixes = []string{"POCKETLAB_", "PL_", "PHONEFORGE_"}

// relaunchOpenArgs builds the `open` argv: a new instance of appPath with the
// selected environment (--env) and the original command-line flags (--args).
func relaunchOpenArgs(appPath string, environ, args []string) []string {
	out := []string{"-n"}
	for _, kv := range environ {
		if !strings.Contains(kv, "=") {
			continue
		}
		for _, p := range relaunchEnvPrefixes {
			if strings.HasPrefix(kv, p) {
				out = append(out, "--env", kv)
				break
			}
		}
	}
	out = append(out, appPath)
	if len(args) > 0 {
		out = append(out, "--args")
		out = append(out, args...)
	}
	return out
}

// relaunchScript waits for the old process ($1) and then runs `open "$@"`. If
// the old process is still alive after ~60 s it gives up instead of opening a
// second instance. Everything variable arrives as positional arguments.
const relaunchScript = `pid="$1"; shift; i=0; while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 120 ]; do sleep 0.5; i=$((i+1)); done; if kill -0 "$pid" 2>/dev/null; then exit 1; fi; exec open "$@"`

// Relaunch starts a detached helper that waits for waitPID to exit and then
// opens appPath as a new instance, keeping the POCKETLAB_/PL_/PHONEFORGE_
// environment and the original flags. It survives the caller's exit.
func Relaunch(appPath string, waitPID int) error {
	argv := []string{"-c", relaunchScript, "sh", fmt.Sprint(waitPID)}
	argv = append(argv, relaunchOpenArgs(appPath, os.Environ(), os.Args[1:])...)
	cmd := exec.Command("/bin/sh", argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

type darwinInstaller struct {
	opts InstallerOptions
	app  string
	err  error

	mu       sync.Mutex
	teamDone bool
	team     string // Team ID of the running app, inspected once
}

// runningTeamID inspects the running app's Team ID once and caches it.
func (i *darwinInstaller) runningTeamID() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.teamDone {
		i.team, i.teamDone = bundleTeamID(i.app), true
	}
	return i.team
}

// NewPlatformInstaller returns the macOS bundle installer.
func NewPlatformInstaller(opts InstallerOptions) Installer {
	in := &darwinInstaller{opts: opts, app: opts.CurrentApp}
	if in.app == "" {
		in.app, in.err = RunningApp()
	}
	return in
}

// AppPath is the bundle this installer replaces.
func (i *darwinInstaller) AppPath() string { return i.app }

func (i *darwinInstaller) Supported() (bool, string) {
	if i.err != nil || i.app == "" {
		return false, "Pocketlab is not running from an app bundle"
	}
	if err := Preflight(i.app); err != nil {
		if errors.Is(err, ErrTranslocated) {
			return false, "Pocketlab is running from a quarantined location; move it to Applications and reopen it"
		}
		return false, "the folder containing Pocketlab is not writable; install the new version from the downloaded disk image"
	}
	// Candidates must carry the running app's Team ID (VerifyCandidate); an
	// ad-hoc signed app has none, so every download would fail verification.
	if !i.opts.AllowAdhoc && i.runningTeamID() == "" {
		return false, "this copy of Pocketlab is not signed with a Developer ID, so it cannot update itself; download the new version manually"
	}
	return true, ""
}

func (i *darwinInstaller) Prepare(archive string, m *Manifest) (string, error) {
	if i.err != nil {
		return "", i.err
	}
	dest := filepath.Join(i.opts.StagingDir, m.Version, "extract")
	_ = os.RemoveAll(dest)
	app, err := Extract(archive, dest)
	if err != nil {
		return "", err
	}
	cur, err := InspectBundle(i.app)
	if err != nil {
		return "", err
	}
	cand, err := InspectBundle(app)
	if err != nil {
		return "", err
	}
	if err := VerifyCandidate(cur, cand, m.Version, i.opts.AllowAdhoc); err != nil {
		_ = os.RemoveAll(dest)
		return "", err
	}
	return app, nil
}

func (i *darwinInstaller) Apply(candidate, version string) (string, error) {
	if err := Preflight(i.app); err != nil {
		return "", err
	}
	return Swap(candidate, i.app, i.opts.BackupDir)
}

func (i *darwinInstaller) Relaunch() error { return Relaunch(i.app, os.Getpid()) }

func (i *darwinInstaller) Rollback(backup string) error {
	if i.err != nil {
		return i.err
	}
	// Only a backup this updater made, inside its backup directory.
	real, err := filepath.EvalSymlinks(backup)
	if err != nil {
		return fmt.Errorf("the previous version's backup is unavailable: %w", err)
	}
	dir, err := filepath.EvalSymlinks(i.opts.BackupDir)
	if err != nil || real == dir || !pathWithin(real, dir) {
		return fmt.Errorf("%w: the backup %q is not inside the backup directory", ErrBundleVerify, backup)
	}
	cur, err := InspectBundle(i.app)
	if err != nil {
		return err
	}
	bak, err := InspectBundle(real)
	if err != nil {
		return err
	}
	if err := VerifyBackup(cur, bak, i.opts.AllowAdhoc); err != nil {
		return err
	}
	return Rollback(i.app, real)
}
