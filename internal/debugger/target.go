package debugger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"phoneforge/internal/device"
)

// ErrNotSupported is returned for platforms without debugger support.
var ErrNotSupported = fmt.Errorf("debugging is only supported on iOS devices and simulators; Android (jdb/lldb) is not supported yet: %w", device.ErrUnsupported)

// Target describes what to debug.
type Target struct {
	Platform    device.Platform   `json:"platform"`
	Serial      string            `json:"serial"` // udid
	BundleID    string            `json:"bundleId,omitempty"`
	PID         int               `json:"pid,omitempty"`
	ProcessName string            `json:"processName,omitempty"`
	Launch      bool              `json:"launch,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

// Runner executes a host command and returns stdout (seam for tests).
type Runner func(ctx context.Context, env []string, name string, args ...string) (string, error)

// ExecRunner is the production Runner.
func ExecRunner(ctx context.Context, env []string, name string, args ...string) (string, error) {
	if runtime.GOOS != "darwin" && (name == "xcrun" || name == "lldb") {
		return "", errors.New("the iOS debugger requires macOS")
	}
	c := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		c.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return stdout.String(), fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, msg)
	}
	return stdout.String(), nil
}

// plan is what must be run in lldb to reach the target process.
type plan struct {
	pid      int
	commands []string
	note     string
}

var (
	bundleRe  = regexp.MustCompile(`^[A-Za-z0-9.\-_]+$`)
	procNameR = regexp.MustCompile(`^[A-Za-z0-9 ._\-+]+$`)
	udidRe    = regexp.MustCompile(`^[A-Za-z0-9\-]+$`)
	launchPid = regexp.MustCompile(`:\s*(\d+)\s*$`)
)

func validateTarget(t Target) error {
	if t.Platform != device.PlatformIOS && t.Platform != device.PlatformSimulator {
		return ErrNotSupported
	}
	if !udidRe.MatchString(t.Serial) {
		return errors.New("debugger: invalid device identifier")
	}
	if t.BundleID != "" && !bundleRe.MatchString(t.BundleID) {
		return errors.New("debugger: invalid bundle identifier")
	}
	if t.ProcessName != "" && !procNameR.MatchString(t.ProcessName) {
		return errors.New("debugger: invalid process name")
	}
	if t.PID < 0 {
		return errors.New("debugger: invalid pid")
	}
	if t.PID == 0 && t.BundleID == "" && t.ProcessName == "" {
		return errors.New("debugger: bundleId, pid or processName is required")
	}
	if t.Launch && t.BundleID == "" {
		return errors.New("debugger: launch needs a bundleId")
	}
	for _, a := range t.Args {
		if strings.ContainsAny(a, "\r\n\x00") {
			return errors.New("debugger: invalid launch argument")
		}
	}
	for k, v := range t.Env {
		if k == "" || strings.ContainsAny(k, "=\r\n\x00") || strings.ContainsAny(v, "\r\n\x00") {
			return errors.New("debugger: invalid environment variable")
		}
	}
	return nil
}

func planFor(ctx context.Context, run Runner, t Target) (plan, error) {
	if err := validateTarget(t); err != nil {
		return plan{}, err
	}
	if t.Platform == device.PlatformSimulator {
		return planSimulator(ctx, run, t)
	}
	return planDevice(ctx, run, t)
}

// ---- simulator ----

func planSimulator(ctx context.Context, run Runner, t Target) (plan, error) {
	pid := t.PID
	var note string
	switch {
	case pid != 0:
	case t.Launch:
		args := []string{"simctl", "launch", "--wait-for-debugger", "--terminate-running-process", t.Serial, t.BundleID}
		args = append(args, t.Args...)
		var env []string
		for k, v := range t.Env {
			env = append(env, "SIMCTL_CHILD_"+k+"="+v)
		}
		out, err := run(ctx, env, "xcrun", args...)
		if err != nil {
			return plan{}, fmt.Errorf("simulator launch failed: %w", err)
		}
		m := launchPid.FindStringSubmatch(strings.TrimSpace(out))
		if m == nil {
			return plan{}, fmt.Errorf("simulator launch: could not read pid from %q", strings.TrimSpace(out))
		}
		pid, _ = strconv.Atoi(m[1])
		note = "app launched suspended (waiting for debugger) and attached; set breakpoints, then continue"
	case t.BundleID != "":
		out, err := run(ctx, nil, "xcrun", "simctl", "spawn", t.Serial, "launchctl", "list")
		if err != nil {
			return plan{}, fmt.Errorf("simulator process list failed: %w", err)
		}
		pid = SimulatorPID(out, t.BundleID)
		if pid == 0 {
			return plan{}, fmt.Errorf("%s is not running in the simulator (use launch to start it under the debugger)", t.BundleID)
		}
	}
	if pid != 0 {
		return plan{pid: pid, commands: []string{"process attach --pid " + strconv.Itoa(pid)}, note: note}, nil
	}
	return plan{commands: []string{"process attach --name " + quoteArg(t.ProcessName)}}, nil
}

// SimulatorPID finds the pid of a running app in `launchctl list` output
// (labels look like UIKitApplication:com.example.app[1234][rb-legacy]).
func SimulatorPID(out, bundle string) int {
	needle := "UIKitApplication:" + bundle + "["
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !strings.HasPrefix(f[2], needle) {
			continue
		}
		if n, err := strconv.Atoi(f[0]); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func quoteArg(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// ---- real device (CoreDevice) ----

func dcJSON(ctx context.Context, run Runner, v any, args ...string) error {
	dir, err := os.MkdirTemp("", "pf-devicectl-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "out.json")
	full := append([]string{"devicectl"}, args...)
	full = append(full, "--json-output", out, "--quiet")
	if _, err := run(ctx, nil, "xcrun", full...); err != nil {
		return err
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// CoreDeviceID maps a udid to the CoreDevice identifier lldb's `device`
// commands use.
func coreDeviceID(ctx context.Context, run Runner, udid string) (string, error) {
	var r struct {
		Result struct {
			Devices []struct {
				Identifier         string `json:"identifier"`
				HardwareProperties struct {
					UDID string `json:"udid"`
				} `json:"hardwareProperties"`
				Properties struct {
					UDID string `json:"udid"`
				} `json:"properties"`
			} `json:"devices"`
		} `json:"result"`
	}
	if err := dcJSON(ctx, run, &r, "list", "devices"); err != nil {
		return "", fmt.Errorf("devicectl list devices: %w", err)
	}
	for _, d := range r.Result.Devices {
		if strings.EqualFold(d.HardwareProperties.UDID, udid) || strings.EqualFold(d.Properties.UDID, udid) || strings.EqualFold(d.Identifier, udid) {
			return d.Identifier, nil
		}
	}
	return "", fmt.Errorf("CoreDevice does not know device %s (is it unlocked, trusted, paired and in Developer Mode?)", udid)
}

func planDevice(ctx context.Context, run Runner, t Target) (plan, error) {
	id, err := coreDeviceID(ctx, run, t.Serial)
	if err != nil {
		return plan{}, err
	}
	pid := t.PID
	note := ""
	switch {
	case pid != 0:
	case t.Launch:
		args := []string{"device", "process", "launch", "--device", id, "--start-stopped", "--terminate-existing"}
		if len(t.Env) > 0 {
			b, _ := json.Marshal(t.Env)
			args = append(args, "--environment-variables", string(b))
		}
		args = append(args, t.BundleID)
		args = append(args, t.Args...)
		var r struct {
			Result struct {
				Process struct {
					PID int `json:"processIdentifier"`
				} `json:"process"`
			} `json:"result"`
		}
		if err := dcJSON(ctx, run, &r, args...); err != nil {
			return plan{}, fmt.Errorf("device launch failed (needs a development-signed, debuggable app): %w", err)
		}
		pid = r.Result.Process.PID
		if pid == 0 {
			return plan{}, errors.New("device launch did not report a pid")
		}
		note = "app launched suspended and attached; set breakpoints, then continue"
	case t.BundleID != "":
		pid, err = devicePID(ctx, run, id, t.BundleID)
		if err != nil {
			return plan{}, err
		}
	}
	cmds := []string{"device select " + id}
	if pid != 0 {
		cmds = append(cmds, "device process attach --pid "+strconv.Itoa(pid))
	} else {
		cmds = append(cmds, "device process attach --name "+quoteArg(t.ProcessName))
	}
	return plan{pid: pid, commands: cmds, note: note}, nil
}

func devicePID(ctx context.Context, run Runner, coreID, bundle string) (int, error) {
	var apps struct {
		Result struct {
			Apps []struct {
				BundleIdentifier string `json:"bundleIdentifier"`
				URL              string `json:"url"`
			} `json:"apps"`
		} `json:"result"`
	}
	if err := dcJSON(ctx, run, &apps, "device", "info", "apps", "--device", coreID, "--bundle-identifier", bundle); err != nil {
		return 0, fmt.Errorf("device app lookup: %w", err)
	}
	var appURL string
	for _, a := range apps.Result.Apps {
		if a.BundleIdentifier == bundle {
			appURL = a.URL
		}
	}
	if appURL == "" {
		return 0, fmt.Errorf("%s is not installed on the device", bundle)
	}
	var procs struct {
		Result struct {
			Running []struct {
				Executable string `json:"executable"`
				PID        int    `json:"processIdentifier"`
			} `json:"runningProcesses"`
		} `json:"result"`
	}
	if err := dcJSON(ctx, run, &procs, "device", "info", "processes", "--device", coreID); err != nil {
		return 0, fmt.Errorf("device process list: %w", err)
	}
	dp := make([]DeviceProcess, 0, len(procs.Result.Running))
	for _, p := range procs.Result.Running {
		dp = append(dp, DeviceProcess{p.Executable, p.PID})
	}
	pid := MatchDevicePID(appURL, dp)
	if pid == 0 {
		return 0, fmt.Errorf("%s is not running on the device (use launch to start it under the debugger)", bundle)
	}
	return pid, nil
}

// DeviceProcess is a running process on a device.
type DeviceProcess struct {
	Executable string
	PID        int
}

// MatchDevicePID returns the pid of the process whose executable lives inside
// appURL (the app bundle URL), or 0.
func MatchDevicePID(appURL string, procs []DeviceProcess) int {
	prefix := strings.TrimSuffix(appURL, "/") + "/"
	for _, p := range procs {
		if strings.HasPrefix(p.Executable, prefix) {
			return p.PID
		}
	}
	return 0
}
