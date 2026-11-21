// Package iossim adds iOS Simulator support: discovery of booted simulators,
// a simctl-backed device.Device, and a WebDriverAgent manager so simulators
// are controllable like a real iPhone.
package iossim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"howett.net/plist"
)

// Sim describes one simulator as reported by `simctl list devices -j`.
type Sim struct {
	UDID        string `json:"udid"`
	Name        string `json:"name"`
	State       string `json:"state"`   // Shutdown | Booted | Booting | Shutting Down
	Runtime     string `json:"runtime"` // e.g. "iOS 27.2"
	RuntimeID   string `json:"runtimeId"`
	Platform    string `json:"platform"` // iOS | watchOS | tvOS | xrOS
	Version     string `json:"osVersion"`
	DeviceType  string `json:"deviceType"` // e.g. "iPhone-17"
	IsAvailable bool   `json:"available"`
}

// Booted reports whether the simulator is fully booted.
func (s Sim) Booted() bool { return s.State == "Booted" }

var runtimeRe = regexp.MustCompile(`SimRuntime\.([A-Za-z]+)-(\d+(?:-\d+)*)$`)

// parseRuntime turns "com.apple.CoreSimulator.SimRuntime.iOS-27-2" into
// ("iOS", "27.2").
func parseRuntime(id string) (platform, version string) {
	m := runtimeRe.FindStringSubmatch(id)
	if m == nil {
		return "", ""
	}
	return m[1], strings.ReplaceAll(m[2], "-", ".")
}

// ParseDevices parses `simctl list devices -j` output. Unavailable simulators
// are dropped. The result is sorted by platform, runtime, then name.
func ParseDevices(data []byte) ([]Sim, error) {
	var env struct {
		Devices map[string][]struct {
			UDID        string `json:"udid"`
			Name        string `json:"name"`
			State       string `json:"state"`
			IsAvailable bool   `json:"isAvailable"`
			DeviceType  string `json:"deviceTypeIdentifier"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("simctl list: %w", err)
	}
	var out []Sim
	for rt, list := range env.Devices {
		plat, ver := parseRuntime(rt)
		if plat == "" {
			plat = "unknown"
		}
		for _, d := range list {
			if d.UDID == "" || !d.IsAvailable {
				continue
			}
			label := plat
			if ver != "" {
				label += " " + ver
			}
			out = append(out, Sim{
				UDID: d.UDID, Name: d.Name, State: d.State, Runtime: label, RuntimeID: rt,
				Platform: plat, Version: ver, IsAvailable: true,
				DeviceType: strings.TrimPrefix(d.DeviceType, "com.apple.CoreSimulator.SimDeviceType."),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Platform != b.Platform {
			// iOS first.
			if a.Platform == "iOS" || b.Platform == "iOS" {
				return a.Platform == "iOS"
			}
			return a.Platform < b.Platform
		}
		if a.Runtime != b.Runtime {
			return a.Runtime > b.Runtime
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.UDID < b.UDID
	})
	return out, nil
}

// App is one installed application from `simctl listapps`.
type App struct {
	BundleID string
	Name     string
	Type     string // User | System
}

// ParseApps parses `simctl listapps` output (an OpenStep or XML plist
// dictionary keyed by bundle id). The result is sorted by name.
func ParseApps(data []byte) ([]App, error) {
	var m map[string]struct {
		Display string `plist:"CFBundleDisplayName"`
		Name    string `plist:"CFBundleName"`
		Type    string `plist:"ApplicationType"`
	}
	if _, err := plist.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("simctl listapps: %w", err)
	}
	out := make([]App, 0, len(m))
	for id, v := range m {
		name := v.Display
		if name == "" {
			name = v.Name
		}
		out = append(out, App{BundleID: id, Name: name, Type: v.Type})
	}
	sort.Slice(out, func(i, j int) bool {
		ni, nj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if ni != nj {
			return ni < nj
		}
		return out[i].BundleID < out[j].BundleID
	})
	return out, nil
}

// Runner executes `xcrun simctl <args>` and returns stdout. Replaceable in tests.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// ExecRunner is the real Runner.
func ExecRunner(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "xcrun", append([]string{"simctl"}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), fmt.Errorf("simctl %s: %s", strings.Join(args, " "), msg)
	}
	return out.Bytes(), nil
}

// ListAll returns every available simulator (all states, all platforms).
func ListAll(ctx context.Context, run Runner) ([]Sim, error) {
	b, err := run(ctx, "list", "devices", "-j")
	if err != nil {
		return nil, err
	}
	return ParseDevices(b)
}
