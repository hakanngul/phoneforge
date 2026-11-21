package iosdrv

import (
	"context"
	"fmt"
	"time"

	"github.com/danielpaulus/go-ios/ios/installationproxy"
	"github.com/danielpaulus/go-ios/ios/instruments"

	"phoneforge/internal/device"
)

// runCtx runs fn and returns early with ctx.Err() if ctx ends first.
func runCtx[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type res struct {
		v   T
		err error
	}
	ch := make(chan res, 1)
	go func() { v, err := fn(); ch <- res{v, err} }()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// ListApps lists user-installed apps via the installation proxy.
func (d *iosDevice) ListApps(ctx context.Context) ([]device.AppInfo, error) {
	return runCtx(ctx, func() ([]device.AppInfo, error) {
		conn, err := installationproxy.New(d.entry)
		if err != nil {
			return nil, fmt.Errorf("iOS installation proxy: %w", err)
		}
		defer conn.Close()
		apps, err := conn.BrowseUserApps()
		if err != nil {
			return nil, fmt.Errorf("iOS list apps: %w", err)
		}
		out := make([]device.AppInfo, 0, len(apps))
		for _, a := range apps {
			build, _ := a["CFBundleVersion"].(string)
			name := a.CFBundleName()
			if dn, _ := a["CFBundleDisplayName"].(string); dn != "" {
				name = dn
			}
			out = append(out, device.AppInfo{BundleID: a.CFBundleIdentifier(), Name: name,
				Version: a.CFBundleShortVersionString(), Build: build})
		}
		return out, nil
	})
}

// LaunchApp launches an app through the instruments process-control service.
// On iOS 17+ this needs the developer image and device tunnel; failures are
// wrapped with device.ErrUnsupported.
func (d *iosDevice) LaunchApp(ctx context.Context, bundleID string) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := runCtx(cctx, func() (uint64, error) {
		pc, err := instruments.NewProcessControl(d.entry)
		if err != nil {
			return 0, err
		}
		defer pc.Close()
		return pc.LaunchApp(bundleID, map[string]any{})
	})
	if err != nil {
		return fmt.Errorf("iOS app launch requires the dev image/tunnel (%v): %w", err, device.ErrUnsupported)
	}
	return nil
}

// StopApp is unsupported: go-ios kills by pid and has no bundle-id lookup
// that works without the dev image/tunnel.
func (d *iosDevice) StopApp(ctx context.Context, bundleID string) error {
	return fmt.Errorf("iOS app stop requires the dev image/tunnel: %w", device.ErrUnsupported)
}
