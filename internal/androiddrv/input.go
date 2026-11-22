package androiddrv

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"pocketlab/internal/device"
)

const inputTimeout = 15 * time.Second

func itoa(vs ...int) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = strconv.Itoa(v)
	}
	return out
}

func (d *androidDevice) input(ctx context.Context, args ...string) error {
	ctx, cancel := withTimeout(ctx, inputTimeout)
	defer cancel()
	_, err := d.run(ctx, append([]string{"shell", "input"}, args...)...)
	return err
}

// Tap taps at the given pixel coordinates.
func (d *androidDevice) Tap(ctx context.Context, x, y int) error {
	return d.input(ctx, append([]string{"tap"}, itoa(x, y)...)...)
}

// LongPress presses at a point for ms milliseconds.
func (d *androidDevice) LongPress(ctx context.Context, x, y int, ms int) error {
	return d.Swipe(ctx, x, y, x, y, ms)
}

// Swipe drags from (x1,y1) to (x2,y2) over ms milliseconds.
func (d *androidDevice) Swipe(ctx context.Context, x1, y1, x2, y2, ms int) error {
	return d.input(ctx, append([]string{"swipe"}, itoa(x1, y1, x2, y2, ms)...)...)
}

// escapeText converts text into an argument for `input text`. Spaces become
// %s; shell-special characters are backslash-escaped because adb joins the
// arguments and runs them through the device shell. Non-ASCII is rejected
// since `input text` cannot type it.
func escapeText(text string) (string, error) {
	var b strings.Builder
	for _, r := range text {
		switch {
		case r > 126 || r < 32:
			return "", fmt.Errorf("androiddrv: cannot type character %q via adb input: %w", r, device.ErrUnsupported)
		case r == ' ':
			b.WriteString("%s")
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r == ',', r == ':', r == '@', r == '/', r == '+', r == '=':
			b.WriteRune(r)
		default:
			b.WriteByte('\\')
			b.WriteRune(r)
		}
	}
	return b.String(), nil
}

// TypeText types text into the focused field using `input text`.
func (d *androidDevice) TypeText(ctx context.Context, text string) error {
	if text == "" {
		return nil
	}
	esc, err := escapeText(text)
	if err != nil {
		return err
	}
	return d.input(ctx, "text", esc)
}

var keyCodes = map[device.Key]int{
	device.KeyHome:   3,
	device.KeyBack:   4,
	device.KeyEnter:  66,
	device.KeyDelete: 67,
}

// PressKey sends a keyevent; unknown keys return device.ErrUnsupported.
func (d *androidDevice) PressKey(ctx context.Context, k device.Key) error {
	code, ok := keyCodes[k]
	if !ok {
		return fmt.Errorf("androiddrv: key %q: %w", k, device.ErrUnsupported)
	}
	return d.input(ctx, "keyevent", strconv.Itoa(code))
}
