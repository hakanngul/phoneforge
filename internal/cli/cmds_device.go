package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"pocketlab/internal/device"
	"pocketlab/internal/uitree"
)

func cmdDevices(c *call) error {
	pos, err := c.parse(newFlagSet(c.name), c.args)
	if err != nil {
		return err
	}
	if err := noArgs(c, pos); err != nil {
		return err
	}
	ctx, cancel := c.ctxFor(30 * time.Second)
	defer cancel()
	var raw json.RawMessage
	if err := c.client().do(ctx, "GET", "/devices", nil, &raw); err != nil {
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	var devs []device.Info
	if err := json.Unmarshal(raw, &devs); err != nil {
		return err
	}
	formatDevices(c.out, devs)
	return nil
}

func cmdDevice(c *call) error {
	pos, err := c.parse(newFlagSet(c.name), c.args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "info" {
		return usagef("usage: pocketlab device info")
	}
	ctx, cancel := c.ctxFor(30 * time.Second)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.client().do(ctx, "GET", devPath(id, ""), nil, &raw); err != nil {
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	var d device.Info
	if err := json.Unmarshal(raw, &d); err != nil {
		return err
	}
	formatDeviceInfo(c.out, d)
	return nil
}

func cmdScreenshot(c *call) error {
	fs := newFlagSet(c.name)
	out := fs.String("o", "", "")
	fs.StringVar(out, "output", "", "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if err := noArgs(c, pos); err != nil {
		return err
	}
	ctx, cancel := c.ctxFor(60 * time.Second)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	png, hdr, err := c.client().raw(ctx, "GET", devPath(id, "/screenshot"), nil)
	if err != nil {
		return err
	}
	if *out == "-" {
		if f, ok := c.out.(*os.File); ok {
			if st, serr := f.Stat(); serr == nil && st.Mode()&os.ModeCharDevice != 0 {
				return usagef("refusing to write PNG data to a terminal; redirect stdout or use -o file.png")
			}
		}
		_, err := c.out.Write(png)
		return err
	}
	path := *out
	if path == "" {
		path = "screenshot-" + time.Now().Format("20060102-150405") + ".png"
	}
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return err
	}
	w, _ := strconv.Atoi(hdr.Get("X-Screen-Width"))
	h, _ := strconv.Atoi(hdr.Get("X-Screen-Height"))
	if c.g.json {
		return c.printJSON(map[string]any{"path": path, "bytes": len(png), "width": w, "height": h})
	}
	if w > 0 {
		fmt.Fprintf(c.out, "saved %s (%d bytes, screen %dx%d)\n", path, len(png), w, h)
	} else {
		fmt.Fprintf(c.out, "saved %s (%d bytes)\n", path, len(png))
	}
	return nil
}

func fetchTree(c *call, ctx context.Context, id string) (device.Element, json.RawMessage, error) {
	var raw json.RawMessage
	if err := c.client().do(ctx, "GET", devPath(id, "/ui-tree"), nil, &raw); err != nil {
		return device.Element{}, nil, err
	}
	var el device.Element
	if err := json.Unmarshal(raw, &el); err != nil {
		return device.Element{}, nil, fmt.Errorf("bad UI tree from server: %w", err)
	}
	return el, raw, nil
}

func cmdTree(c *call) error {
	fs := newFlagSet(c.name)
	filter := fs.String("filter", "", "")
	maxNodes := fs.Int("max-nodes", 0, "")
	offscreen := fs.Bool("offscreen", false, "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if err := noArgs(c, pos); err != nil {
		return err
	}
	ctx, cancel := c.ctxFor(60 * time.Second)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	el, raw, err := fetchTree(c, ctx, id)
	if err != nil {
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	fmt.Fprint(c.out, uitree.Format(el, uitree.Options{
		DeviceID: id, Filter: *filter, MaxNodes: *maxNodes, IncludeOffscreen: *offscreen,
	}))
	return nil
}

func atoiArgs(c *call, names []string, pos []string) ([]int, error) {
	out := make([]int, len(names))
	for i, n := range names {
		v, err := strconv.Atoi(strings.TrimSpace(pos[i]))
		if err != nil {
			return nil, usagef("%s: %s must be an integer, got %q", c.name, n, pos[i])
		}
		out[i] = v
	}
	return out, nil
}

// actionResult prints {"ok":true} (json) or a one-line confirmation.
func (c *call) actionResult(msg string) error {
	if c.g.json {
		return c.printJSON(map[string]any{"ok": true})
	}
	fmt.Fprintln(c.out, msg)
	return nil
}

func (c *call) post(path string, body any, def time.Duration, msg string) error {
	ctx, cancel := c.ctxFor(def)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	if err := c.client().do(ctx, "POST", devPath(id, path), body, nil); err != nil {
		return err
	}
	return c.actionResult(msg)
}

func cmdTap(c *call) error {
	fs := newFlagSet(c.name)
	text := fs.String("text", "", "")
	rid := fs.String("id", "", "")
	index := fs.Int("index", 0, "")
	ocr := fs.Bool("ocr", false, "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if *text == "" && *rid == "" {
		if len(pos) != 2 {
			return usagef("usage: pocketlab tap X Y | tap --text TEXT [--ocr] | tap --id RESOURCE_ID")
		}
		xy, err := atoiArgs(c, []string{"X", "Y"}, pos)
		if err != nil {
			return err
		}
		return c.post("/tap", map[string]int{"x": xy[0], "y": xy[1]}, 30*time.Second, fmt.Sprintf("tapped (%d, %d)", xy[0], xy[1]))
	}
	if err := noArgs(c, pos); err != nil {
		return err
	}
	if *text != "" && *rid != "" {
		return usagef("tap: use either --text or --id, not both")
	}
	if *index < 0 {
		return usagef("tap: --index must be >= 0")
	}
	ctx, cancel := c.ctxFor(90 * time.Second)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	el, _, err := fetchTree(c, ctx, id)
	if err != nil {
		return err
	}
	matches := findElements(el, elemQuery{Text: *text, ID: *rid})
	if len(matches) > *index {
		m := matches[*index]
		if err := c.client().do(ctx, "POST", devPath(id, "/tap"), map[string]int{"x": m.X, "y": m.Y}, nil); err != nil {
			return err
		}
		if c.g.json {
			return c.printJSON(map[string]any{"ok": true, "via": "tree", "x": m.X, "y": m.Y, "matches": len(matches), "role": uitree.ShortRole(m.El.Role)})
		}
		fmt.Fprintf(c.out, "tapped %s %q at (%d, %d)\n", uitree.ShortRole(m.El.Role), firstNonEmpty(m.El.Text, m.El.Label, m.El.ResourceID), m.X, m.Y)
		return nil
	}
	what := fmt.Sprintf("--text %q", *text)
	if *rid != "" {
		what = fmt.Sprintf("--id %q", *rid)
	}
	if len(matches) > 0 {
		return fmt.Errorf("tap %s: only %d match(es), --index %d is out of range", what, len(matches), *index)
	}
	if !*ocr || *text == "" {
		hint := ""
		if *text != "" {
			hint = " (add --ocr to fall back to on-screen text recognition)"
		}
		return fmt.Errorf("tap %s: no matching element in the UI tree%s", what, hint)
	}
	var res json.RawMessage
	if err := c.client().do(ctx, "POST", "/ocr/devices/"+url.PathEscape(id)+"/tap", map[string]any{"text": *text, "index": *index}, &res); err != nil {
		return err
	}
	if c.g.json {
		return c.printRaw(res)
	}
	var r struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	_ = json.Unmarshal(res, &r)
	fmt.Fprintf(c.out, "tapped %q via OCR at (%d, %d)\n", *text, r.X, r.Y)
	return nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func cmdLongPress(c *call) error {
	fs := newFlagSet(c.name)
	ms := fs.Int("ms", 800, "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: pocketlab longpress X Y [--ms N]")
	}
	xy, err := atoiArgs(c, []string{"X", "Y"}, pos)
	if err != nil {
		return err
	}
	return c.post("/long-press", map[string]int{"x": xy[0], "y": xy[1], "ms": *ms}, 30*time.Second,
		fmt.Sprintf("long-pressed (%d, %d) for %dms", xy[0], xy[1], *ms))
}

// directionSwipe computes the finger path for a direction on a w x h screen.
// The direction is the way the finger moves ("up" moves the finger up).
func directionSwipe(dir string, w, h int) (x1, y1, x2, y2 int, ok bool) {
	cx, cy := w/2, h/2
	switch strings.ToLower(dir) {
	case "up":
		return cx, h * 7 / 10, cx, h * 3 / 10, true
	case "down":
		return cx, h * 3 / 10, cx, h * 7 / 10, true
	case "left":
		return w * 8 / 10, cy, w * 2 / 10, cy, true
	case "right":
		return w * 2 / 10, cy, w * 8 / 10, cy, true
	}
	return 0, 0, 0, 0, false
}

func cmdSwipe(c *call) error {
	fs := newFlagSet(c.name)
	ms := fs.Int("ms", 300, "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	switch len(pos) {
	case 1:
		if _, _, _, _, ok := directionSwipe(pos[0], 100, 100); !ok {
			return usagef("swipe: direction must be up, down, left or right, got %q", pos[0])
		}
		ctx, cancel := c.ctxFor(60 * time.Second)
		defer cancel()
		id, err := c.target(ctx)
		if err != nil {
			return err
		}
		w, h, err := screenSize(c, ctx, id)
		if err != nil {
			return err
		}
		x1, y1, x2, y2, _ := directionSwipe(pos[0], w, h)
		return swipeTo(c, ctx, id, x1, y1, x2, y2, *ms, pos[0])
	case 4:
		v, err := atoiArgs(c, []string{"X1", "Y1", "X2", "Y2"}, pos)
		if err != nil {
			return err
		}
		ctx, cancel := c.ctxFor(30 * time.Second)
		defer cancel()
		id, err := c.target(ctx)
		if err != nil {
			return err
		}
		return swipeTo(c, ctx, id, v[0], v[1], v[2], v[3], *ms, "")
	}
	return usagef("usage: pocketlab swipe X1 Y1 X2 Y2 [--ms N] | swipe up|down|left|right")
}

func swipeTo(c *call, ctx context.Context, id string, x1, y1, x2, y2, ms int, label string) error {
	body := map[string]int{"x1": x1, "y1": y1, "x2": x2, "y2": y2, "durationMs": ms}
	if err := c.client().do(ctx, "POST", devPath(id, "/swipe"), body, nil); err != nil {
		return err
	}
	msg := fmt.Sprintf("swiped (%d, %d) -> (%d, %d)", x1, y1, x2, y2)
	if label != "" {
		msg = fmt.Sprintf("swiped %s: (%d, %d) -> (%d, %d)", label, x1, y1, x2, y2)
	}
	return c.actionResult(msg)
}

// screenSize returns the logical screen size, from the UI tree or, failing
// that, the screenshot headers.
func screenSize(c *call, ctx context.Context, id string) (int, int, error) {
	if el, _, err := fetchTree(c, ctx, id); err == nil {
		if w, h := treeSize(el); w > 0 && h > 0 {
			return w, h, nil
		}
	}
	_, hdr, err := c.client().raw(ctx, "GET", devPath(id, "/screenshot"), nil)
	if err != nil {
		return 0, 0, err
	}
	w, _ := strconv.Atoi(hdr.Get("X-Screen-Width"))
	h, _ := strconv.Atoi(hdr.Get("X-Screen-Height"))
	if w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("cannot determine screen size; use explicit coordinates")
	}
	return w, h, nil
}

func cmdType(c *call) error {
	pos, err := c.parse(newFlagSet(c.name), c.args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usagef("usage: pocketlab type \"text\"")
	}
	text := strings.Join(pos, " ")
	return c.post("/type", map[string]string{"text": text}, 60*time.Second, fmt.Sprintf("typed %d characters", len([]rune(text))))
}

func cmdKey(c *call) error {
	pos, err := c.parse(newFlagSet(c.name), c.args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: pocketlab key home|back|enter|delete|volume_up|volume_down|...")
	}
	key := strings.ToLower(strings.TrimSpace(pos[0]))
	return c.post("/press-key", map[string]string{"key": key}, 30*time.Second, "pressed "+key)
}
