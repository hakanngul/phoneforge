package device

import (
	"context"
	"io"
)

// GestureExtras is an optional capability for gestures beyond tap/swipe.
type GestureExtras interface {
	// DoubleTap taps twice in quick succession at (x, y).
	DoubleTap(ctx context.Context, x, y int) error
	// Drag presses at (x1,y1), moves to (x2,y2) over ms milliseconds and releases.
	Drag(ctx context.Context, x1, y1, x2, y2, ms int) error
	// Pinch performs a two-finger pinch centred on (cx, cy). scale < 1 pinches
	// in, scale > 1 spreads out, over ms milliseconds.
	Pinch(ctx context.Context, cx, cy int, scale float64, ms int) error
}

// URLOpener is an optional capability to open a URL or deep link.
type URLOpener interface {
	// OpenURL opens url (http, https or a custom app scheme) on the device.
	OpenURL(ctx context.Context, url string) error
}

// Clipboard is an optional capability to access the device clipboard.
type Clipboard interface {
	// SetClipboard replaces the clipboard contents with text.
	SetClipboard(ctx context.Context, text string) error
	// GetClipboard returns the current clipboard text.
	GetClipboard(ctx context.Context) (string, error)
}

// AppInstaller is an optional capability to install and remove apps.
type AppInstaller interface {
	// InstallApp installs (or reinstalls) the app package at localPath on the host.
	InstallApp(ctx context.Context, localPath string) error
	// UninstallApp removes the app identified by bundleID.
	UninstallApp(ctx context.Context, bundleID string) error
}

// PermissionManager is an optional capability to grant or revoke runtime permissions.
type PermissionManager interface {
	// SetPermission grants or revokes a permission for the given app.
	SetPermission(ctx context.Context, bundleID, permission string, grant bool) error
}

// LogStreamer is an optional capability to stream device logs.
type LogStreamer interface {
	// StreamLogs writes plain-text log lines to w until ctx is done or the
	// stream fails.
	StreamLogs(ctx context.Context, w io.Writer) error
}

// ScreenRecorder is an optional capability to record the screen.
type ScreenRecorder interface {
	// RecordScreen records the screen for the given number of seconds and
	// returns the MP4 bytes.
	RecordScreen(ctx context.Context, seconds int) ([]byte, error)
}

// KeyboardDismisser is an optional capability to hide the on-screen keyboard.
type KeyboardDismisser interface {
	// DismissKeyboard hides the soft keyboard if it is shown.
	DismissKeyboard(ctx context.Context) error
}
