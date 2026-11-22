package iosbridge

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pocketlab/internal/brand"
)

// WDATag is the pinned appium/WebDriverAgent release Pocketlab builds. Change
// it together with scripts/fetch-wda.sh (the packaging script reads this value).
const WDATag = "v16.15.3"

// WDATarballName is the file name of the shipped source tarball. It embeds the
// tag, so a new pin never picks up an old tarball.
const WDATarballName = "WebDriverAgent-" + WDATag + ".tar.gz"

// wdaDownloadURL is the network fallback used only when no shipped tarball is found.
const wdaDownloadURL = "https://codeload.github.com/appium/WebDriverAgent/tar.gz/refs/tags/" + WDATag

const srcMarkerName = ".pf-wda-version"

// maxExtractBytes caps the total size extracted from a source tarball.
const maxExtractBytes = 1 << 30

// WDAHome returns the WDA working directory: $PL_WDA_HOME or ~/.pocketlab/wda.
func WDAHome() string {
	if v := brand.Getenv("PL_WDA_HOME"); v != "" {
		return v
	}
	return filepath.Join(brand.DataDir(), "wda")
}

// TarballCandidates lists, in priority order, where the WDA source tarball may
// be: $PL_WDA_TARBALL, <exe dir>/../Resources/ (inside Pocketlab.app),
// <exe dir>/, and <wdaHome>/.
func TarballCandidates(wdaHome string) []string {
	var out []string
	if v := brand.Getenv("PL_WDA_TARBALL"); v != "" {
		out = append(out, v)
	}
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		dir := filepath.Dir(exe)
		out = append(out, filepath.Join(dir, "..", "Resources", WDATarballName), filepath.Join(dir, WDATarballName))
	}
	return append(out, filepath.Join(wdaHome, WDATarballName))
}

// FindTarball returns the first existing candidate ("" when none).
func FindTarball(wdaHome string) string {
	for _, p := range TarballCandidates(wdaHome) {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return filepath.Clean(p)
		}
	}
	return ""
}

// SrcDir is where the WDA source is extracted.
func SrcDir(wdaHome string) string { return filepath.Join(wdaHome, "src") }

// SourceReady reports whether wdaHome/src holds the pinned WDA source.
func SourceReady(wdaHome string) bool {
	src := SrcDir(wdaHome)
	b, err := os.ReadFile(filepath.Join(src, srcMarkerName))
	if err != nil || strings.TrimSpace(string(b)) != WDATag {
		return false
	}
	_, err = os.Stat(filepath.Join(src, "WebDriverAgent.xcodeproj"))
	return err == nil
}

// ExtractTarball unpacks the gzip tarball at tarball into dst, stripping the
// single top-level directory. Entries that would land outside dst (absolute
// paths, "..", links pointing out) are rejected.
func ExtractTarball(tarball, dst string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	return extractTarGz(f, dst)
}

func extractTarGz(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("not a gzip archive: %w", err)
	}
	defer gz.Close()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	root, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	files := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("corrupt archive: %w", err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader || h.Typeflag == tar.TypeXHeader {
			continue
		}
		rel, ok, err := stripTop(h.Name)
		if err != nil {
			return err
		}
		if !ok {
			continue // the top-level directory entry itself
		}
		target := filepath.Join(root, filepath.FromSlash(rel))
		if !within(root, target) {
			return fmt.Errorf("unsafe path in archive: %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if total > maxExtractBytes {
				return errors.New("archive too large")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(h.Mode) & 0o755
			if mode&0o400 == 0 {
				mode |= 0o600
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, io.LimitReader(tr, h.Size)); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			files++
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) || strings.HasPrefix(h.Linkname, "/") {
				return fmt.Errorf("unsafe symlink target in archive: %q -> %q", h.Name, h.Linkname)
			}
			lt := filepath.Join(filepath.Dir(target), filepath.FromSlash(h.Linkname))
			if !within(root, lt) {
				return fmt.Errorf("unsafe symlink target in archive: %q -> %q", h.Name, h.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			return fmt.Errorf("hard links are not allowed in the archive: %q", h.Name)
		default:
			// devices, fifos...: ignore
		}
	}
	if files == 0 {
		return errors.New("archive contained no files")
	}
	return nil
}

// stripTop removes the first path component. ok is false for the top-level
// directory entry itself. Absolute paths and ".." components are errors.
func stripTop(name string) (rel string, ok bool, err error) {
	if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "\x00") {
		return "", false, fmt.Errorf("unsafe path in archive: %q", name)
	}
	parts := strings.Split(name, "/")
	for _, p := range parts {
		if p == ".." {
			return "", false, fmt.Errorf("unsafe path in archive: %q", name)
		}
	}
	var keep []string
	for i, p := range parts {
		if i == 0 {
			continue
		}
		if p != "" && p != "." {
			keep = append(keep, p)
		}
	}
	if len(keep) == 0 {
		return "", false, nil
	}
	return strings.Join(keep, "/"), true, nil
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// PrepareSource makes sure wdaHome/src holds the pinned source. It extracts
// the shipped tarball (re-extracting when the version marker differs) and,
// only when no tarball exists, downloads one. note receives progress lines.
func PrepareSource(ctx context.Context, wdaHome string, note func(string)) error {
	if note == nil {
		note = func(string) {}
	}
	if SourceReady(wdaHome) {
		return nil
	}
	tb := FindTarball(wdaHome)
	if tb == "" {
		note("No bundled WebDriverAgent source found; downloading " + WDATag + " from GitHub (needs internet)...")
		p, err := downloadTarball(ctx, wdaHome)
		if err != nil {
			return &Issue{
				Code:    CodeSourceMissing,
				Message: "The WebDriverAgent source was not found next to Pocketlab and could not be downloaded: " + err.Error(),
				Hint:    "Reinstall Pocketlab from the official release (it ships the source), connect to the internet and retry, or set PL_WDA_TARBALL to a " + WDATarballName + " file.",
			}
		}
		tb = p
	}
	note("Extracting WebDriverAgent " + WDATag + " from " + tb)
	if err := os.MkdirAll(wdaHome, 0o755); err != nil {
		return err
	}
	src := SrcDir(wdaHome)
	tmp := src + ".new"
	_ = os.RemoveAll(tmp)
	if err := ExtractTarball(tb, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return &Issue{Code: CodeSourceMissing, Message: "Could not extract " + tb + ": " + err.Error(), Hint: "The tarball looks damaged. Reinstall Pocketlab or delete it and retry."}
	}
	if _, err := os.Stat(filepath.Join(tmp, "WebDriverAgent.xcodeproj")); err != nil {
		_ = os.RemoveAll(tmp)
		return &Issue{Code: CodeSourceMissing, Message: "The tarball " + tb + " does not contain WebDriverAgent.xcodeproj.", Hint: "Use the " + WDATarballName + " that ships with Pocketlab."}
	}
	if err := os.WriteFile(filepath.Join(tmp, srcMarkerName), []byte(WDATag+"\n"), 0o644); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	_ = os.RemoveAll(src)
	if err := os.Rename(tmp, src); err != nil {
		return err
	}
	return nil
}

// downloadFn is replaceable in tests.
var downloadFn = defaultDownload

func downloadTarball(ctx context.Context, wdaHome string) (string, error) {
	if err := os.MkdirAll(wdaHome, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(wdaHome, WDATarballName)
	if err := downloadFn(ctx, wdaDownloadURL, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func defaultDownload(ctx context.Context, url, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, maxExtractBytes)); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
