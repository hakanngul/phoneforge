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

var (
	defaultOnce sync.Once
	defaultDir  string
)

// Migrate moves the legacy state directory oldDir to newDir when newDir does not
// exist and oldDir does: it renames, and copies the tree if the rename fails
// (for example across volumes). It reports whether a migration happened. When
// both exist, newDir wins and oldDir is left untouched. When neither exists,
// nothing is created here.
func Migrate(oldDir, newDir string) (bool, error) {
	if _, err := os.Stat(newDir); err == nil {
		return false, nil
	}
	st, err := os.Stat(oldDir)
	if err != nil || !st.IsDir() {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(newDir), 0o755); err != nil {
		return false, err
	}
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
// running the legacy migration once per process (skipped while POCKETLAB_HOME /
// PHONEFORGE_HOME is set). It ignores the override for its result; use Home for
// the server state directory.
func DataDir() string {
	if Getenv(HomeEnv) != "" {
		// An explicit state-dir override (a second instance, tests) must not
		// move the user's real ~/.phoneforge as a side effect.
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, DirName)
		}
		return DirName
	}
	defaultOnce.Do(func() {
		if h, err := os.UserHomeDir(); err == nil {
			defaultDir = DataDirUnder(h)
		} else {
			defaultDir = DirName
		}
	})
	return defaultDir
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
