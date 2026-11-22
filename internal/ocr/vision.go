package ocr

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"pocketlab/internal/brand"
	"pocketlab/internal/device"
)

//go:embed helper/plocr.swift
var swiftSource []byte

// warmImage is a small PNG containing real text. Vision only specializes its
// text-recognition models when it actually has text to read, so a blank image
// is useless as a warm-up (it returns instantly and leaves the first real scan
// to pay for the compile).
//
//go:embed helper/warm.png
var warmImage []byte

var (
	compileMu sync.Mutex
	coldMu    sync.Mutex // serializes cold (model-compiling) helper runs
)

const (
	compileTimeout = 180 * time.Second
	runTimeout     = 30 * time.Second
	coldTimeout    = 10 * time.Minute
)

func sourceHash() string {
	h := sha256.Sum256(swiftSource)
	return hex.EncodeToString(h[:])
}

// bundledHelper returns the helper shipped inside the app bundle
// (Pocketlab.app/Contents/Resources/bin/plocr), built and signed at release
// time so the user's Mac needs no swiftc / Xcode CLT. "" when absent.
func bundledHelper() string {
	if brand.Getenv("POCKETLAB_OCR_BIN") != "" {
		return ""
	}
	mu.Lock()
	override := binPath != ""
	mu.Unlock()
	if override {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	p := filepath.Join(filepath.Dir(exe), "..", "Resources", "bin", "plocr")
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		return filepath.Clean(p)
	}
	return ""
}

// warmMarkerPath is where the "models are compiled" marker lives. A bundled
// helper sits in a signed, possibly read-only bundle, so its marker goes to the
// cache dir instead.
func warmMarkerPath(bin string) string {
	if b := bundledHelper(); b != "" && b == bin {
		return filepath.Join(brand.Home(), "bin", "plocr.warm")
	}
	return bin + ".warm"
}

var osBuild = sync.OnceValue(func() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("sysctl", "-n", "kern.osversion").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
})

// warmID identifies the (helper source, OS build) pair the models were
// compiled for; a marker from another pair (OS update, new helper) is stale and
// the next run is treated as cold again.
func warmID() string { return sourceHash() + "|" + osBuild() }

func isWarm(bin string) bool {
	b, err := os.ReadFile(warmMarkerPath(bin))
	return err == nil && strings.TrimSpace(string(b)) == warmID()
}

func markWarm(bin string) {
	p := warmMarkerPath(bin)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(warmID()), 0o644)
}

func helperCompiled() bool {
	if bundledHelper() != "" {
		return true
	}
	bin := helperPath()
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		return false
	}
	b, err := os.ReadFile(bin + ".sha")
	return err == nil && strings.TrimSpace(string(b)) == sourceHash()
}

// EnsureHelper compiles the Swift helper if missing or stale and returns its path.
func EnsureHelper(ctx context.Context) (string, error) {
	if b := bundledHelper(); b != "" {
		return b, nil
	}
	bin := helperPath()
	if helperCompiled() {
		return bin, nil
	}
	swiftc, err := exec.LookPath("swiftc")
	if err != nil {
		return "", fmt.Errorf("ocr: swiftc not found: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return "", err
	}
	// Per-process unique temp names so concurrent processes don't collide.
	src, err := os.CreateTemp(filepath.Dir(bin), "plocr-*.swift")
	if err != nil {
		return "", err
	}
	defer os.Remove(src.Name())
	if _, err := src.Write(swiftSource); err != nil {
		src.Close()
		return "", err
	}
	src.Close()
	tmpBin := src.Name() + ".bin"
	defer os.Remove(tmpBin)

	cctx, cancel := context.WithTimeout(ctx, compileTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, swiftc, "-O", src.Name(), "-o", tmpBin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ocr: compiling helper: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := os.Rename(tmpBin, bin); err != nil {
		return "", err
	}
	if err := os.WriteFile(bin+".sha", []byte(sourceHash()), 0o644); err != nil {
		return "", err
	}
	return bin, nil
}

func recognizeVision(ctx context.Context, png []byte, o Options) ([]Result, error) {
	compileMu.Lock() // serialize first-use compilation
	bin, err := EnsureHelper(ctx)
	compileMu.Unlock()
	if err != nil {
		return nil, err
	}
	return runHelper(ctx, bin, png, o)
}

func runHelper(ctx context.Context, bin string, png []byte, o Options) ([]Result, error) {
	// The first accurate-level run on a machine (or after an OS update) compiles
	// Vision's models, which can take a minute or more; allow a long timeout
	// until a run on real text has succeeded, and run cold runs one at a time so
	// concurrent requests don't each restart (and kill) the compile.
	timeout := runTimeout
	cold := !isWarm(bin) && !o.Fast
	if cold {
		timeout = coldTimeout
		coldMu.Lock()
		defer coldMu.Unlock()
		cold = !isWarm(bin) // another request may have warmed it while we waited
		if !cold {
			timeout = runTimeout
		}
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"-"}
	if o.Words {
		args = append(args, "--words")
	}
	if o.Fast {
		args = append(args, "--fast")
	}
	cmd := exec.CommandContext(rctx, bin, args...)
	cmd.Stdin = bytes.NewReader(png)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if rctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("ocr: helper timed out after %s (Vision is still preparing its models; try again)", timeout)
		}
		return nil, fmt.Errorf("ocr: helper failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	res, err := ParseVisionJSON(stdout.Bytes())
	// Only a run that actually read text proves the models are compiled; a
	// legitimately blank screen returns instantly either way.
	if err == nil && cold && len(res) > 0 {
		markWarm(bin)
	}
	return res, err
}

// Warm makes the first real scan fast: it ensures the helper exists and reads a
// small image containing text so Vision compiles its models now, in the
// background. Failures are returned for logging, never fatal.
func Warm(ctx context.Context) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	compileMu.Lock()
	bin, err := EnsureHelper(ctx)
	compileMu.Unlock()
	if err != nil {
		return err
	}
	res, err := runHelper(ctx, bin, warmImage, Options{})
	if err != nil {
		return err
	}
	if len(res) == 0 {
		return errors.New("ocr: warm-up image produced no text")
	}
	return nil
}

// ParseVisionJSON parses the helper's JSON output.
func ParseVisionJSON(b []byte) ([]Result, error) {
	var items []struct {
		Text       string  `json:"text"`
		X          int     `json:"x"`
		Y          int     `json:"y"`
		W          int     `json:"w"`
		H          int     `json:"h"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(b), &items); err != nil {
		return nil, fmt.Errorf("ocr: parsing helper output: %w", err)
	}
	out := make([]Result, 0, len(items))
	for _, it := range items {
		if strings.TrimSpace(it.Text) == "" {
			continue
		}
		out = append(out, Result{Text: it.Text, Bounds: device.Rect{X: it.X, Y: it.Y, W: it.W, H: it.H}, Confidence: it.Confidence})
	}
	return out, nil
}
