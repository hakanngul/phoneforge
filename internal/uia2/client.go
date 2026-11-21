package uia2

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"phoneforge/internal/device"
)

// ErrNotRunning is returned by client calls when the bridge has no session.
var ErrNotRunning = errors.New("uia2: bridge not running")

// do performs a JSON request against the running server and returns the
// "value" member of the W3C response.
func (b *Bridge) do(ctx context.Context, method, path string, in any) (json.RawMessage, error) {
	base, sid := b.endpoint()
	if sid == "" {
		return nil, ErrNotRunning
	}
	return b.doRaw(ctx, method, base+strings.ReplaceAll(path, "{sid}", sid), in)
}

func (b *Bridge) doRaw(ctx context.Context, method, fullURL string, in any) (json.RawMessage, error) {
	var rd io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, rd)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("uia2: %s: %w", method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("uia2: read response: %w", err)
	}
	var env struct {
		Value json.RawMessage `json:"value"`
	}
	_ = json.Unmarshal(data, &env)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(env.Value))
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("uia2: HTTP %d: %s", resp.StatusCode, msg)
	}
	if env.Value == nil {
		return data, nil // non-enveloped response (old protocol)
	}
	return env.Value, nil
}

// Source fetches the UI hierarchy and parses it into a device.Element tree.
func (b *Bridge) Source(ctx context.Context) (device.Element, error) {
	v, err := b.do(ctx, http.MethodGet, "/session/{sid}/source", nil)
	if err != nil {
		return device.Element{}, err
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return device.Element{}, fmt.Errorf("uia2: source is not a string: %w", err)
	}
	return parseSource([]byte(s))
}

// parseSource parses the UiAutomator2 hierarchy XML. Unlike plain
// `uiautomator dump`, element tag names are class names, so parsing is
// tag-agnostic and reads attributes only.
func parseSource(data []byte) (device.Element, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var stack []*device.Element
	var root *device.Element
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return device.Element{}, fmt.Errorf("uia2: parse source: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			el := elementFromAttrs(t)
			stack = append(stack, &el)
		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			el := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				root = el
			} else {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, *el)
			}
		}
	}
	if root == nil {
		return device.Element{}, errors.New("uia2: empty hierarchy")
	}
	if root.Role == "" || root.Role == "hierarchy" {
		root.Role = "hierarchy"
		root.Enabled = true
	}
	return *root, nil
}

func elementFromAttrs(t xml.StartElement) device.Element {
	attrs := map[string]string{}
	for _, a := range t.Attr {
		attrs[a.Name.Local] = a.Value
	}
	e := device.Element{
		Role:       attrs["class"],
		Text:       attrs["text"],
		Label:      attrs["content-desc"],
		ResourceID: attrs["resource-id"],
		Clickable:  attrs["clickable"] == "true",
		Enabled:    attrs["enabled"] == "true",
		Focused:    attrs["focused"] == "true",
	}
	if e.Role == "" {
		e.Role = t.Name.Local
	}
	e.Bounds, _ = parseBounds(attrs["bounds"])
	return e
}

func parseBounds(s string) (device.Rect, bool) {
	var x1, y1, x2, y2 int
	n, _ := fmt.Sscanf(s, "[%d,%d][%d,%d]", &x1, &y1, &x2, &y2)
	if n != 4 {
		return device.Rect{}, false
	}
	return device.Rect{X: x1, Y: y1, W: x2 - x1, H: y2 - y1}, true
}

// Screenshot returns the screen as PNG bytes.
func (b *Bridge) Screenshot(ctx context.Context) ([]byte, error) {
	v, err := b.do(ctx, http.MethodGet, "/session/{sid}/screenshot", nil)
	if err != nil {
		return nil, err
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return nil, fmt.Errorf("uia2: screenshot is not a string: %w", err)
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("uia2: decode screenshot: %w", err)
	}
	return png, nil
}

// ---- W3C pointer actions ----

type pAction map[string]any

type pSeq struct {
	Type       string            `json:"type"`
	ID         string            `json:"id"`
	Parameters map[string]string `json:"parameters"`
	Actions    []pAction         `json:"actions"`
}

func touchSeq(id string, acts ...pAction) pSeq {
	return pSeq{Type: "pointer", ID: id, Parameters: map[string]string{"pointerType": "touch"}, Actions: acts}
}

func mv(x, y, ms int) pAction {
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return pAction{"type": "pointerMove", "duration": ms, "origin": "viewport", "x": x, "y": y}
}
func down() pAction        { return pAction{"type": "pointerDown", "button": 0} }
func up() pAction          { return pAction{"type": "pointerUp", "button": 0} }
func pause(ms int) pAction { return pAction{"type": "pause", "duration": ms} }

func (b *Bridge) perform(ctx context.Context, seqs ...pSeq) error {
	_, err := b.do(ctx, http.MethodPost, "/session/{sid}/actions", map[string]any{"actions": seqs})
	return err
}

func (b *Bridge) Tap(ctx context.Context, x, y int) error {
	return b.perform(ctx, touchSeq("finger1", mv(x, y, 0), down(), pause(50), up()))
}

func (b *Bridge) DoubleTap(ctx context.Context, x, y int) error {
	return b.perform(ctx, touchSeq("finger1", mv(x, y, 0), down(), pause(40), up(), pause(60), down(), pause(40), up()))
}

func (b *Bridge) LongPress(ctx context.Context, x, y, ms int) error {
	if ms <= 0 {
		ms = 800
	}
	return b.perform(ctx, touchSeq("finger1", mv(x, y, 0), down(), pause(ms), up()))
}

func (b *Bridge) Swipe(ctx context.Context, x1, y1, x2, y2, ms int) error {
	if ms <= 0 {
		ms = 300
	}
	return b.perform(ctx, touchSeq("finger1", mv(x1, y1, 0), down(), mv(x2, y2, ms), up()))
}

// Drag presses, holds briefly so the target picks up the item, moves and releases.
func (b *Bridge) Drag(ctx context.Context, x1, y1, x2, y2, ms int) error {
	if ms <= 0 {
		ms = 500
	}
	return b.perform(ctx, touchSeq("finger1", mv(x1, y1, 0), down(), pause(400), mv(x2, y2, ms), pause(100), up()))
}

// Pinch builds two touch pointers moving along the horizontal axis through
// (cx, cy). scale < 1 moves the fingers together, scale > 1 apart.
func (b *Bridge) Pinch(ctx context.Context, cx, cy int, scale float64, ms int) error {
	return b.perform(ctx, pinchSeqs(cx, cy, scale, ms)...)
}

func pinchSeqs(cx, cy int, scale float64, ms int) []pSeq {
	if scale <= 0 {
		scale = 1
	}
	if ms <= 0 {
		ms = 400
	}
	const ref = 200.0
	start, end := ref, ref*scale
	if scale > 1 {
		start, end = ref/scale, ref
	}
	s, e := int(start), int(end)
	return []pSeq{
		touchSeq("finger1", mv(cx-s, cy, 0), down(), pause(50), mv(cx-e, cy, ms), up()),
		touchSeq("finger2", mv(cx+s, cy, 0), down(), pause(50), mv(cx+e, cy, ms), up()),
	}
}
