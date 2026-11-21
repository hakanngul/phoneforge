package iosdrv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
	"github.com/danielpaulus/go-ios/ios/zipconduit"

	"phoneforge/internal/device"
)

var _ device.AppInstaller = (*iosDevice)(nil)

const (
	installTimeout   = 5 * time.Minute
	uninstallTimeout = 60 * time.Second
)

var bundleIDRe = regexp.MustCompile(`^[A-Za-z0-9.\-_]+$`)

// Seams replaced by tests.
var (
	sendInstall = func(e ios.DeviceEntry, path string) error {
		conn, err := zipconduit.New(e)
		if err != nil {
			return fmt.Errorf("cannot open install service: %w", err)
		}
		defer conn.Close()
		if err := conn.SendFile(path); err != nil {
			return fmt.Errorf("install failed: %w", err)
		}
		return nil
	}
	sendUninstall = func(e ios.DeviceEntry, bundleID string) error {
		conn, err := installationproxy.New(e)
		if err != nil {
			return fmt.Errorf("iOS installation proxy: %w", err)
		}
		defer conn.Close()
		return conn.Uninstall(bundleID)
	}
)

// validateInstallPath accepts a .ipa file or a .app directory.
func validateInstallPath(p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("iosdrv: app not found: %w", err)
	}
	lower := strings.ToLower(strings.TrimRight(p, "/"))
	switch {
	case st.IsDir() && strings.HasSuffix(lower, ".app"):
		return nil
	case !st.IsDir() && strings.HasSuffix(lower, ".ipa"):
		return nil
	}
	return errors.New("iosdrv: iOS install needs a .ipa file or a .app directory")
}

// InstallApp installs a signed .ipa or .app onto the device through zipconduit
// (the same mechanism used for the WebDriverAgent runner). The app must be
// signed for this device.
func (d *iosDevice) InstallApp(ctx context.Context, localPath string) error {
	if err := validateInstallPath(localPath); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	_, err := runCtx(cctx, func() (struct{}, error) { return struct{}{}, sendInstall(d.entry, localPath) })
	return err
}

// UninstallApp removes an app through the installation proxy.
func (d *iosDevice) UninstallApp(ctx context.Context, bundleID string) error {
	if !bundleIDRe.MatchString(bundleID) {
		return errors.New("iosdrv: invalid bundle identifier")
	}
	cctx, cancel := context.WithTimeout(ctx, uninstallTimeout)
	defer cancel()
	_, err := runCtx(cctx, func() (struct{}, error) { return struct{}{}, sendUninstall(d.entry, bundleID) })
	return err
}
