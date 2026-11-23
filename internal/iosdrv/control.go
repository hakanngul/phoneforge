package iosdrv

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"strconv"
	"strings"
	"time"

	"github.com/danielpaulus/go-ios/ios/instruments"

	"pocketlab/internal/device"
)

func wdaErr() error {
	return fmt.Errorf("iOS UI automation requires the WebDriverAgent bridge (not configured): %w", device.ErrUnsupported)
}

// iosMajor returns the major version of an iOS version string ("26.5.2" -> 26), or 0.
func iosMajor(v string) int {
	n, _, _ := strings.Cut(strings.TrimSpace(v), ".")
	m, _ := strconv.Atoi(n)
	return m
}

// Screenshot captures the screen through the instruments screenshot service.
// On iOS 17+ this needs the device tunnel (usually root); without it the
// error is device.ErrWDARequired (the WDA bridge provides screenshots).
func (d *iosDevice) Screenshot(ctx context.Context) (device.Screenshot, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	data, err := runCtx(cctx, func() ([]byte, error) {
		svc, err := instruments.NewScreenshotService(d.entry)
		if err != nil {
			return nil, err
		}
		defer svc.Close()
		return svc.TakeScreenshot()
	})
	if err != nil {
		if iosMajor(d.info.OSVersion) >= 17 {
			return device.Screenshot{}, &device.WDARequiredError{Cause: err}
		}
		return device.Screenshot{}, fmt.Errorf("iOS screenshot failed: %w", err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return device.Screenshot{}, fmt.Errorf("iOS screenshot is not a valid PNG: %w", err)
	}
	return device.Screenshot{PNG: data, Width: cfg.Width, Height: cfg.Height}, nil
}

// UITree is unsupported without WebDriverAgent.
func (d *iosDevice) UITree(ctx context.Context) (device.Element, error) {
	return device.Element{}, wdaErr()
}

// Tap is unsupported without WebDriverAgent.
func (d *iosDevice) Tap(ctx context.Context, x, y int) error { return wdaErr() }

// LongPress is unsupported without WebDriverAgent.
func (d *iosDevice) LongPress(ctx context.Context, x, y int, ms int) error { return wdaErr() }

// Swipe is unsupported without WebDriverAgent.
func (d *iosDevice) Swipe(ctx context.Context, x1, y1, x2, y2, ms int) error { return wdaErr() }

// TypeText is unsupported without WebDriverAgent.
func (d *iosDevice) TypeText(ctx context.Context, text string) error { return wdaErr() }

// PressKey is unsupported without WebDriverAgent.
func (d *iosDevice) PressKey(ctx context.Context, k device.Key) error { return wdaErr() }
