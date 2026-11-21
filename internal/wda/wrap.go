package wda

import (
	"context"
	"errors"
	"io"

	"phoneforge/internal/device"
)

// Wrap decorates inner so that, while active() reports true, screenshots,
// UI-tree, input, app launch/stop and extras go through WDA. If WDA fails (or
// is inactive) the call falls back to inner; when inner reports ErrUnsupported
// the WDA error is surfaced instead. ListApps, installs, logs and other
// capabilities WDA lacks delegate to inner.
func Wrap(inner device.Device, c *Client, active func() bool) device.Device {
	return &wrapped{base: inner, c: c, act: active}
}

// WrapEnsure is Wrap with a hook that runs before UI-tree and input
// operations (not screenshots): it may start WDA lazily and block until it is
// up. A non-nil error aborts the operation and is returned as is.
func WrapEnsure(inner device.Device, c *Client, active func() bool, ensure func(ctx context.Context) error) device.Device {
	return &wrapped{base: inner, c: c, act: active, ens: ensure}
}

type wrapped struct {
	base device.Device
	c    *Client
	act  func() bool
	ens  func(ctx context.Context) error
}

func (w *wrapped) prep(ctx context.Context) error {
	if w.ens == nil {
		return nil
	}
	return w.ens(ctx)
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

// Client returns the WDA client used by this wrapper.
func (w *wrapped) Client() *Client { return w.c }

func (w *wrapped) active() bool { return w.c != nil && w.act != nil && w.act() }

// fall resolves a fallback result: if the inner device does not support the
// operation, report the WDA failure (when there was one).
func fall(werr, ierr error) error {
	if werr != nil && errors.Is(ierr, device.ErrUnsupported) {
		return werr
	}
	return ierr
}

func (w *wrapped) Info() device.Info { return w.base.Info() }

func (w *wrapped) Screenshot(ctx context.Context) (device.Screenshot, error) {
	var werr error
	if w.active() {
		var s device.Screenshot
		if s, werr = w.c.Screenshot(ctx); werr == nil {
			return s, nil
		}
	}
	s, err := w.base.Screenshot(ctx)
	return s, fall(werr, err)
}

func (w *wrapped) UITree(ctx context.Context) (device.Element, error) {
	if err := w.prep(ctx); err != nil {
		return device.Element{}, err
	}
	var werr error
	if w.active() {
		var e device.Element
		if e, werr = w.c.Source(ctx); werr == nil {
			return e, nil
		}
	}
	e, err := w.base.UITree(ctx)
	return e, fall(werr, err)
}

// route runs op on WDA when active, else (or on failure) on fallback.
func (w *wrapped) route(op func() error, fallback func() error) error {
	var werr error
	if w.active() {
		if werr = op(); werr == nil {
			return nil
		}
	}
	return fall(werr, fallback())
}

func (w *wrapped) Tap(ctx context.Context, x, y int) error {
	if err := w.prep(ctx); err != nil {
		return err
	}
	return w.route(func() error { return w.c.Tap(ctx, x, y) }, func() error { return w.base.Tap(ctx, x, y) })
}

func (w *wrapped) LongPress(ctx context.Context, x, y, ms int) error {
	if err := w.prep(ctx); err != nil {
		return err
	}
	return w.route(func() error { return w.c.LongPress(ctx, x, y, ms) }, func() error { return w.base.LongPress(ctx, x, y, ms) })
}

func (w *wrapped) Swipe(ctx context.Context, x1, y1, x2, y2, ms int) error {
	if err := w.prep(ctx); err != nil {
		return err
	}
	return w.route(func() error { return w.c.Swipe(ctx, x1, y1, x2, y2, ms) }, func() error { return w.base.Swipe(ctx, x1, y1, x2, y2, ms) })
}

func (w *wrapped) TypeText(ctx context.Context, t string) error {
	if err := w.prep(ctx); err != nil {
		return err
	}
	return w.route(func() error { return w.c.TypeText(ctx, t) }, func() error { return w.base.TypeText(ctx, t) })
}

func (w *wrapped) PressKey(ctx context.Context, k device.Key) error {
	if err := w.prep(ctx); err != nil {
		return err
	}
	return w.route(func() error { return w.c.PressKey(ctx, k) }, func() error { return w.base.PressKey(ctx, k) })
}

func (w *wrapped) ListApps(ctx context.Context) ([]device.AppInfo, error) {
	return w.base.ListApps(ctx)
}

func (w *wrapped) LaunchApp(ctx context.Context, id string) error {
	return w.route(func() error { return w.c.LaunchApp(ctx, id) }, func() error { return w.base.LaunchApp(ctx, id) })
}

func (w *wrapped) StopApp(ctx context.Context, id string) error {
	return w.route(func() error { return w.c.StopApp(ctx, id) }, func() error { return w.base.StopApp(ctx, id) })
}

func (w *wrapped) gestures() (device.GestureExtras, bool) {
	g, ok := w.base.(device.GestureExtras)
	return g, ok
}

func (w *wrapped) DoubleTap(ctx context.Context, x, y int) error {
	if err := w.prep(ctx); err != nil {
		return err
	}
	return w.route(func() error { return w.c.DoubleTap(ctx, x, y) }, func() error {
		if g, ok := w.gestures(); ok {
			return g.DoubleTap(ctx, x, y)
		}
		return device.ErrUnsupported
	})
}

func (w *wrapped) Drag(ctx context.Context, x1, y1, x2, y2, ms int) error {
	if err := w.prep(ctx); err != nil {
		return err
	}
	return w.route(func() error { return w.c.Drag(ctx, x1, y1, x2, y2, ms) }, func() error {
		if g, ok := w.gestures(); ok {
			return g.Drag(ctx, x1, y1, x2, y2, ms)
		}
		return device.ErrUnsupported
	})
}

func (w *wrapped) Pinch(ctx context.Context, cx, cy int, scale float64, ms int) error {
	if err := w.prep(ctx); err != nil {
		return err
	}
	return w.route(func() error { return w.c.Pinch(ctx, cx, cy, scale, ms) }, func() error {
		if g, ok := w.gestures(); ok {
			return g.Pinch(ctx, cx, cy, scale, ms)
		}
		return device.ErrUnsupported
	})
}

func (w *wrapped) OpenURL(ctx context.Context, u string) error {
	return w.route(func() error { return w.c.OpenURL(ctx, u) }, func() error {
		if o, ok := w.base.(device.URLOpener); ok {
			return o.OpenURL(ctx, u)
		}
		return device.ErrUnsupported
	})
}

func (w *wrapped) SetClipboard(ctx context.Context, t string) error {
	return w.route(func() error { return w.c.SetClipboard(ctx, t) }, func() error {
		if c, ok := w.base.(device.Clipboard); ok {
			return c.SetClipboard(ctx, t)
		}
		return device.ErrUnsupported
	})
}

func (w *wrapped) GetClipboard(ctx context.Context) (string, error) {
	var werr error
	if w.active() {
		var s string
		if s, werr = w.c.GetClipboard(ctx); werr == nil {
			return s, nil
		}
	}
	if c, ok := w.base.(device.Clipboard); ok {
		s, err := c.GetClipboard(ctx)
		return s, fall(werr, err)
	}
	return "", fall(werr, device.ErrUnsupported)
}

func (w *wrapped) DismissKeyboard(ctx context.Context) error {
	if err := w.prep(ctx); err != nil {
		return err
	}
	return w.route(func() error { return w.c.DismissKeyboard(ctx) }, func() error {
		if k, ok := w.base.(device.KeyboardDismisser); ok {
			return k.DismissKeyboard(ctx)
		}
		return device.ErrUnsupported
	})
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

func (w *wrapped) StreamH264(ctx context.Context, out io.Writer) error {
	if v, ok := w.base.(device.VideoStreamer); ok {
		return v.StreamH264(ctx, out)
	}
	return device.ErrUnsupported
}
