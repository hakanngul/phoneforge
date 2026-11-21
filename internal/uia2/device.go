package uia2

import (
	"context"
	"io"
	"os"
	"strings"

	"phoneforge/internal/device"
)

// Wrap decorates base so that, while b is running, UI-tree and gesture calls
// go through the UiAutomator2 bridge (falling back to base on error). All
// other methods and optional interfaces delegate to base.
//
// Unless PF_ANDROID_AUTOBRIDGE=0, the first UI-tree read starts the bridge
// lazily (the plain `uiautomator dump` path costs ~2 s per read; the bridge
// ~0.15 s). Taps and other gestures never trigger a start, and a bridge the
// user stopped explicitly is left alone.
func Wrap(base device.Device, b *Bridge) device.Device {
	return &wrapped{base: base, b: b, auto: AutoBridgeEnabled()}
}

// AutoBridgeEnabled reports whether lazy bridge start is allowed.
func AutoBridgeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PF_ANDROID_AUTOBRIDGE"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

type wrapped struct {
	base device.Device
	b    *Bridge
	auto bool
}

var (
	_ device.Device            = (*wrapped)(nil)
	_ device.GestureExtras     = (*wrapped)(nil)
	_ device.URLOpener         = (*wrapped)(nil)
	_ device.Clipboard         = (*wrapped)(nil)
	_ device.AppInstaller      = (*wrapped)(nil)
	_ device.PermissionManager = (*wrapped)(nil)
	_ device.LogStreamer       = (*wrapped)(nil)
	_ device.ScreenRecorder    = (*wrapped)(nil)
	_ device.KeyboardDismisser = (*wrapped)(nil)
	_ device.VideoStreamer     = (*wrapped)(nil)
)

// Unwrap returns the underlying device.
func (w *wrapped) Unwrap() device.Device { return w.base }

func (w *wrapped) active() bool { return w.b != nil && w.b.Status().Running }

// try runs the bridge op when active; it reports whether the bridge handled it.
func (w *wrapped) try(op func() error) (handled bool, err error) {
	if !w.active() {
		return false, nil
	}
	if err := op(); err != nil {
		w.b.setError(err)
		return false, nil
	}
	return true, nil
}

func (w *wrapped) Info() device.Info { return w.base.Info() }
func (w *wrapped) Screenshot(ctx context.Context) (device.Screenshot, error) {
	return w.base.Screenshot(ctx)
}
func (w *wrapped) TypeText(ctx context.Context, t string) error { return w.base.TypeText(ctx, t) }
func (w *wrapped) PressKey(ctx context.Context, k device.Key) error {
	return w.base.PressKey(ctx, k)
}
func (w *wrapped) ListApps(ctx context.Context) ([]device.AppInfo, error) {
	return w.base.ListApps(ctx)
}
func (w *wrapped) LaunchApp(ctx context.Context, id string) error { return w.base.LaunchApp(ctx, id) }
func (w *wrapped) StopApp(ctx context.Context, id string) error   { return w.base.StopApp(ctx, id) }

func (w *wrapped) UITree(ctx context.Context) (device.Element, error) {
	if w.auto && w.b != nil && !w.active() {
		_ = w.b.EnsureStarted(ctx) // failure is remembered; fall through to adb
	}
	var el device.Element
	if ok, _ := w.try(func() (err error) { el, err = w.b.Source(ctx); return }); ok {
		return el, nil
	}
	// The session may have gone stale (instrumentation restarted underneath
	// us); one fresh start is cheaper than falling back to the 2 s dump, which
	// also cannot run while the instrumentation holds UiAutomation.
	if w.auto && w.b != nil && w.active() && ctx.Err() == nil && w.b.restart(ctx) == nil {
		if ok, _ := w.try(func() (err error) { el, err = w.b.Source(ctx); return }); ok {
			return el, nil
		}
	}
	return w.base.UITree(ctx)
}

func (w *wrapped) Tap(ctx context.Context, x, y int) error {
	if ok, _ := w.try(func() error { return w.b.Tap(ctx, x, y) }); ok {
		return nil
	}
	return w.base.Tap(ctx, x, y)
}

func (w *wrapped) LongPress(ctx context.Context, x, y, ms int) error {
	if ok, _ := w.try(func() error { return w.b.LongPress(ctx, x, y, ms) }); ok {
		return nil
	}
	return w.base.LongPress(ctx, x, y, ms)
}

func (w *wrapped) Swipe(ctx context.Context, x1, y1, x2, y2, ms int) error {
	if ok, _ := w.try(func() error { return w.b.Swipe(ctx, x1, y1, x2, y2, ms) }); ok {
		return nil
	}
	return w.base.Swipe(ctx, x1, y1, x2, y2, ms)
}

func (w *wrapped) DoubleTap(ctx context.Context, x, y int) error {
	if ok, _ := w.try(func() error { return w.b.DoubleTap(ctx, x, y) }); ok {
		return nil
	}
	g, ok := w.base.(device.GestureExtras)
	if !ok {
		return device.ErrUnsupported
	}
	return g.DoubleTap(ctx, x, y)
}

func (w *wrapped) Drag(ctx context.Context, x1, y1, x2, y2, ms int) error {
	if ok, _ := w.try(func() error { return w.b.Drag(ctx, x1, y1, x2, y2, ms) }); ok {
		return nil
	}
	g, ok := w.base.(device.GestureExtras)
	if !ok {
		return device.ErrUnsupported
	}
	return g.Drag(ctx, x1, y1, x2, y2, ms)
}

func (w *wrapped) Pinch(ctx context.Context, cx, cy int, scale float64, ms int) error {
	if ok, _ := w.try(func() error { return w.b.Pinch(ctx, cx, cy, scale, ms) }); ok {
		return nil
	}
	g, ok := w.base.(device.GestureExtras)
	if !ok {
		return device.ErrUnsupported
	}
	return g.Pinch(ctx, cx, cy, scale, ms)
}

func (w *wrapped) OpenURL(ctx context.Context, u string) error {
	if o, ok := w.base.(device.URLOpener); ok {
		return o.OpenURL(ctx, u)
	}
	return device.ErrUnsupported
}

func (w *wrapped) SetClipboard(ctx context.Context, t string) error {
	if c, ok := w.base.(device.Clipboard); ok {
		return c.SetClipboard(ctx, t)
	}
	return device.ErrUnsupported
}

func (w *wrapped) GetClipboard(ctx context.Context) (string, error) {
	if c, ok := w.base.(device.Clipboard); ok {
		return c.GetClipboard(ctx)
	}
	return "", device.ErrUnsupported
}

func (w *wrapped) InstallApp(ctx context.Context, p string) error {
	if a, ok := w.base.(device.AppInstaller); ok {
		return a.InstallApp(ctx, p)
	}
	return device.ErrUnsupported
}

func (w *wrapped) UninstallApp(ctx context.Context, id string) error {
	if a, ok := w.base.(device.AppInstaller); ok {
		return a.UninstallApp(ctx, id)
	}
	return device.ErrUnsupported
}

func (w *wrapped) SetPermission(ctx context.Context, id, perm string, grant bool) error {
	if p, ok := w.base.(device.PermissionManager); ok {
		return p.SetPermission(ctx, id, perm, grant)
	}
	return device.ErrUnsupported
}

func (w *wrapped) StreamLogs(ctx context.Context, out io.Writer) error {
	if l, ok := w.base.(device.LogStreamer); ok {
		return l.StreamLogs(ctx, out)
	}
	return device.ErrUnsupported
}

func (w *wrapped) RecordScreen(ctx context.Context, seconds int) ([]byte, error) {
	if r, ok := w.base.(device.ScreenRecorder); ok {
		return r.RecordScreen(ctx, seconds)
	}
	return nil, device.ErrUnsupported
}

func (w *wrapped) DismissKeyboard(ctx context.Context) error {
	if k, ok := w.base.(device.KeyboardDismisser); ok {
		return k.DismissKeyboard(ctx)
	}
	return device.ErrUnsupported
}

func (w *wrapped) StreamH264(ctx context.Context, out io.Writer) error {
	if v, ok := w.base.(device.VideoStreamer); ok {
		return v.StreamH264(ctx, out)
	}
	return device.ErrUnsupported
}
