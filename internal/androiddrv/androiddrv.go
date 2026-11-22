// Package androiddrv implements the Pocketlab device contract for Android
// by shelling out to the adb command-line tool. No on-device agent is used.
package androiddrv

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"pocketlab/internal/device"
	"pocketlab/internal/registry"
)

const (
	defaultADB     = "adb"
	discoverTimout = 10 * time.Second
	propTimeout    = 5 * time.Second
)

type discoverer struct {
	adbPath string
}

// NewDiscoverer returns a registry.Discoverer that lists devices via
// `adb devices -l`. If adbPath is empty, "adb" from PATH is used.
func NewDiscoverer(adbPath string) registry.Discoverer {
	if adbPath == "" {
		adbPath = defaultADB
	}
	return &discoverer{adbPath: adbPath}
}

// Discover runs `adb devices -l` and returns one device per listed serial.
// Devices that are offline or unauthorized are returned with State set
// accordingly and without model/OS metadata.
func (d *discoverer) Discover(ctx context.Context) ([]device.Device, error) {
	ctx, cancel := context.WithTimeout(ctx, discoverTimout)
	defer cancel()
	out, err := runGlobal(ctx, d.adbPath, "devices", "-l")
	if err != nil {
		return nil, err
	}
	var devs []device.Device
	for _, e := range parseDevices(string(out)) {
		dev := &androidDevice{adbPath: d.adbPath, serial: e.serial}
		switch e.state {
		case "device":
			pctx, pcancel := context.WithTimeout(ctx, propTimeout)
			dev.loadInfo(pctx)
			pcancel()
		case "offline", "unauthorized":
			dev.setBase(e.state)
		default:
			continue
		}
		devs = append(devs, dev)
	}
	return devs, nil
}

type deviceEntry struct {
	serial string
	state  string
}

// parseDevices parses the output of `adb devices -l`.
func parseDevices(out string) []deviceEntry {
	var res []deviceEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") || strings.HasPrefix(line, "*") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		res = append(res, deviceEntry{serial: f[0], state: f[1]})
	}
	return res
}

// androidDevice is a single adb-attached Android device.
type androidDevice struct {
	adbPath string
	serial  string
	info    device.Info
}

var _ device.Device = (*androidDevice)(nil)

// sanitizeSerial keeps [A-Za-z0-9._-] and replaces everything else with '-'.
func sanitizeSerial(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '-'
	}, s)
}

func (d *androidDevice) setBase(state string) {
	d.info = device.Info{
		ID:       "android-" + sanitizeSerial(d.serial),
		Serial:   d.serial,
		Name:     d.serial,
		Platform: device.PlatformAndroid,
		State:    state,
	}
}

// loadInfo populates the cached Info from device properties.
func (d *androidDevice) loadInfo(ctx context.Context) {
	d.setBase("connected")
	if out, err := d.run(ctx, "shell", "getprop", "ro.product.model"); err == nil {
		d.info.Model = strings.TrimSpace(string(out))
	}
	if out, err := d.run(ctx, "shell", "getprop", "ro.build.version.release"); err == nil {
		d.info.OSVersion = strings.TrimSpace(string(out))
	}
	if d.info.Model != "" {
		d.info.Name = d.info.Model
	}
}

// Info returns the cached device description.
func (d *androidDevice) Info() device.Info { return d.info }

// run executes `adb -s <serial> <args...>` and returns stdout.
func (d *androidDevice) run(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"-s", d.serial}, args...)
	return runGlobal(ctx, d.adbPath, full...)
}

// runGlobal executes adb with the given args and returns stdout. On failure
// the error includes trimmed stderr.
func runGlobal(ctx context.Context, adbPath string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, adbPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("adb %s: %w", strings.Join(args, " "), ctx.Err())
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("adb %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return stdout.Bytes(), nil
}

// runText runs an adb command and returns trimmed output text.
func (d *androidDevice) runText(ctx context.Context, args ...string) (string, error) {
	out, err := d.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n")), nil
}

// withTimeout applies a default deadline if ctx has none.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}
