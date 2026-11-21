package iossim

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"phoneforge/internal/device"
)

// IDPrefix is the device id prefix for simulators ("sim-<udid>").
const IDPrefix = "sim-"

// FormatID builds the PhoneForge device id for a simulator udid.
func FormatID(udid string) string { return IDPrefix + udid }

func needWDA() error {
	return fmt.Errorf("simulator UI automation needs WebDriverAgent: start it with POST /simulators/{udid}/wda/start (build it first with scripts/build-wda.sh --sim): %w", device.ErrUnsupported)
}

// simDevice implements device.Device (and several extras) via simctl. UI
// automation is layered on top with wda.Wrap.
type simDevice struct {
	udid string
	info device.Info
	run  Runner
}

var (
	_ device.Device            = (*simDevice)(nil)
	_ device.URLOpener         = (*simDevice)(nil)
	_ device.AppInstaller      = (*simDevice)(nil)
	_ device.Clipboard         = (*simDevice)(nil)
	_ device.LogStreamer       = (*simDevice)(nil)
	_ device.ScreenRecorder    = (*simDevice)(nil)
	_ device.PermissionManager = (*simDevice)(nil)
)

func newDevice(s Sim, run Runner) *simDevice {
	return &simDevice{udid: s.UDID, run: run, info: device.Info{
		ID: FormatID(s.UDID), Serial: s.UDID, Name: s.Name, Platform: device.PlatformIOS,
		Model: s.DeviceType, OSVersion: s.Version, State: "connected", Simulator: true,
	}}
}

func (d *simDevice) Info() device.Info { return d.info }

func (d *simDevice) Screenshot(ctx context.Context) (device.Screenshot, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Newer simctl (Xcode 27) no longer accepts "-" for stdout and writes a file literally
	// named "-" into the cwd; always write to a temp file and fall back to stdout bytes.
	dir, err := os.MkdirTemp("", "pf-simshot-")
	if err != nil {
		return device.Screenshot{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "shot.png")
	data, err := d.run(cctx, "io", d.udid, "screenshot", "--type=png", path)
	if err != nil {
		return device.Screenshot{}, err
	}
	if b, rerr := os.ReadFile(path); rerr == nil && len(b) > 0 {
		data = b
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return device.Screenshot{}, fmt.Errorf("simulator screenshot is not a valid PNG: %w", err)
	}
	return device.Screenshot{PNG: data, Width: cfg.Width, Height: cfg.Height}, nil
}

func (d *simDevice) UITree(ctx context.Context) (device.Element, error) {
	return device.Element{}, needWDA()
}
func (d *simDevice) Tap(ctx context.Context, x, y int) error                 { return needWDA() }
func (d *simDevice) LongPress(ctx context.Context, x, y int, ms int) error   { return needWDA() }
func (d *simDevice) Swipe(ctx context.Context, x1, y1, x2, y2, ms int) error { return needWDA() }
func (d *simDevice) TypeText(ctx context.Context, text string) error         { return needWDA() }
func (d *simDevice) PressKey(ctx context.Context, k device.Key) error        { return needWDA() }

func (d *simDevice) ListApps(ctx context.Context) ([]device.AppInfo, error) {
	b, err := d.run(ctx, "listapps", d.udid)
	if err != nil {
		return nil, err
	}
	apps, err := ParseApps(b)
	if err != nil {
		return nil, err
	}
	out := make([]device.AppInfo, 0, len(apps))
	for _, a := range apps {
		out = append(out, device.AppInfo{BundleID: a.BundleID, Name: a.Name})
	}
	return out, nil
}

func (d *simDevice) LaunchApp(ctx context.Context, id string) error {
	_, err := d.run(ctx, "launch", d.udid, id)
	return err
}

func (d *simDevice) StopApp(ctx context.Context, id string) error {
	_, err := d.run(ctx, "terminate", d.udid, id)
	return err
}

func (d *simDevice) OpenURL(ctx context.Context, url string) error {
	_, err := d.run(ctx, "openurl", d.udid, url)
	return err
}

func (d *simDevice) InstallApp(ctx context.Context, localPath string) error {
	_, err := d.run(ctx, "install", d.udid, localPath)
	return err
}

func (d *simDevice) UninstallApp(ctx context.Context, id string) error {
	_, err := d.run(ctx, "uninstall", d.udid, id)
	return err
}

func (d *simDevice) SetPermission(ctx context.Context, id, permission string, grant bool) error {
	verb := "revoke"
	if grant {
		verb = "grant"
	}
	_, err := d.run(ctx, "privacy", d.udid, verb, permission, id)
	return err
}

func (d *simDevice) GetClipboard(ctx context.Context) (string, error) {
	b, err := d.run(ctx, "pbpaste", d.udid)
	return string(b), err
}

func (d *simDevice) SetClipboard(ctx context.Context, text string) error {
	cmd := exec.CommandContext(ctx, "xcrun", "simctl", "pbcopy", d.udid)
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("simctl pbcopy: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (d *simDevice) StreamLogs(ctx context.Context, w io.Writer) error {
	cmd := exec.CommandContext(ctx, "xcrun", "simctl", "spawn", d.udid, "log", "stream", "--style", "compact")
	cmd.Stdout = w
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// RecordScreen records for seconds and returns MP4/MOV bytes.
func (d *simDevice) RecordScreen(ctx context.Context, seconds int) ([]byte, error) {
	if seconds < 1 {
		seconds = 1
	}
	dir, err := os.MkdirTemp("", "pf-simrec-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "rec.mov")
	cmd := exec.Command("xcrun", "simctl", "io", d.udid, "recordVideo", "--codec=h264", "--force", file)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	select {
	case <-time.After(time.Duration(seconds)*time.Second + time.Second):
	case <-ctx.Done():
	}
	_ = cmd.Process.Signal(syscall.SIGINT)
	_ = cmd.Wait()
	return os.ReadFile(file)
}
