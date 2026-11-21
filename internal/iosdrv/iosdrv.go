// Package iosdrv is PhoneForge's iOS driver built on go-ios. Without
// WebDriverAgent it supports discovery, device info and app listing; UI
// automation returns device.ErrUnsupported and screenshots need the iOS 17+
// device tunnel.
package iosdrv

import (
	"context"
	"fmt"
	"strings"

	"github.com/danielpaulus/go-ios/ios"

	"phoneforge/internal/device"
	"phoneforge/internal/registry"
)

type discoverer struct{}

// NewDiscoverer returns a registry.Discoverer that enumerates USB-connected
// iOS devices through usbmuxd.
func NewDiscoverer() registry.Discoverer { return discoverer{} }

// Discover lists connected iOS devices. Lockdown value failures (locked or
// untrusted device) do not drop the device; it is reported as "unauthorized".
func (discoverer) Discover(ctx context.Context) ([]device.Device, error) {
	list, err := ios.ListDevices()
	if err != nil {
		return nil, fmt.Errorf("iOS discovery (is usbmuxd running?): %w", err)
	}
	var out []device.Device
	for _, e := range list.DeviceList {
		udid := e.Properties.SerialNumber
		if udid == "" {
			continue
		}
		d := &iosDevice{entry: e, info: device.Info{
			ID:       FormatID(udid),
			Serial:   udid,
			Name:     udid,
			Platform: device.PlatformIOS,
			State:    "connected",
		}}
		vals, verr := ios.GetValuesPlist(e)
		if verr != nil {
			d.info.State = "unauthorized"
		} else {
			d.info.Model = str(vals["ProductType"])
			d.info.OSVersion = str(vals["ProductVersion"])
			if n := str(vals["DeviceName"]); n != "" {
				d.info.Name = n
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func str(v interface{}) string {
	s, _ := v.(string)
	return s
}

// FormatID builds the PhoneForge device id for an iOS udid: "ios-" plus the
// udid with whitespace trimmed and characters outside [A-Za-z0-9-] removed.
func FormatID(udid string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(udid) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		}
	}
	return "ios-" + b.String()
}

// iosDevice implements device.Device on top of go-ios.
type iosDevice struct {
	entry ios.DeviceEntry
	info  device.Info
}

var _ device.Device = (*iosDevice)(nil)

// Info returns the device description captured at discovery time.
func (d *iosDevice) Info() device.Info { return d.info }
