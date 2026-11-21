package wda

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"phoneforge/internal/device"
)

func pmove(x, y, dur int) map[string]any {
	return map[string]any{"type": "pointerMove", "duration": dur, "x": x, "y": y, "origin": "viewport"}
}
func pdown() map[string]any { return map[string]any{"type": "pointerDown", "button": 0} }
func pup() map[string]any   { return map[string]any{"type": "pointerUp", "button": 0} }
func ppause(ms int) map[string]any {
	return map[string]any{"type": "pause", "duration": ms}
}

func finger(id string, acts ...map[string]any) map[string]any {
	return map[string]any{
		"type":       "pointer",
		"id":         id,
		"parameters": map[string]any{"pointerType": "touch"},
		"actions":    acts,
	}
}

func (c *Client) perform(ctx context.Context, fingers ...map[string]any) error {
	_, err := c.do(ctx, http.MethodPost, "/session/{sid}/actions", map[string]any{"actions": fingers})
	return err
}

func tapActs(x, y int) []map[string]any {
	return []map[string]any{pmove(x, y, 0), pdown(), ppause(50), pup()}
}

// Tap taps at (x, y) points, falling back to /wda/tap if actions fail.
func (c *Client) Tap(ctx context.Context, x, y int) error {
	err := c.perform(ctx, finger("finger1", tapActs(x, y)...))
	if err == nil {
		return nil
	}
	if _, ferr := c.do(ctx, http.MethodPost, "/session/{sid}/wda/tap", map[string]any{"x": x, "y": y}); ferr == nil {
		return nil
	}
	return err
}

// LongPress presses at (x, y) for ms milliseconds.
func (c *Client) LongPress(ctx context.Context, x, y, ms int) error {
	if ms <= 0 {
		ms = 1000
	}
	err := c.perform(ctx, finger("finger1", pmove(x, y, 0), pdown(), ppause(ms), pup()))
	if err == nil {
		return nil
	}
	body := map[string]any{"x": x, "y": y, "duration": float64(ms) / 1000}
	if _, ferr := c.do(ctx, http.MethodPost, "/session/{sid}/wda/touchAndHold", body); ferr == nil {
		return nil
	}
	return err
}

// Swipe drags from (x1,y1) to (x2,y2) over ms milliseconds.
func (c *Client) Swipe(ctx context.Context, x1, y1, x2, y2, ms int) error {
	if ms <= 0 {
		ms = 300
	}
	err := c.perform(ctx, finger("finger1", pmove(x1, y1, 0), pdown(), pmove(x2, y2, ms), pup()))
	if err == nil {
		return nil
	}
	body := map[string]any{"fromX": x1, "fromY": y1, "toX": x2, "toY": y2, "duration": float64(ms) / 1000}
	if _, ferr := c.do(ctx, http.MethodPost, "/session/{sid}/wda/dragfromtoforduration", body); ferr == nil {
		return nil
	}
	return err
}

// Drag is a press-hold-move: it presses, waits briefly so iOS starts a drag
// (reordering, icon move), then moves over ms and releases.
func (c *Client) Drag(ctx context.Context, x1, y1, x2, y2, ms int) error {
	if ms <= 0 {
		ms = 500
	}
	return c.perform(ctx, finger("finger1", pmove(x1, y1, 0), pdown(), ppause(500), pmove(x2, y2, ms), ppause(100), pup()))
}

// DoubleTap taps twice at (x, y).
func (c *Client) DoubleTap(ctx context.Context, x, y int) error {
	acts := []map[string]any{pmove(x, y, 0), pdown(), ppause(40), pup(), ppause(80), pdown(), ppause(40), pup()}
	err := c.perform(ctx, finger("finger1", acts...))
	if err == nil {
		return nil
	}
	if _, ferr := c.do(ctx, http.MethodPost, "/session/{sid}/wda/doubleTap", map[string]any{"x": x, "y": y}); ferr == nil {
		return nil
	}
	return err
}

// Pinch performs a two-finger vertical pinch centred on (cx, cy). scale < 1
// pinches in, scale > 1 spreads out.
func (c *Client) Pinch(ctx context.Context, cx, cy int, scale float64, ms int) error {
	if scale <= 0 {
		return fmt.Errorf("wda: invalid pinch scale %v", scale)
	}
	if ms <= 0 {
		ms = 400
	}
	r0 := 100.0
	if scale > 1 {
		r0 = 40
	}
	r1 := r0 * scale
	clamp := func(v float64) int {
		if v < 0 {
			return 0
		}
		return int(v + 0.5)
	}
	f1 := finger("finger1", pmove(cx, clamp(float64(cy)-r0), 0), pdown(), pmove(cx, clamp(float64(cy)-r1), ms), pup())
	f2 := finger("finger2", pmove(cx, clamp(float64(cy)+r0), 0), pdown(), pmove(cx, clamp(float64(cy)+r1), ms), pup())
	return c.perform(ctx, f1, f2)
}

// TypeText sends text to the focused element via /wda/keys.
func (c *Client) TypeText(ctx context.Context, text string) error {
	return c.Keys(ctx, text)
}

// Keys posts each rune of s as a separate key to /wda/keys.
func (c *Client) Keys(ctx context.Context, s string) error {
	chars := make([]string, 0, len(s))
	for _, r := range s {
		chars = append(chars, string(r))
	}
	if len(chars) == 0 {
		return nil
	}
	_, err := c.do(ctx, http.MethodPost, "/session/{sid}/wda/keys", map[string]any{"value": chars})
	return err
}

// Home goes to the home screen.
//
// The session pressButton call is preferred: on recent iOS, /wda/homescreen
// leaves Spotlight (and some other system UI) open, while pressing the Home
// button leaves it. /wda/homescreen is the fallback.
func (c *Client) Home(ctx context.Context) error {
	if err := c.PressButton(ctx, "home"); err == nil {
		return nil
	}
	_, err := c.doGlobal(ctx, http.MethodPost, "/wda/homescreen", map[string]any{})
	return err
}

// PressButton presses a hardware button by WDA name (volumeUp, volumeDown, home).
func (c *Client) PressButton(ctx context.Context, name string) error {
	_, err := c.do(ctx, http.MethodPost, "/session/{sid}/wda/pressButton", map[string]any{"name": name})
	return err
}

// Back performs the iOS back gesture: a swipe from the left screen edge
// towards the right. iOS has no back key, so this is the closest equivalent
// (it pops a navigation controller and dismisses many sheets).
func (c *Client) Back(ctx context.Context) error {
	w, h, err := c.WindowSize(ctx)
	if err != nil {
		return err
	}
	return c.Swipe(ctx, 1, h/2, w*3/5, h/2, 250)
}

// normKeyName lowercases a key name and drops separators so "volume_up",
// "VolumeUp" and "volume-up" all match.
func normKeyName(k device.Key) string {
	var b strings.Builder
	for _, r := range strings.ToLower(string(k)) {
		switch r {
		case '_', '-', ' ':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// PressKey maps device keys onto WDA operations.
func (c *Client) PressKey(ctx context.Context, k device.Key) error {
	switch normKeyName(k) {
	case "home", "homescreen":
		return c.Home(ctx)
	case "enter", "return", "newline":
		return c.Keys(ctx, "\n")
	case "delete", "backspace", "del":
		return c.Keys(ctx, "\b")
	case "back":
		return c.Back(ctx)
	case "volumeup", "volup":
		return c.PressButton(ctx, "volumeUp")
	case "volumedown", "voldown":
		return c.PressButton(ctx, "volumeDown")
	}
	return device.ErrUnsupported
}

// AppState describes the foreground app.
type AppState struct {
	BundleID string `json:"bundleId"`
	Name     string `json:"name"`
	PID      int    `json:"pid"`
}

// LaunchApp launches (or activates) an app by bundle id.
func (c *Client) LaunchApp(ctx context.Context, bundleID string) error {
	_, err := c.do(ctx, http.MethodPost, "/session/{sid}/wda/apps/launch", map[string]any{"bundleId": bundleID})
	return err
}

// StopApp terminates an app by bundle id.
func (c *Client) StopApp(ctx context.Context, bundleID string) error {
	_, err := c.do(ctx, http.MethodPost, "/session/{sid}/wda/apps/terminate", map[string]any{"bundleId": bundleID})
	return err
}

// ActiveApp returns the foreground application.
func (c *Client) ActiveApp(ctx context.Context) (AppState, error) {
	v, err := c.doGlobal(ctx, http.MethodGet, "/wda/activeAppInfo", nil)
	if err != nil {
		return AppState{}, err
	}
	var a AppState
	if err := json.Unmarshal(v, &a); err != nil {
		return AppState{}, fmt.Errorf("wda: decode activeAppInfo: %w", err)
	}
	return a, nil
}

// OpenURL opens a URL or deep link.
func (c *Client) OpenURL(ctx context.Context, url string) error {
	_, err := c.do(ctx, http.MethodPost, "/session/{sid}/url", map[string]any{"url": url})
	return err
}

// SetClipboard sets the pasteboard to text.
func (c *Client) SetClipboard(ctx context.Context, text string) error {
	body := map[string]any{
		"content":     base64.StdEncoding.EncodeToString([]byte(text)),
		"contentType": "plaintext",
	}
	_, err := c.doGlobal(ctx, http.MethodPost, "/wda/setPasteboard", body)
	return err
}

// GetClipboard reads the pasteboard as text.
func (c *Client) GetClipboard(ctx context.Context) (string, error) {
	v, err := c.doGlobal(ctx, http.MethodPost, "/wda/getPasteboard", map[string]any{"contentType": "plaintext"})
	if err != nil {
		return "", err
	}
	var b64 string
	if err := json.Unmarshal(v, &b64); err != nil {
		return "", fmt.Errorf("wda: pasteboard is not a string: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("wda: decode pasteboard: %w", err)
	}
	return string(raw), nil
}

// DismissKeyboard hides the on-screen keyboard.
func (c *Client) DismissKeyboard(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodPost, "/session/{sid}/wda/keyboard/dismiss", map[string]any{})
	return err
}

// Lock locks the device.
func (c *Client) Lock(ctx context.Context) error {
	_, err := c.doGlobal(ctx, http.MethodPost, "/wda/lock", map[string]any{})
	return err
}

// Unlock unlocks the device.
func (c *Client) Unlock(ctx context.Context) error {
	_, err := c.doGlobal(ctx, http.MethodPost, "/wda/unlock", map[string]any{})
	return err
}

// Orientation returns PORTRAIT, LANDSCAPE, etc.
func (c *Client) Orientation(ctx context.Context) (string, error) {
	v, err := c.do(ctx, http.MethodGet, "/session/{sid}/orientation", nil)
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", err
	}
	return s, nil
}

// SetOrientation sets the orientation (PORTRAIT, LANDSCAPE).
func (c *Client) SetOrientation(ctx context.Context, o string) error {
	_, err := c.do(ctx, http.MethodPost, "/session/{sid}/orientation", map[string]any{"orientation": o})
	if err == nil {
		c.mu.Lock()
		c.w, c.h = 0, 0 // window size changes with rotation
		c.mu.Unlock()
	}
	return err
}
