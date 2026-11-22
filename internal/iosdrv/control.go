package iosdrv

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"time"

	"github.com/danielpaulus/go-ios/ios/instruments"

	"pocketlab/internal/device"
)

func wdaErr() error {
	return fmt.Errorf("iOS UI automation requires the WebDriverAgent bridge (not configured): %w", device.ErrUnsupported)
}

// Screenshot captures the screen through the instruments screenshot service.
// On iOS 17+ this needs the device tunnel (usually root); otherwise it fails
// with a descriptive error.
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
		return device.Screenshot{}, fmt.Errorf("iOS screenshot requires the device tunnel (iOS 17+): %w", err)
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
