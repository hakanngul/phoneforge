// Package iosdrv is PhoneForge's iOS driver built on go-ios. Without
// WebDriverAgent it supports discovery, device info and app listing; UI
// automation returns device.ErrUnsupported and screenshots need the iOS 17+
// device tunnel.
package iosdrv

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	idx := map[string]int{} // udid -> index in out (USB and Wi-Fi entries share a udid)
	for _, e := range list.DeviceList {
		udid := e.Properties.SerialNumber
		if udid == "" {
			continue
		}
		if i, dup := idx[udid]; dup {
			// Same phone over USB and Wi-Fi: keep a single entry, preferring USB.
			if out[i].(*iosDevice).network && !isNetwork(e) {
				out = append(out[:i], out[i+1:]...)
				delete(idx, udid)
				for u, j := range idx {
					if j > i {
						idx[u] = j - 1
					}
				}
			} else {
				continue
			}
		}
		d := &iosDevice{entry: e, network: isNetwork(e), info: device.Info{
			ID:       FormatID(udid),
			Serial:   udid,
			Name:     udid,
			Platform: device.PlatformIOS,
			State:    "connected",
		}}
		vals, verr := getValues(ctx, e)
		if verr != nil {
			d.info.State = "unauthorized"
		} else {
			d.info.Model = str(vals["ProductType"])
			d.info.OSVersion = str(vals["ProductVersion"])
			if n := str(vals["DeviceName"]); n != "" {
				d.info.Name = n
			}
		}
		idx[udid] = len(out)
		out = append(out, d)
	}
	return out, nil
}

// isNetwork reports whether a usbmuxd entry is a Wi-Fi (not USB) connection.
func isNetwork(e ios.DeviceEntry) bool {
	return strings.EqualFold(e.Properties.ConnectionType, "Network")
}

// IsNetworkDevice reports whether d is an iOS device reached over Wi-Fi. Such
// devices flap in and out of usbmuxd, so the registry keeps them listed for a
// grace period; USB devices are removed as soon as they detach.
func IsNetworkDevice(d device.Device) bool {
	id, ok := d.(*iosDevice)
	return ok && id.network
}

// getValues reads lockdown values but gives up after valuesTimeout so a locked
// or half-paired device cannot block discovery of every other device.
func getValues(ctx context.Context, e ios.DeviceEntry) (map[string]interface{}, error) {
	type res struct {
		v   map[string]interface{}
		err error
	}
	ch := make(chan res, 1)
	go func() {
		v, err := ios.GetValuesPlist(e)
		ch <- res{v, err}
	}()
	t := time.NewTimer(valuesTimeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-t.C:
		return nil, fmt.Errorf("lockdown timed out")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

const valuesTimeout = 5 * time.Second

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
	entry   ios.DeviceEntry
	network bool // Wi-Fi paired rather than USB
	info    device.Info
}

var _ device.Device = (*iosDevice)(nil)

// Info returns the device description captured at discovery time.
func (d *iosDevice) Info() device.Info { return d.info }
