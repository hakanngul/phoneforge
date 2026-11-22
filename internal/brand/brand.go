// Package brand holds the product naming and the compatibility shims for the
// PhoneForge -> Pocketlab rename: environment variables (new name first, old
// name as fallback), the state directory (~/.phoneforge is migrated to
// ~/.pocketlab) and the legacy HTTP header names.
package brand

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// Name is the product display name.
	Name = "Pocketlab"
	// LegacyName is the product's former name (compat only).
	LegacyName = "PhoneForge"

	// DirName / LegacyDirName are the state directory names under $HOME.
	DirName       = ".pocketlab"
	LegacyDirName = ".phoneforge"
)

// Legacy HTTP header names still accepted from older clients, and still sent
// by our own non-browser clients so a new CLI/desktop can stop an old server.
const (
	LegacyActionHeader    = "X-PhoneForge-Action"
	LegacyOriginHeader    = "X-PhoneForge-Origin"
	LegacySignatureHeader = "X-PhoneForge-Signature"
	LegacyMJPEGAccept     = "application/x-pf-mjpeg"
)

// LegacyEnvName maps a new environment variable name to its former name
// (POCKETLAB_X -> PHONEFORGE_X, PL_X -> PF_X). It returns "" for other names.
func LegacyEnvName(name string) string {
	switch {
	case strings.HasPrefix(name, "POCKETLAB_"):
		return "PHONEFORGE_" + strings.TrimPrefix(name, "POCKETLAB_")
	case strings.HasPrefix(name, "PL_"):
		return "PF_" + strings.TrimPrefix(name, "PL_")
	}
	return ""
}

// Lookup reads name (a POCKETLAB_* / PL_* variable) through get and, when it is
// empty, falls back to the legacy PHONEFORGE_* / PF_* variable.
func Lookup(get func(string) string, name string) string {
	if v := get(name); v != "" {
		return v
	}
	if old := LegacyEnvName(name); old != "" {
		return get(old)
	}
	return ""
}

// Getenv is Lookup over the process environment.
func Getenv(name string) string { return Lookup(os.Getenv, name) }

// HomeEnv is the state directory override variable.
const HomeEnv = "POCKETLAB_HOME"

// logw receives the one-time migration message.
var logw io.Writer = os.Stderr

var migrated sync.Map // userHome -> struct{}: migration attempted this process

// Migrate moves the legacy state directory oldDir to newDir. It reports whether
// anything was migrated.
//
//   - newDir missing, oldDir present: oldDir is renamed to newDir (copied if the
//     rename fails, e.g. across volumes).
//   - both present: the trees are merged. Every legacy entry that is absent from
//     newDir is moved over; an entry that is an empty/auto-created tree in newDir
//     (empty directories) is replaced by the legacy one; the helper cache bin/
//     prefers the legacy files; server-events.jsonl history is prepended. For any
//     other conflict newDir wins and the legacy file stays behind. What is left
//     of oldDir is renamed to <oldDir>.migrated-<timestamp> (never deleted), or
//     removed if it ended up empty.
//
// A lock file next to oldDir keeps two starting processes from merging at once.
func Migrate(oldDir, newDir string) (bool, error) {
	st, err := os.Stat(oldDir)
	if err != nil || !st.IsDir() {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(newDir), 0o755); err != nil {
		return false, err
	}
	unlock, ok := lockMigration(oldDir + ".migrate.lock")
	if !ok {
		return false, nil // another process is migrating; it will finish the job
	}
	defer unlock()
	// Re-check under the lock: the other process may have finished meanwhile.
	if st, err := os.Stat(oldDir); err != nil || !st.IsDir() {
		return false, nil
	}
	if _, err := os.Stat(newDir); err != nil {
		if err := os.Rename(oldDir, newDir); err == nil {
			fmt.Fprintf(logw, "%s: migrated state directory %s -> %s\n", Name, oldDir, newDir)
			return true, nil
		}
		if err := copyTree(oldDir, newDir); err != nil {
			_ = os.RemoveAll(newDir)
			return false, fmt.Errorf("migrate %s: %w", oldDir, err)
		}
		fmt.Fprintf(logw, "%s: migrated state directory %s -> %s (copied; the old directory was left in place)\n", Name, oldDir, newDir)
		return true, nil
	}
	moved, kept, err := mergeDirs(oldDir, newDir, true)
	if err != nil {
		return moved > 0, fmt.Errorf("merge %s into %s: %w", oldDir, newDir, err)
	}
	if left, _ := os.ReadDir(oldDir); len(left) == 0 {
		_ = os.Remove(oldDir)
	} else {
		dst := fmt.Sprintf("%s.migrated-%s", oldDir, time.Now().Format("20060102-150405"))
		if err := os.Rename(oldDir, dst); err != nil {
			fmt.Fprintf(logw, "%s: could not archive leftover %s: %v\n", Name, oldDir, err)
		} else {
			fmt.Fprintf(logw, "%s: %d conflicting entr(ies) kept from the new directory; legacy leftovers archived in %s\n", Name, kept, dst)
		}
	}
	if moved > 0 {
		fmt.Fprintf(logw, "%s: merged %d entr(ies) from %s into %s\n", Name, moved, oldDir, newDir)
	}
	return moved > 0, nil
}

// lockMigration takes an exclusive lock file; a stale one (older than a
// minute) is broken. It waits up to ~10s for a live holder.
func lockMigration(path string) (func(), bool) {
	for i := 0; i < 100; i++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { _ = os.Remove(path) }, true
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > time.Minute {
			_ = os.Remove(path)
			continue
		}
		time.Sleep(100 * time.Millisecond)
	}
	return func() {}, false
}

// hasFiles reports whether the tree under dir holds anything but directories.
func hasFiles(dir string) bool {
	found := false
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// moveEntry renames src to dst, falling back to copy + remove across volumes.
func moveEntry(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}

// mergeDirs moves entries of src into dst (see Migrate). It returns how many
// entries were moved and how many conflicts were left in src.
func mergeDirs(src, dst string, top bool) (moved, kept int, err error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		name := e.Name()
		s, d := filepath.Join(src, name), filepath.Join(dst, name)
		dinfo, derr := os.Lstat(d)
		if derr != nil { // absent in dst
			if err := moveEntry(s, d); err != nil {
				return moved, kept, err
			}
			moved++
			continue
		}
		sinfo, serr := os.Lstat(s)
		if serr != nil {
			continue
		}
		switch {
		case top && name == "server-events.jsonl" && sinfo.Mode().IsRegular() && dinfo.Mode().IsRegular():
			// Keep the whole history: legacy lines first, then the new ones.
			ob, e1 := os.ReadFile(s)
			nb, e2 := os.ReadFile(d)
			if e1 != nil || e2 != nil {
				kept++
				continue
			}
			if len(ob) > 0 && ob[len(ob)-1] != '\n' {
				ob = append(ob, '\n')
			}
			tmp := d + ".merge-tmp"
			if err := os.WriteFile(tmp, append(ob, nb...), dinfo.Mode().Perm()); err != nil {
				return moved, kept, err
			}
			if err := os.Rename(tmp, d); err != nil {
				return moved, kept, err
			}
			_ = os.Remove(s)
			moved++
		case top && name == "server.state":
			// Live process state of the running (new) server: never overwrite.
			kept++
		case sinfo.IsDir() && dinfo.IsDir() && !hasFiles(d):
			// New side is only auto-created empty directories: legacy wins.
			if err := os.RemoveAll(d); err != nil {
				return moved, kept, err
			}
			if err := moveEntry(s, d); err != nil {
				return moved, kept, err
			}
			moved++
		case top && name == "bin" && sinfo.IsDir() && dinfo.IsDir():
			// Regenerable helper caches (plocr, plocr.sha, plocr.warm ...):
			// prefer the legacy copies, keep the pair consistent.
			bes, _ := os.ReadDir(s)
			for _, be := range bes {
				bs, bd := filepath.Join(s, be.Name()), filepath.Join(d, be.Name())
				_ = os.RemoveAll(bd)
				if err := moveEntry(bs, bd); err != nil {
					return moved, kept, err
				}
				moved++
			}
		case sinfo.IsDir() && dinfo.IsDir():
			m, k, err := mergeDirs(s, d, false)
			moved += m
			kept += k
			if err != nil {
				return moved, kept, err
			}
			_ = os.Remove(s) // only succeeds when emptied
		default:
			kept++ // same name, new side has real content: new wins
		}
	}
	return moved, kept, nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, target)
		case info.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, b, info.Mode().Perm())
		}
		return nil
	})
}

// DataDirUnder returns <userHome>/.pocketlab, migrating <userHome>/.phoneforge
// first when needed. It never creates the directory when neither exists.
func DataDirUnder(userHome string) string {
	nd := filepath.Join(userHome, DirName)
	if _, err := Migrate(filepath.Join(userHome, LegacyDirName), nd); err != nil {
		log.Printf("%s: state directory migration failed: %v (continuing with %s)", Name, err, nd)
	}
	return nd
}

// DataDir returns ~/.pocketlab (or ./.pocketlab without a home directory),
// running the legacy migration once per home per process (skipped while
// POCKETLAB_HOME / PHONEFORGE_HOME is set). It ignores the override for its
// result, so state code must use Home/HomeFrom instead.
func DataDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return DirName
	}
	if Getenv(HomeEnv) != "" {
		// An explicit state-dir override (a second instance, tests) must not
		// move the user's real ~/.phoneforge as a side effect.
		return filepath.Join(h, DirName)
	}
	if _, loaded := migrated.LoadOrStore(h, struct{}{}); !loaded {
		return DataDirUnder(h)
	}
	return filepath.Join(h, DirName)
}

// HomeFrom resolves the state directory through get: $POCKETLAB_HOME (or the
// legacy $PHONEFORGE_HOME) when set, else the migrated default ~/.pocketlab.
// An explicit override is never migrated.
func HomeFrom(get func(string) string) string {
	if h := Lookup(get, HomeEnv); h != "" {
		return h // lets a second instance (tests) keep its state apart
	}
	return DataDir()
}

// Home is HomeFrom over the process environment.
func Home() string { return HomeFrom(os.Getenv) }

// HeaderAny returns the first non-empty value among the given header lookups.
func HeaderAny(get func(string) string, names ...string) string {
	for _, n := range names {
		if v := get(n); v != "" {
			return v
		}
	}
	return ""
}
