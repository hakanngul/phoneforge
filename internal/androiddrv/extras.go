package androiddrv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pocketlab/internal/device"
)

var (
	_ device.GestureExtras     = (*androidDevice)(nil)
	_ device.URLOpener         = (*androidDevice)(nil)
	_ device.Clipboard         = (*androidDevice)(nil)
	_ device.AppInstaller      = (*androidDevice)(nil)
	_ device.PermissionManager = (*androidDevice)(nil)
	_ device.LogStreamer       = (*androidDevice)(nil)
	_ device.ScreenRecorder    = (*androidDevice)(nil)
	_ device.KeyboardDismisser = (*androidDevice)(nil)
)

const (
	installTimeout = 5 * time.Minute
	remoteRecPath  = "/sdcard/pf_rec.mp4"
	minRecSeconds  = 1
	maxRecSeconds  = 180
)

var (
	urlSchemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
	permNameRe  = regexp.MustCompile(`^[A-Za-z0-9._]+$`)
)

// shellQuote single-quotes s for the device shell (adb joins args and runs
// them through `sh -c`).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// validateURL checks the URL has a scheme and no control characters.
func validateURL(u string) error {
	if u == "" {
		return errors.New("androiddrv: empty url")
	}
	if !urlSchemeRe.MatchString(u) {
		return errors.New("androiddrv: url must start with a scheme such as https:")
	}
	for _, r := range u {
		if r < 32 || r == 127 {
			return errors.New("androiddrv: url contains control characters")
		}
	}
	return nil
}

// clampSeconds limits a recording length to 1..180 seconds.
func clampSeconds(s int) int {
	if s < minRecSeconds {
		return minRecSeconds
	}
	if s > maxRecSeconds {
		return maxRecSeconds
	}
	return s
}

// DoubleTap sends two taps in a single adb shell invocation so the gap
// between them stays short.
func (d *androidDevice) DoubleTap(ctx context.Context, x, y int) error {
	ctx, cancel := withTimeout(ctx, inputTimeout)
	defer cancel()
	xs, ys := strconv.Itoa(x), strconv.Itoa(y)
	cmd := fmt.Sprintf("input tap %s %s && input tap %s %s", xs, ys, xs, ys)
	_, err := d.run(ctx, "shell", cmd)
	return err
}

// Drag uses `input draganddrop` (Android 8+) and falls back to a swipe.
func (d *androidDevice) Drag(ctx context.Context, x1, y1, x2, y2, ms int) error {
	args := append([]string{"draganddrop"}, itoa(x1, y1, x2, y2, ms)...)
	if err := d.input(ctx, args...); err == nil {
		return nil
	} else if ctx.Err() != nil {
		return err
	}
	return d.Swipe(ctx, x1, y1, x2, y2, ms)
}

// Pinch is not possible with the `input` tool, which only injects a single
// pointer. It always returns device.ErrUnsupported.
func (d *androidDevice) Pinch(ctx context.Context, cx, cy int, scale float64, ms int) error {
	return fmt.Errorf("androiddrv: pinch needs two simultaneous pointers, which adb input cannot inject; a UiAutomator2 bridge is required: %w", device.ErrUnsupported)
}

// OpenURL opens a URL with an ACTION_VIEW intent.
func (d *androidDevice) OpenURL(ctx context.Context, url string) error {
	if err := validateURL(url); err != nil {
		return err
	}
	ctx, cancel := withTimeout(ctx, appsTimeout)
	defer cancel()
	cmd := "am start -a android.intent.action.VIEW -d " + shellQuote(url)
	out, err := d.run(ctx, "shell", cmd)
	if err != nil {
		return err
	}
	if s := string(out); strings.Contains(s, "Error:") || strings.Contains(s, "Exception") {
		return fmt.Errorf("androiddrv: open url: %s", strings.TrimSpace(s))
	}
	return nil
}

const clipboardUnsupportedMsg = "androiddrv: clipboard access is not available through stock adb on this device (no `cmd clipboard` implementation; Android 10+ blocks background clipboard access): %w"

func clipboardUnsupported(detail string) error {
	_ = detail
	return fmt.Errorf(clipboardUnsupportedMsg, device.ErrUnsupported)
}

func clipboardNotImplemented(out string) bool {
	return strings.Contains(out, "No shell command implementation") ||
		strings.Contains(out, "Unknown command") ||
		strings.Contains(out, "Can't find service")
}

// SetClipboard tries `cmd clipboard set-primary-clip`; devices without that
// shell command return device.ErrUnsupported.
func (d *androidDevice) SetClipboard(ctx context.Context, text string) error {
	ctx, cancel := withTimeout(ctx, inputTimeout)
	defer cancel()
	out, err := d.run(ctx, "shell", "cmd clipboard set-primary-clip "+shellQuote(text))
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return clipboardUnsupported(err.Error())
	}
	if clipboardNotImplemented(string(out)) {
		return clipboardUnsupported(string(out))
	}
	return nil
}

// GetClipboard tries `cmd clipboard get-primary-clip`; devices without that
// shell command return device.ErrUnsupported.
func (d *androidDevice) GetClipboard(ctx context.Context) (string, error) {
	ctx, cancel := withTimeout(ctx, inputTimeout)
	defer cancel()
	out, err := d.runText(ctx, "shell", "cmd", "clipboard", "get-primary-clip")
	if err != nil {
		if ctx.Err() != nil {
			return "", err
		}
		return "", clipboardUnsupported(err.Error())
	}
	if clipboardNotImplemented(out) {
		return "", clipboardUnsupported(out)
	}
	return out, nil
}

// InstallApp installs an .apk with `adb install -r`.
func (d *androidDevice) InstallApp(ctx context.Context, localPath string) error {
	if !strings.HasSuffix(strings.ToLower(localPath), ".apk") {
		return errors.New("androiddrv: install path must end in .apk")
	}
	st, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("androiddrv: apk: %w", err)
	}
	if st.IsDir() {
		return errors.New("androiddrv: apk path is a directory")
	}
	ctx, cancel := withTimeout(ctx, installTimeout)
	defer cancel()
	out, err := d.run(ctx, "install", "-r", localPath)
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "Success") {
		return fmt.Errorf("androiddrv: install failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// UninstallApp removes a package with `adb uninstall`.
func (d *androidDevice) UninstallApp(ctx context.Context, bundleID string) error {
	if !validPkg(bundleID) {
		return errors.New("androiddrv: invalid package name")
	}
	ctx, cancel := withTimeout(ctx, appsTimeout)
	defer cancel()
	out, err := d.run(ctx, "uninstall", bundleID)
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "Success") {
		return fmt.Errorf("androiddrv: uninstall failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// SetPermission grants or revokes a runtime permission with `pm`.
func (d *androidDevice) SetPermission(ctx context.Context, bundleID, permission string, grant bool) error {
	if !validPkg(bundleID) {
		return errors.New("androiddrv: invalid package name")
	}
	if !permNameRe.MatchString(permission) {
		return errors.New("androiddrv: invalid permission name")
	}
	verb := "revoke"
	if grant {
		verb = "grant"
	}
	ctx, cancel := withTimeout(ctx, appsTimeout)
	defer cancel()
	_, err := d.run(ctx, "shell", "pm", verb, bundleID, permission)
	return err
}

// RecordScreen records the screen with screenrecord, pulls the MP4 over
// exec-out and deletes the temporary file. seconds is clamped to 1..180.
func (d *androidDevice) RecordScreen(ctx context.Context, seconds int) ([]byte, error) {
	seconds = clampSeconds(seconds)
	ctx, cancel := withTimeout(ctx, time.Duration(seconds+60)*time.Second)
	defer cancel()
	defer func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer rcancel()
		_, _ = d.run(rctx, "shell", "rm", "-f", remoteRecPath)
	}()
	if _, err := d.run(ctx, "shell", "screenrecord", "--time-limit", strconv.Itoa(seconds), remoteRecPath); err != nil {
		return nil, err
	}
	data, err := d.run(ctx, "exec-out", "cat", remoteRecPath)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("androiddrv: screenrecord produced no data")
	}
	return data, nil
}

// DismissKeyboard sends KEYCODE_ESCAPE (111). BACK (4) is avoided because
// when no keyboard is shown it navigates away from the current screen.
func (d *androidDevice) DismissKeyboard(ctx context.Context) error {
	return d.input(ctx, "keyevent", "111")
}
