// Package ocr recognizes text in device screenshots. It prefers Apple Vision
// (via an embedded Swift helper compiled on first use) and falls back to the
// tesseract CLI.
package ocr

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"phoneforge/internal/device"
)

// Result is one recognized piece of text. Bounds are in screenshot pixels
// with a top-left origin.
type Result struct {
	Text       string      `json:"text"`
	Bounds     device.Rect `json:"bounds"`
	Confidence float64     `json:"confidence"` // 0..1
}

// Engine names.
const (
	EngineVision    = "vision"
	EngineTesseract = "tesseract"
)

// ErrUnavailable means no OCR engine could be used on this machine.
var ErrUnavailable = errors.New("ocr: no engine available (need macOS swiftc or tesseract on PATH)")

// Options tunes recognition.
type Options struct {
	Words bool // return word-level boxes instead of lines
	Fast  bool // Vision only: fast recognition level
}

var (
	mu      sync.Mutex
	binPath string // override for compiled helper path
)

// SetHelperPath overrides where the compiled Swift helper is cached
// (default ~/.phoneforge/bin/pfocr, or $PHONEFORGE_OCR_BIN).
func SetHelperPath(p string) {
	mu.Lock()
	binPath = p
	mu.Unlock()
}

func helperPath() string {
	mu.Lock()
	defer mu.Unlock()
	if binPath != "" {
		return binPath
	}
	if p := os.Getenv("PHONEFORGE_OCR_BIN"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return home + "/.phoneforge/bin/pfocr"
}

// Available reports which engine would be used.
func Available() (engine string, ok bool) {
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("swiftc"); err == nil {
			return EngineVision, true
		}
		if helperCompiled() {
			return EngineVision, true
		}
	}
	if _, err := exec.LookPath("tesseract"); err == nil {
		return EngineTesseract, true
	}
	return "", false
}

// Recognize returns line-level results for a PNG.
func Recognize(ctx context.Context, png []byte) ([]Result, error) {
	return RecognizeOpts(ctx, png, Options{})
}

// RecognizeOpts is Recognize with options.
func RecognizeOpts(ctx context.Context, png []byte, o Options) ([]Result, error) {
	if len(png) == 0 {
		return nil, errors.New("ocr: empty image")
	}
	var visionErr error
	if runtime.GOOS == "darwin" {
		res, err := recognizeVision(ctx, png, o)
		if err == nil {
			return res, nil
		}
		visionErr = err
		if ctx.Err() != nil {
			return nil, err
		}
	}
	if _, err := exec.LookPath("tesseract"); err == nil {
		return recognizeTesseract(ctx, png, o)
	}
	if visionErr != nil {
		return nil, visionErr
	}
	return nil, ErrUnavailable
}
