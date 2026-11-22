package androiddrv

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"pocketlab/internal/device"
)

const appsTimeout = 30 * time.Second

// validPkg reports whether s looks like an Android package name (guards
// against shell injection since adb passes args through the device shell).
func validPkg(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
		default:
			return false
		}
	}
	return true
}

// ListApps lists installed package names via `pm list packages`.
func (d *androidDevice) ListApps(ctx context.Context) ([]device.AppInfo, error) {
	ctx, cancel := withTimeout(ctx, appsTimeout)
	defer cancel()
	out, err := d.runText(ctx, "shell", "pm", "list", "packages")
	if err != nil {
		return nil, err
	}
	var apps []device.AppInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "package:") {
			continue
		}
		apps = append(apps, device.AppInfo{BundleID: strings.TrimPrefix(line, "package:")})
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].BundleID < apps[j].BundleID })
	return apps, nil
}

// LaunchApp starts an app's launcher activity using monkey.
func (d *androidDevice) LaunchApp(ctx context.Context, bundleID string) error {
	if !validPkg(bundleID) {
		return errors.New("androiddrv: invalid package name")
	}
	ctx, cancel := withTimeout(ctx, appsTimeout)
	defer cancel()
	_, err := d.run(ctx, "shell", "monkey", "-p", bundleID, "-c", "android.intent.category.LAUNCHER", "1")
	return err
}

// StopApp force-stops an app.
func (d *androidDevice) StopApp(ctx context.Context, bundleID string) error {
	if !validPkg(bundleID) {
		return errors.New("androiddrv: invalid package name")
	}
	ctx, cancel := withTimeout(ctx, appsTimeout)
	defer cancel()
	_, err := d.run(ctx, "shell", "am", "force-stop", bundleID)
	return err
}
