// Package device defines Pocketlab's stable device-automation contract.
// All drivers (Android now; iOS later) implement Device; the DSL engine,
// HTTP API and MCP server depend only on these types.
package device

import (
	"context"
	"strings"
)

// Platform identifies the device family.
type Platform string

const (
	PlatformAndroid   Platform = "android"
	PlatformIOS       Platform = "ios"
	PlatformSimulator Platform = "simulator"
)

// Info describes a connected device.
type Info struct {
	ID        string   `json:"id"`     // stable opaque id used in API paths
	Serial    string   `json:"serial"` // adb serial / udid (may be redacted in logs)
	Name      string   `json:"name"`   // human label
	Platform  Platform `json:"platform"`
	Model     string   `json:"model"`
	OSVersion string   `json:"osVersion"`
	State     string   `json:"state"` // "connected" | "offline" | "unauthorized"
	// Simulator marks an iOS Simulator (Platform stays "ios").
	Simulator bool `json:"simulator,omitempty"`
}

// Rect is an on-screen rectangle in device pixels.
type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Center returns the midpoint of the rectangle.
func (r Rect) Center() (int, int) { return r.X + r.W/2, r.Y + r.H/2 }

// Element is one node of the UI hierarchy, normalized across platforms.
type Element struct {
	Role       string    `json:"role"` // class/type
	Text       string    `json:"text,omitempty"`
	Label      string    `json:"label,omitempty"`      // content-desc / accessibility label
	ResourceID string    `json:"resourceId,omitempty"` // android resource-id / ios name
	Bounds     Rect      `json:"bounds"`
	Clickable  bool      `json:"clickable"`
	Enabled    bool      `json:"enabled"`
	Focused    bool      `json:"focused"`
	Children   []Element `json:"children,omitempty"`
}

// Screenshot is PNG bytes plus the logical size they were captured at.
type Screenshot struct {
	PNG    []byte `json:"-"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Key is a hardware/navigation key.
type Key string

const (
	KeyHome   Key = "home"
	KeyBack   Key = "back"
	KeyEnter  Key = "enter"
	KeyDelete Key = "delete"
	// KeyAppSwitch opens the recent-apps / multitasking switcher.
	KeyAppSwitch Key = "app_switch"
)

// IsAppSwitch reports whether k names the app switcher: app_switch plus the
// aliases recents, recent_apps, app_switcher and appswitch (case-insensitive,
// "_", "-" and spaces ignored).
func IsAppSwitch(k Key) bool {
	var b strings.Builder
	for _, r := range strings.ToLower(string(k)) {
		switch r {
		case '_', '-', ' ':
			continue
		}
		b.WriteRune(r)
	}
	switch b.String() {
	case "appswitch", "appswitcher", "recents", "recentapps":
		return true
	}
	return false
}

// AppInfo is an installed application.
type AppInfo struct {
	BundleID string `json:"bundleId"`
	Name     string `json:"name,omitempty"`
	Version  string `json:"version,omitempty"` // CFBundleShortVersionString / versionName
	Build    string `json:"build,omitempty"`   // CFBundleVersion / versionCode
}

// Device is the full automation surface a driver must provide.
// All methods take a context for cancellation/timeouts.
type Device interface {
	Info() Info

	// Capture
	Screenshot(ctx context.Context) (Screenshot, error)
	UITree(ctx context.Context) (Element, error)

	// Input
	Tap(ctx context.Context, x, y int) error
	LongPress(ctx context.Context, x, y int, ms int) error
	Swipe(ctx context.Context, x1, y1, x2, y2, ms int) error
	TypeText(ctx context.Context, text string) error
	PressKey(ctx context.Context, k Key) error

	// Apps
	ListApps(ctx context.Context) ([]AppInfo, error)
	LaunchApp(ctx context.Context, bundleID string) error
	StopApp(ctx context.Context, bundleID string) error
}
