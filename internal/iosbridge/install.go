package iosbridge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/zipconduit"
)

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
	return InstallApp(udid, appPath)
}
