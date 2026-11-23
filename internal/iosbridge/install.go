package iosbridge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/zipconduit"
	"howett.net/plist"

	"pocketlab/internal/brand"
)

// Reasons a built runner app is not usable (Env.RunnerReason / SimReason).
const (
	ReasonStaleBundleID = "stale_bundle_id" // CFBundleIdentifier differs from the expected runner id
	ReasonBadPlist      = "unreadable_info_plist"
)

// ExpectedRunnerBundleID is the xctrunner bundle id a build with the given
// bundle prefix produces ("" prefix = the default com.pocketlab).
func ExpectedRunnerBundleID(prefix string) string {
	if prefix == "" {
		prefix = devBundleID
	}
	return prefix + ".WebDriverAgentRunner.xctrunner"
}

// envBundlePrefix is the configured bundle prefix (PL_BUNDLE_PREFIX or default).
func envBundlePrefix() string {
	if v := brand.Getenv("PL_BUNDLE_PREFIX"); v != "" {
		return v
	}
	return devBundleID
}

// AppBundleID reads CFBundleIdentifier from the .app's Info.plist.
func AppBundleID(appPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(appPath, "Info.plist"))
	if err != nil {
		return "", err
	}
	var info struct {
		ID string `plist:"CFBundleIdentifier"`
	}
	if _, err := plist.Unmarshal(data, &info); err != nil {
		return "", err
	}
	return info.ID, nil
}

// RunnerAppReason returns "" when the runner .app carries the expected bundle
// id, ReasonStaleBundleID when it was built with another one (for example
// before a rename) and ReasonBadPlist when its Info.plist cannot be read.
func RunnerAppReason(appPath, expectedID string) string {
	id, err := AppBundleID(appPath)
	if err != nil || id == "" {
		return ReasonBadPlist
	}
	if id != expectedID {
		return ReasonStaleBundleID
	}
	return ""
}

func staleRunnerIssue(have, want string) *Issue {
	return &Issue{
		Code:    CodeRunnerStale,
		Message: "The built WebDriverAgent runner uses an old bundle id (" + have + ", expected " + want + ").",
		Hint:    "Rebuild WDA",
	}
}

// ErrRunnerNotBuilt means the signed WebDriverAgent runner .app does not exist
// on this machine yet.
var ErrRunnerNotBuilt = errors.New("iosbridge: WebDriverAgent runner app has not been built yet; use \"Build WDA\" in Pocketlab (or `pocketlab ios build-wda`)")

// DefaultRunnerAppPath is where the WDA build leaves the signed runner
// ($PL_WDA_HOME or ~/.pocketlab/wda, then build/Build/Products/...).
func DefaultRunnerAppPath() string {
	return filepath.Join(WDAHome(), "build", "Build", "Products", "Debug-iphoneos", "WebDriverAgentRunner-Runner.app")
}

// InstallApp installs a signed .app (or .ipa) onto the device with the given
// udid using go-ios zipconduit. A missing path yields ErrRunnerNotBuilt-style
// guidance via the wrapped os error; callers that install the WDA runner
// should use InstallRunner.
func InstallApp(udid, appPath string) error {
	if _, err := os.Stat(appPath); err != nil {
		return fmt.Errorf("app not found: %w", err)
	}
	dev, err := ios.GetDevice(udid)
	if err != nil {
		return fmt.Errorf("device not found (is it connected, unlocked and trusted?): %w", err)
	}
	conn, err := zipconduit.New(dev)
	if err != nil {
		return fmt.Errorf("cannot open install service: %w", err)
	}
	defer conn.Close()
	if err := conn.SendFile(appPath); err != nil {
		return fmt.Errorf("install failed: %w", err)
	}
	return nil
}

// InstallRunner installs the built WDA runner (appPath, or the default build
// output when empty) onto the device.
func InstallRunner(udid, appPath string) error {
	if appPath == "" {
		appPath = DefaultRunnerAppPath()
	}
	if _, err := os.Stat(appPath); err != nil {
		return ErrRunnerNotBuilt
	}
	want := ExpectedRunnerBundleID(envBundlePrefix())
	if reason := RunnerAppReason(appPath, want); reason != "" {
		have, _ := AppBundleID(appPath)
		if have == "" {
			have = "unreadable Info.plist"
		}
		return staleRunnerIssue(have, want)
	}
	return InstallApp(udid, appPath)
}
