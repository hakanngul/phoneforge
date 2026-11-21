package iosdrv

import (
	"context"
	"errors"
	"testing"

	"phoneforge/internal/device"
)

func TestFormatID(t *testing.T) {
	if got := FormatID(" 00008130-001A/5C\n"); got != "ios-00008130-001A5C" {
		t.Fatalf("got %q", got)
	}
}

func TestWDAGatedUnsupported(t *testing.T) {
	d := &iosDevice{}
	ctx := context.Background()
	if err := d.Tap(ctx, 1, 2); !errors.Is(err, device.ErrUnsupported) {
		t.Fatalf("Tap: %v", err)
	}
	if _, err := d.UITree(ctx); !errors.Is(err, device.ErrUnsupported) {
		t.Fatalf("UITree: %v", err)
	}
	if err := d.StopApp(ctx, "x"); !errors.Is(err, device.ErrUnsupported) {
		t.Fatalf("StopApp: %v", err)
	}
}
