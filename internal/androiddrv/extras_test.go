package androiddrv

import (
	"context"
	"errors"
	"testing"

	"pocketlab/internal/device"
)

func TestValidateURL(t *testing.T) {
	ok := []string{"https://example.com/a?b=c", "http://x.y", "myapp://open/thing", "tel:+123", "a+b.c-d:x"}
	bad := []string{"", "example.com", "1http://x", "://x", "https://x\n", "/path", " https://x"}
	for _, u := range ok {
		if err := validateURL(u); err != nil {
			t.Errorf("validateURL(%q) = %v, want nil", u, err)
		}
	}
	for _, u := range bad {
		if err := validateURL(u); err == nil {
			t.Errorf("validateURL(%q) = nil, want error", u)
		}
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote(`a'b;c`); got != `'a'\''b;c'` {
		t.Errorf("shellQuote = %s", got)
	}
}

func TestPermNameRe(t *testing.T) {
	for _, p := range []string{"android.permission.CAMERA", "com.x.PERM_1"} {
		if !permNameRe.MatchString(p) {
			t.Errorf("%q should be valid", p)
		}
	}
	for _, p := range []string{"", "a b", "a;b", "a$(x)", "a-b", "a\n"} {
		if permNameRe.MatchString(p) {
			t.Errorf("%q should be invalid", p)
		}
	}
}

func TestClampSeconds(t *testing.T) {
	cases := map[int]int{-5: 1, 0: 1, 1: 1, 30: 30, 180: 180, 181: 180, 9999: 180}
	for in, want := range cases {
		if got := clampSeconds(in); got != want {
			t.Errorf("clampSeconds(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestInvalidInputsRejectedBeforeADB(t *testing.T) {
	d := &androidDevice{adbPath: "/nonexistent-adb", serial: "x"}
	ctx := context.Background()
	if err := d.OpenURL(ctx, "javascript\n"); err == nil {
		t.Error("expected url error")
	}
	if err := d.SetPermission(ctx, "com.x", "bad perm", true); err == nil {
		t.Error("expected permission error")
	}
	if err := d.SetPermission(ctx, "bad pkg;", "android.permission.CAMERA", true); err == nil {
		t.Error("expected package error")
	}
	if err := d.InstallApp(ctx, "/tmp/file.txt"); err == nil {
		t.Error("expected non-apk error")
	}
	if err := d.InstallApp(ctx, "/tmp/definitely-missing.apk"); err == nil {
		t.Error("expected missing file error")
	}
	if err := d.UninstallApp(ctx, "a b"); err == nil {
		t.Error("expected package error")
	}
}

func TestPinchUnsupported(t *testing.T) {
	d := &androidDevice{}
	err := d.Pinch(context.Background(), 1, 2, 0.5, 100)
	if !errors.Is(err, device.ErrUnsupported) {
		t.Errorf("Pinch err = %v, want ErrUnsupported", err)
	}
}

func TestClipboardNotImplementedDetection(t *testing.T) {
	if !clipboardNotImplemented("No shell command implementation.") {
		t.Error("should detect missing clipboard cmd")
	}
	if clipboardNotImplemented("hello") {
		t.Error("false positive")
	}
	if !errors.Is(clipboardUnsupported("x"), device.ErrUnsupported) {
		t.Error("clipboardUnsupported must wrap ErrUnsupported")
	}
}

// Compile-time interface assertions.
var (
	_ device.GestureExtras     = (*androidDevice)(nil)
	_ device.URLOpener         = (*androidDevice)(nil)
	_ device.Clipboard         = (*androidDevice)(nil)
	_ device.AppInstaller      = (*androidDevice)(nil)
	_ device.PermissionManager = (*androidDevice)(nil)
	_ device.LogStreamer       = (*androidDevice)(nil)
	_ device.ScreenRecorder    = (*androidDevice)(nil)
	_ device.KeyboardDismisser = (*androidDevice)(nil)
)
