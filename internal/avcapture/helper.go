// Package avcapture mirrors real iPhones through macOS's native USB screen
// capture (CoreMediaIO + AVCaptureSession, the mechanism QuickTime uses) via a
// small Swift helper, plcapture, compiled on demand.
package avcapture

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

	"pocketlab/internal/brand"
)

//go:embed helper/plcapture.swift
var swiftSource []byte

// infoPlist is linked into the helper so macOS can show the Camera prompt.
const infoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>dev.pocketlab.plcapture</string>
<key>CFBundleName</key><string>plcapture</string>
<key>NSCameraUsageDescription</key><string>Pocketlab mirrors the screen of a connected iPhone.</string>
</dict></plist>
`

var compileMu sync.Mutex

const compileTimeout = 180 * time.Second

func sourceHash() string {
	h := sha256.Sum256(append(append([]byte{}, swiftSource...), infoPlist...))
	return hex.EncodeToString(h[:])
}

// HelperPath is where the compiled helper lives (POCKETLAB_AVCAPTURE_BIN
// overrides; default ~/.pocketlab/bin/plcapture).
func HelperPath() string {
	if p := brand.Getenv("POCKETLAB_AVCAPTURE_BIN"); p != "" {
		return p
	}
	return filepath.Join(brand.Home(), "bin", "plcapture")
}

func helperCompiled(bin string) bool {
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		return false
	}
	b, err := os.ReadFile(bin + ".sha")
	return err == nil && strings.TrimSpace(string(b)) == sourceHash()
}

// EnsureHelper compiles the Swift helper if missing or stale and returns its path.
func EnsureHelper(ctx context.Context) (string, error) {
	compileMu.Lock()
	defer compileMu.Unlock()
	bin := HelperPath()
	if helperCompiled(bin) {
		return bin, nil
	}
	swiftc, err := exec.LookPath("swiftc")
	if err != nil {
		return "", fmt.Errorf("avcapture: swiftc not found: %w", err)
	}
	dir := filepath.Dir(bin)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	src, err := os.CreateTemp(dir, "plcapture-*.swift")
	if err != nil {
		return "", err
	}
	defer os.Remove(src.Name())
	if _, err := src.Write(swiftSource); err != nil {
		src.Close()
		return "", err
	}
	src.Close()
	plist := src.Name() + ".plist"
	if err := os.WriteFile(plist, []byte(infoPlist), 0o644); err != nil {
		return "", err
	}
	defer os.Remove(plist)
	tmpBin := src.Name() + ".bin"
	defer os.Remove(tmpBin)

	cctx, cancel := context.WithTimeout(ctx, compileTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, swiftc, "-O", src.Name(), "-o", tmpBin,
		"-Xlinker", "-sectcreate", "-Xlinker", "__TEXT", "-Xlinker", "__info_plist", "-Xlinker", plist)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("avcapture: compiling helper: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	// Re-sign ad hoc so the embedded Info.plist is bound to the signature.
	if cs, err := exec.LookPath("codesign"); err == nil {
		_ = exec.CommandContext(cctx, cs, "-s", "-", "-f", tmpBin).Run()
	}
	if err := os.Rename(tmpBin, bin); err != nil {
		return "", err
	}
	if err := os.WriteFile(bin+".sha", []byte(sourceHash()), 0o644); err != nil {
		return "", err
	}
	return bin, nil
}

// CaptureDevice is one entry of `plcapture list`.
type CaptureDevice struct {
	ID           string `json:"id"` // AVCaptureDevice.uniqueID (a GUID, not the UDID)
	Name         string `json:"name"`
	Model        string `json:"model"`
	Manufacturer string `json:"manufacturer"`
	Connected    bool   `json:"connected"`
	InUseByOther bool   `json:"inUseByAnotherApplication"`
	Suspended    bool   `json:"suspended"`
}

// Listing is the JSON `plcapture list` prints.
type Listing struct {
	Authorization string          `json:"authorization"` // authorized|denied|restricted|notDetermined
	Devices       []CaptureDevice `json:"devices"`
}

// ParseListing decodes `plcapture list` output.
func ParseListing(b []byte) (Listing, error) {
	var l Listing
	err := json.Unmarshal(bytes.TrimSpace(b), &l)
	return l, err
}

func runList(ctx context.Context) (Listing, error) {
	bin, err := EnsureHelper(ctx)
	if err != nil {
		return Listing{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "list").Output()
	if err != nil {
		return Listing{}, fmt.Errorf("avcapture: list: %w", err)
	}
	return ParseListing(out)
}

// Error is a classified helper failure.
type Error struct {
	Code string // TCC_DENIED, DEVICE_NOT_FOUND, BUSY, NO_FRAMES, HELPER_FAILED
	Msg  string
}

func (e *Error) Error() string { return "avcapture " + e.Code + ": " + e.Msg }

// Fatal errors will not be fixed by simply restarting the helper.
func (e *Error) Fatal() bool {
	switch e.Code {
	case "TCC_DENIED", "DEVICE_NOT_FOUND", "BUSY", "NO_FRAMES":
		return true
	}
	return false
}

// classifyExit maps a helper exit code plus its last stderr event to an Error.
func classifyExit(code int, stderrLast string) *Error {
	msg := stderrLast
	var ev struct{ Message string }
	if json.Unmarshal([]byte(stderrLast), &ev) == nil && ev.Message != "" {
		msg = ev.Message
	}
	switch code {
	case 10:
		return &Error{"TCC_DENIED", msg}
	case 11:
		return &Error{"DEVICE_NOT_FOUND", msg}
	case 12:
		return &Error{"BUSY", msg}
	case 13:
		return &Error{"NO_FRAMES", msg}
	}
	if msg == "" {
		msg = fmt.Sprintf("helper exited with code %d", code)
	}
	return &Error{"HELPER_FAILED", msg}
}
