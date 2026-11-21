package ocr

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"phoneforge/internal/device"
)

//go:embed helper/pfocr.swift
var swiftSource []byte

var compileMu sync.Mutex

const (
	compileTimeout = 180 * time.Second
	runTimeout     = 30 * time.Second
	coldTimeout    = 10 * time.Minute
)

func sourceHash() string {
	h := sha256.Sum256(swiftSource)
	return hex.EncodeToString(h[:])
}

func helperCompiled() bool {
	bin := helperPath()
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		return false
	}
	b, err := os.ReadFile(bin + ".sha")
	return err == nil && strings.TrimSpace(string(b)) == sourceHash()
}

// EnsureHelper compiles the Swift helper if missing or stale and returns its path.
func EnsureHelper(ctx context.Context) (string, error) {
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
	src, err := os.CreateTemp(filepath.Dir(bin), "pfocr-*.swift")
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
	// The first accurate-level run on a machine compiles Vision's models
	// (can take minutes); allow a long timeout until one run has succeeded.
	timeout := runTimeout
	warm := bin + ".warm"
	_, werr := os.Stat(warm)
	cold := werr != nil && !o.Fast
	if cold {
		timeout = coldTimeout
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
		return nil, fmt.Errorf("ocr: helper failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	res, err := ParseVisionJSON(stdout.Bytes())
	if err == nil && cold {
		_ = os.WriteFile(warm, []byte("ok"), 0o644)
	}
	return res, err
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
