package dsl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"pocketlab/internal/device"
)

const (
	defaultLongPressMs   = 500
	defaultDragMs        = 500
	defaultSwipeMs       = 300
	defaultWaitTimeoutMs = 10000
	defaultMaxSwipes     = 8
	defaultMaxScrolls    = 10
	scrollSwipeMs        = 400
	defaultPinchScale    = 2.0
)

func unsupported(action, why string) error {
	if why == "" {
		return fmt.Errorf("%s: %w", action, device.ErrUnsupported)
	}
	return fmt.Errorf("%s: %w (%s)", action, device.ErrUnsupported, why)
}

// capability type-asserts dev to an optional interface, failing the step with
// device.ErrUnsupported when it is not implemented.
func capability[T any](dev device.Device, action string) (T, error) {
	c, ok := dev.(T)
	if !ok {
		return c, unsupported(action, "")
	}
	return c, nil
}

func contextIsWeb(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return strings.EqualFold(one, "web")
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, c := range many {
			if strings.EqualFold(c, "web") {
				return true
			}
		}
	}
	return false
}

// do executes one canonical action.
func (r *runner) do(ctx context.Context, canon string, st Step, oc *outcome) error {
	dev := r.dev
	if len(st.Context) > 0 && contextIsWeb(st.Context) {
		var many []string
		if canon == "observe" && json.Unmarshal(st.Context, &many) == nil && len(many) > 1 {
			oc.warn("web context ignored: %s", webNotSupportedPhrase)
		} else {
			return errWeb()
		}
	}
	switch canon {
	case "launch_app":
		if st.BundleID == "" {
			return codeErr(CodeValidation, "launch_app requires bundleId")
		}
		if len(st.Arguments) > 0 || len(st.Environment) > 0 {
			oc.warn("arguments/environment are not supported by this driver and were ignored")
		}
		if st.Debug || st.LogPath != "" {
			oc.warn("debug/log_path ignored")
		}
		if st.Fresh {
			if err := dev.StopApp(ctx, st.BundleID); err != nil {
				oc.warn("fresh: stop failed: %v", err)
			}
		}
		return dev.LaunchApp(ctx, st.BundleID)
	case "stop_app":
		if st.BundleID == "" {
			return codeErr(CodeValidation, "stop_app requires bundleId")
		}
		return dev.StopApp(ctx, st.BundleID)
	case "open_url":
		if st.URL == "" {
			return codeErr(CodeValidation, "open_url requires url")
		}
		u, err := capability[device.URLOpener](dev, canon)
		if err != nil {
			return err
		}
		return u.OpenURL(ctx, st.URL)
	case "tap":
		x, y, err := r.targetPoint(ctx, st, oc, true)
		if err != nil {
			return err
		}
		return dev.Tap(ctx, x, y)
	case "double_tap":
		g, err := capability[device.GestureExtras](dev, canon)
		if err != nil {
			return err
		}
		x, y, err := r.targetPoint(ctx, st, oc, true)
		if err != nil {
			return err
		}
		return g.DoubleTap(ctx, x, y)
	case "long_press":
		x, y, err := r.targetPoint(ctx, st, oc, true)
		if err != nil {
			return err
		}
		ms := waitMs(st)
		if ms <= 0 {
			ms = defaultLongPressMs
		}
		return dev.LongPress(ctx, x, y, ms)
	case "two_finger_tap":
		c, err := capability[TwoFingerTapper](dev, canon)
		if err != nil {
			return err
		}
		x, y, err := r.targetPoint(ctx, st, oc, true)
		if err != nil {
			return err
		}
		return c.TwoFingerTap(ctx, x, y)
	case "pinch":
		g, err := capability[device.GestureExtras](dev, canon)
		if err != nil {
			return err
		}
		x, y, err := r.targetPoint(ctx, st, oc, true)
		if err != nil {
			return err
		}
		scale := defaultPinchScale
		if st.Scale != nil {
			scale = *st.Scale
		}
		if scale <= 0 {
			return codeErr(CodeValidation, "pinch scale must be > 0")
		}
		ms := st.DurationMs
		if ms <= 0 {
			ms = defaultDragMs
		}
		return g.Pinch(ctx, x, y, scale, ms)
	case "swipe":
		return r.doSwipe(ctx, st, oc)
	case "scroll":
		return r.doScroll(ctx, st, oc)
	case "drag":
		return r.doDrag(ctx, st, oc)
	case "drag_path":
		if len(st.Points) < 2 {
			return codeErr(CodeValidation, "drag_path requires at least 2 points")
		}
		for i, p := range st.Points[1:] {
			if p.DurationMs <= 0 {
				return codeErr(CodeValidation, "drag_path point %d needs duration_ms", i+1)
			}
		}
		c, err := capability[PathDragger](dev, canon)
		if err != nil {
			return err
		}
		return c.DragPath(ctx, st.Points)
	case "perform_actions":
		return unsupported(canon, "W3C input sequences need driver support")
	case "type", "type_secret":
		return r.doType(ctx, canon, st, oc)
	case "clear":
		return r.doClear(ctx, st, oc)
	case "dismiss_keyboard":
		k, err := capability[device.KeyboardDismisser](dev, canon)
		if err != nil {
			return err
		}
		return k.DismissKeyboard(ctx)
	case "copy_text":
		if st.Selector == nil {
			return codeErr(CodeValidation, "copy_text requires predicate")
		}
		el, err := r.find(ctx, st.Selector, canon)
		if err != nil {
			return err
		}
		b := el.Bounds
		oc.matched = &b
		text := textOf(el)
		if st.StoreAs != "" {
			r.vars[st.StoreAs] = text
		}
		oc.set("text", text)
		if c, ok := dev.(device.Clipboard); ok {
			return c.SetClipboard(ctx, text)
		}
		return nil
	case "paste_text":
		c, err := capability[device.Clipboard](dev, canon)
		if err != nil {
			return err
		}
		text, err := c.GetClipboard(ctx)
		if err != nil {
			return err
		}
		return dev.TypeText(ctx, text)
	case "set_clipboard":
		c, err := capability[device.Clipboard](dev, canon)
		if err != nil {
			return err
		}
		return c.SetClipboard(ctx, st.Text)
	case "press_key":
		if st.Key == "" {
			return codeErr(CodeValidation, "press_key requires key")
		}
		return dev.PressKey(ctx, device.Key(strings.ToLower(st.Key)))
	case "navigate":
		return r.doNavigate(ctx, st)
	case "toggle":
		if st.Selector == nil {
			return codeErr(CodeValidation, "toggle requires predicate")
		}
		switch strings.ToLower(st.State) {
		case "":
		case "on", "off":
			return unsupported(canon, "the UI tree does not expose checked state; omit state to flip the control")
		default:
			return codeErr(CodeValidation, "toggle state must be on or off")
		}
		x, y, err := r.targetPoint(ctx, st, oc, true)
		if err != nil {
			return err
		}
		oc.set("toggled", true)
		return dev.Tap(ctx, x, y)
	case "select_picker":
		if st.Value == "" {
			return codeErr(CodeValidation, "select_picker requires value")
		}
		c, err := capability[PickerSelector](dev, canon)
		if err != nil {
			return err
		}
		x, y, err := r.targetPoint(ctx, st, oc, true)
		if err != nil {
			return err
		}
		return c.SelectPickerValue(ctx, x, y, st.Value)
	case "observe":
		return r.doObserve(ctx, st, oc)
	case "audit":
		return unsupported(canon, "accessibility audit is not implemented")
	case "screenshot":
		shot, err := dev.Screenshot(ctx)
		if err != nil {
			return err
		}
		if len(shot.PNG) > 0 {
			oc.screenshot = b64(shot.PNG)
		}
		if st.FilePath != "" {
			oc.warn("file_path ignored: the screenshot is returned inline")
		}
		return nil
	case "record_start", "record_stop":
		return unsupported(canon, "start/stop recording is not available")
	case "wait":
		ms := waitMs(st)
		if ms <= 0 {
			return codeErr(CodeValidation, "delay requires duration_ms > 0")
		}
		return sleep(ctx, msDuration(ms))
	case "wait_for", "wait_stable":
		return r.doWaitFor(ctx, canon == "wait_stable", st, oc)
	case "scroll_until_visible":
		return r.doScrollUntilVisible(ctx, st, oc)
	case "checkpoint":
		if st.Name == "" {
			return codeErr(CodeValidation, "checkpoint requires name")
		}
		r.checks = append(r.checks, Checkpoint{Name: st.Name, StepIndex: r.curIdx,
			ElapsedMs: time.Since(r.start).Milliseconds(), Metadata: st.Metadata})
		return nil
	case "set_location":
		if st.Lat == nil || st.Lon == nil {
			return codeErr(CodeValidation, "set_location requires lat and lon")
		}
		if *st.Lat < -90 || *st.Lat > 90 || *st.Lon < -180 || *st.Lon > 180 || (*st.Lat == 0 && *st.Lon == 0) {
			return codeErr(CodeValidation, "set_location: lat must be -90..90, lon -180..180, and not exactly 0,0")
		}
		c, err := capability[LocationController](dev, canon)
		if err != nil {
			return err
		}
		return c.SetLocation(ctx, *st.Lat, *st.Lon)
	case "reset_location":
		c, err := capability[LocationController](dev, canon)
		if err != nil {
			return err
		}
		return c.ResetLocation(ctx)
	case "set_permissions":
		return r.doSetPermissions(ctx, st)
	case "grant_permission", "revoke_permission":
		if st.BundleID == "" || st.Permission == "" {
			return codeErr(CodeValidation, "%s requires bundleId and permission", canon)
		}
		p, err := capability[device.PermissionManager](dev, canon)
		if err != nil {
			return err
		}
		return p.SetPermission(ctx, st.BundleID, st.Permission, canon == "grant_permission")
	case "install_app":
		if st.Path == "" {
			return codeErr(CodeValidation, "install_app requires path")
		}
		i, err := capability[device.AppInstaller](dev, canon)
		if err != nil {
			return err
		}
		return i.InstallApp(ctx, st.Path)
	case "uninstall_app":
		if st.BundleID == "" {
			return codeErr(CodeValidation, "uninstall_app requires bundleId")
		}
		i, err := capability[device.AppInstaller](dev, canon)
		if err != nil {
			return err
		}
		return i.UninstallApp(ctx, st.BundleID)
	case "biometrics":
		state := strings.ToLower(st.State)
		switch state {
		case "fail", "no_match":
			state = "nomatch"
		case "match", "nomatch", "enroll", "unenroll":
		default:
			return codeErr(CodeValidation, "biometrics state must be match, nomatch, enroll or unenroll")
		}
		c, err := capability[BiometricsController](dev, canon)
		if err != nil {
			return err
		}
		return c.Biometrics(ctx, state, strings.ToLower(st.Sensor))
	case "fold":
		state := strings.ToLower(st.State)
		if st.Angle == nil {
			switch state {
			case "closed", "book", "open":
			default:
				return codeErr(CodeValidation, "fold requires state closed|book|open or angle 0..180")
			}
		} else if *st.Angle < 0 || *st.Angle > 180 {
			return codeErr(CodeValidation, "fold angle must be 0..180")
		}
		c, err := capability[FoldController](dev, canon)
		if err != nil {
			return err
		}
		return c.Fold(ctx, state, st.Angle)
	case "siri":
		if st.Prompt == "" {
			return codeErr(CodeValidation, "siri requires prompt")
		}
		c, err := capability[SiriController](dev, canon)
		if err != nil {
			return err
		}
		reply, err := c.Siri(ctx, st.Prompt)
		if err != nil {
			return err
		}
		r.vars["siri_response"] = reply
		oc.set("siriResponse", reply)
		return nil
	case "metrics_start", "metrics_stop":
		return unsupported(canon, "performance metrics are not implemented")
	case "extract":
		return r.doExtract(ctx, st, oc)
	case "eval_script", "run_script":
		return unsupported(canon, "scripting needs a JavaScript runtime, which Pocketlab does not embed")
	case "request_user_input":
		return codeErr(CodeExecutionError, "request_user_input always fails inside scripts")
	case "select_web_context", "execute_js":
		return errWeb()
	case "assert_exists", "assert_not_exists", "assert_count", "assert_property", "assert_screen_changed":
		return r.doAssert(ctx, canon, st, oc)
	case "if_exists":
		return r.doIfExists(ctx, st, oc)
	case "repeat":
		return r.doRepeat(ctx, st, oc)
	case "ocr_tap", "assert_text_ocr", "ocr":
		return r.doOCR(ctx, canon, st, oc)
	case "web_navigate", "web_click", "web_type", "web_eval", "web_wait", "web_assert_text":
		return r.doWeb(ctx, canon, st, oc)
	}
	return codeErr(CodeInvalidAction, "unknown action %q", st.Action)
}

// ---- element lookup ----

func (r *runner) tree(ctx context.Context) ([]node, device.Rect, error) {
	t, err := r.dev.UITree(ctx)
	if err != nil {
		return nil, device.Rect{}, fmt.Errorf("ui tree: %w", err)
	}
	return flatten(t), t.Bounds, nil
}

func checkSelector(sel *Selector, action string) error {
	if sel == nil {
		return codeErr(CodeValidation, "%s requires selector", action)
	}
	if sel.CSSSelector != "" {
		return errWeb()
	}
	if sel.isEmpty() {
		return codeErr(CodeValidation, "selector has no predicates")
	}
	return nil
}

// findNode resolves sel against the live tree and returns the picked node.
func (r *runner) findNode(ctx context.Context, sel *Selector, action string) ([]node, device.Rect, int, error) {
	if err := checkSelector(sel, action); err != nil {
		return nil, device.Rect{}, 0, err
	}
	nodes, screen, err := r.tree(ctx)
	if err != nil {
		return nil, screen, 0, err
	}
	idx, err := resolve(nodes, screen, *sel)
	if err != nil {
		return nodes, screen, 0, err
	}
	i, ok := pick(idx, *sel)
	if !ok {
		return nodes, screen, 0, fmt.Errorf("%w: %s", device.ErrElementNotFound, describe(*sel))
	}
	return nodes, screen, i, nil
}

func (r *runner) find(ctx context.Context, sel *Selector, action string) (device.Element, error) {
	nodes, _, i, err := r.findNode(ctx, sel, action)
	if err != nil {
		return device.Element{}, err
	}
	return nodes[i].el, nil
}

func (r *runner) screenSize(ctx context.Context) (int, int, error) {
	t, err := r.dev.UITree(ctx)
	if err == nil {
		if w, h := t.Bounds.X+t.Bounds.W, t.Bounds.Y+t.Bounds.H; w > 0 && h > 0 {
			return w, h, nil
		}
	}
	shot, serr := r.dev.Screenshot(ctx)
	if serr == nil && shot.Width > 0 && shot.Height > 0 {
		return shot.Width, shot.Height, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return 0, 0, errors.New("unknown screen size")
}

func (r *runner) coordsPoint(ctx context.Context, c Coords) (int, int, error) {
	val := func(abs, pct *float64, size int) (int, bool) {
		if abs != nil {
			return int(*abs), true
		}
		if pct != nil {
			return int(*pct / 100 * float64(size)), true
		}
		return 0, false
	}
	needSize := c.XPercent != nil || c.YPercent != nil
	w, h := 0, 0
	if needSize {
		var err error
		if w, h, err = r.screenSize(ctx); err != nil {
			return 0, 0, err
		}
	}
	x, okx := val(c.X, c.XPercent, w)
	y, oky := val(c.Y, c.YPercent, h)
	if !okx || !oky {
		return 0, 0, codeErr(CodeValidation, "coordinates need x/y or x_percent/y_percent")
	}
	return x, y, nil
}

// targetPoint resolves where to act: explicit x/y, then coords, then the
// predicate's element centre.
func (r *runner) targetPoint(ctx context.Context, st Step, oc *outcome, settle bool) (int, int, error) {
	if st.X != nil && st.Y != nil {
		return *st.X, *st.Y, nil
	}
	if st.Coords != nil {
		return r.coordsPoint(ctx, *st.Coords)
	}
	if st.Selector == nil {
		return 0, 0, codeErr(CodeValidation, "%s requires predicate, coords or x/y", st.Action)
	}
	el, err := r.find(ctx, st.Selector, st.Action)
	if err != nil {
		return 0, 0, err
	}
	if settle && r.settle {
		el = r.settleElement(ctx, st.Selector, el)
	}
	b := el.Bounds
	oc.matched = &b
	x, y := b.Center()
	return x, y, nil
}

// settleElement re-resolves the predicate until the element stops moving (max 500 ms).
func (r *runner) settleElement(ctx context.Context, sel *Selector, el device.Element) device.Element {
	deadline := time.Now().Add(settleStable)
	for time.Now().Before(deadline) {
		if sleep(ctx, 100*time.Millisecond) != nil {
			return el
		}
		next, err := r.find(ctx, sel, "settle")
		if err != nil {
			return el
		}
		if next.Bounds == el.Bounds {
			return next
		}
		el = next
	}
	return el
}

// ---- gestures ----

func dirVec(d string) (dx, dy int, ok bool) {
	switch strings.ToLower(d) {
	case "up":
		return 0, -1, true
	case "down":
		return 0, 1, true
	case "left":
		return -1, 0, true
	case "right":
		return 1, 0, true
	}
	return 0, 0, false
}

func (r *runner) refPoint(ctx context.Context, ref *ElementRef, c *Coords, oc *outcome) (int, int, bool, error) {
	switch {
	case ref != nil && ref.Predicate != nil:
		el, err := r.find(ctx, ref.Predicate, "target")
		if err != nil {
			return 0, 0, false, err
		}
		b := el.Bounds
		if oc.matched == nil {
			oc.matched = &b
		}
		x, y := b.Center()
		return x, y, true, nil
	case c != nil:
		x, y, err := r.coordsPoint(ctx, *c)
		return x, y, true, err
	}
	return 0, 0, false, nil
}

func (r *runner) doSwipe(ctx context.Context, st Step, oc *outcome) error {
	ms := st.DurationMs
	if ms <= 0 {
		ms = defaultSwipeMs
	}
	if st.Direction != "" {
		dx, dy, ok := dirVec(st.Direction)
		if !ok {
			return codeErr(CodeValidation, "swipe direction must be up, down, left or right")
		}
		frac := 0.5
		switch strings.ToLower(st.Distance) {
		case "", "medium":
		case "short":
			frac = 0.25
		case "full":
			frac = 0.75
		default:
			return codeErr(CodeValidation, "swipe distance must be short, medium or full")
		}
		w, h, err := r.screenSize(ctx)
		if err != nil {
			return err
		}
		length := frac * float64(h)
		if dx != 0 {
			length = frac * float64(w)
		}
		cx, cy := w/2, h/2
		if px, py, ok, err := r.refPoint(ctx, st.From, st.FromCoords, oc); err != nil {
			return err
		} else if ok {
			x1, y1 := px, py
			return r.dev.Swipe(ctx, x1, y1, x1+int(float64(dx)*length), y1+int(float64(dy)*length), ms)
		}
		half := length / 2
		return r.dev.Swipe(ctx, cx-int(float64(dx)*half), cy-int(float64(dy)*half),
			cx+int(float64(dx)*half), cy+int(float64(dy)*half), ms)
	}
	if st.FromCoords != nil || st.ToCoords != nil || st.From != nil {
		x1, y1, okA, err := r.refPoint(ctx, st.From, st.FromCoords, oc)
		if err != nil {
			return err
		}
		x2, y2, okB, err := r.refPoint(ctx, nil, st.ToCoords, oc)
		if err != nil {
			return err
		}
		if !okA || !okB {
			return codeErr(CodeValidation, "swipe needs both from/from_coords and to_coords")
		}
		return r.dev.Swipe(ctx, x1, y1, x2, y2, ms)
	}
	return r.dev.Swipe(ctx, st.X1, st.Y1, st.X2, st.Y2, st.DurationMs)
}

func (r *runner) doScroll(ctx context.Context, st Step, oc *outcome) error {
	dir := st.Direction
	if dir == "" {
		dir = "down"
	}
	dx, dy, ok := dirVec(dir)
	if !ok {
		return codeErr(CodeValidation, "scroll direction must be up, down, left or right")
	}
	frac := 0.5
	switch strings.ToLower(st.Amount) {
	case "", "page":
	case "small":
		frac = 0.2
	case "full":
		frac = 0.8
	default:
		return codeErr(CodeValidation, "scroll amount must be small, page or full")
	}
	var target *Selector
	fully := false
	if st.ToElement != nil {
		target, fully = st.ToElement.Predicate, st.ToElement.FullyVisible
	} else if st.ToSelector != nil {
		target = st.ToSelector
	}
	if target != nil {
		if err := checkSelector(target, "scroll"); err != nil {
			return err
		}
	}
	max := st.MaxScrolls
	if max <= 0 {
		max = defaultMaxScrolls
	}
	for i := 0; ; i++ {
		if target != nil {
			nodes, screen, err := r.tree(ctx)
			if err != nil {
				return err
			}
			idx, err := resolve(nodes, screen, *target)
			if err != nil {
				return err
			}
			if k, ok := pick(idx, *target); ok {
				e := nodes[k].el
				if (fully && fullyVisible(e, screen)) || (!fully && isVisible(e, screen)) {
					b := e.Bounds
					oc.matched = &b
					return nil
				}
			}
			if i >= max {
				return fmt.Errorf("%w: not visible after %d scrolls (%s): %s", device.ErrElementNotFound, max, dir, describe(*target))
			}
		}
		w, h, err := r.screenSize(ctx)
		if err != nil {
			return err
		}
		cx, cy := w/2, h/2
		if st.Selector != nil {
			el, err := r.find(ctx, st.Selector, "scroll")
			if err != nil {
				return err
			}
			cx, cy = el.Bounds.Center()
		}
		length := frac * float64(h)
		if dx != 0 {
			length = frac * float64(w)
		}
		half := int(length / 2)
		// Scrolling "down" moves content up, so the finger travels opposite to the direction.
		if err := r.dev.Swipe(ctx, cx+dx*half, cy+dy*half, cx-dx*half, cy-dy*half, scrollSwipeMs); err != nil {
			return err
		}
		if target == nil {
			return nil
		}
		if err := sleep(ctx, scrollSettle); err != nil {
			return err
		}
	}
}

func (r *runner) doDrag(ctx context.Context, st Step, oc *outcome) error {
	g, err := capability[device.GestureExtras](r.dev, "drag")
	if err != nil {
		return err
	}
	x1, y1, x2, y2 := st.X1, st.Y1, st.X2, st.Y2
	switch {
	case st.Selector != nil || st.From != nil || st.FromCoords != nil:
		from := st.From
		if from == nil && st.Selector != nil {
			from = &ElementRef{Predicate: st.Selector}
		}
		px, py, ok, err := r.refPoint(ctx, from, st.FromCoords, oc)
		if err != nil {
			return err
		}
		if !ok {
			return codeErr(CodeValidation, "drag needs a source")
		}
		to := st.ToElement
		if to == nil && st.ToSelector != nil {
			to = &ElementRef{Predicate: st.ToSelector}
		}
		qx, qy, ok, err := r.refPoint(ctx, to, st.ToCoords, oc)
		if err != nil {
			return err
		}
		if !ok {
			return codeErr(CodeValidation, "drag with a source element requires to_element or to_coords")
		}
		x1, y1, x2, y2 = px, py, qx, qy
	case st.ToCoords != nil || st.ToElement != nil:
		return codeErr(CodeValidation, "drag needs from or from_coords")
	}
	if st.PressDurationMs > 0 || st.HoldDurationMs > 0 {
		oc.warn("press_duration_ms / hold_duration_ms are not supported by this driver and were ignored")
	}
	ms := st.DurationMs
	if ms <= 0 {
		ms = defaultDragMs
	}
	return g.Drag(ctx, x1, y1, x2, y2, ms)
}

// ---- text ----

func clearCount(el device.Element) int {
	n := len([]rune(textOf(el))) + 1
	if n > 256 {
		n = 256
	}
	return n
}

func (r *runner) clearFocused(ctx context.Context, el *device.Element) error {
	if c, ok := r.dev.(TextClearer); ok {
		return c.ClearText(ctx)
	}
	n := 40
	if el != nil {
		n = clearCount(*el)
	}
	for i := 0; i < n; i++ {
		if err := r.dev.PressKey(ctx, device.KeyDelete); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) doType(ctx context.Context, canon string, st Step, oc *outcome) error {
	text := st.Text
	if canon == "type_secret" {
		if st.SecretID == "" {
			return codeErr(CodeValidation, "type_secret requires secret_id")
		}
		if r.opts.Secrets == nil {
			return codeErr(CodeValidation, "secret %q is not available: no secret store is configured", st.SecretID)
		}
		v, err := r.opts.Secrets(st.SecretID)
		if err != nil {
			return codeErr(CodeValidation, "secret %q: %v", st.SecretID, err)
		}
		text = v
	}
	var el *device.Element
	if st.Selector != nil || st.Coords != nil || (st.X != nil && st.Y != nil) {
		if st.Selector != nil && st.X == nil && st.Coords == nil {
			e, err := r.find(ctx, st.Selector, canon)
			if err != nil {
				return err
			}
			el = &e
		}
		x, y, err := r.targetPoint(ctx, st, oc, true)
		if err != nil {
			return err
		}
		if err := r.dev.Tap(ctx, x, y); err != nil {
			return err
		}
	}
	if st.ClearFirst {
		if err := r.clearFocused(ctx, el); err != nil {
			return err
		}
	}
	if err := r.dev.TypeText(ctx, text); err != nil {
		return err
	}
	if st.DismissKeyboard {
		if k, ok := r.dev.(device.KeyboardDismisser); ok {
			return k.DismissKeyboard(ctx)
		}
		oc.warn("dismiss_keyboard ignored: not supported by this driver")
	}
	return nil
}

func (r *runner) doClear(ctx context.Context, st Step, oc *outcome) error {
	if st.Selector != nil {
		el, err := r.find(ctx, st.Selector, "clear")
		if err != nil {
			return err
		}
		b := el.Bounds
		oc.matched = &b
		x, y := b.Center()
		if err := r.dev.Tap(ctx, x, y); err != nil {
			return err
		}
		return r.clearFocused(ctx, &el)
	}
	nodes, _, err := r.tree(ctx)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if n.el.Focused {
			e := n.el
			b := e.Bounds
			oc.matched = &b
			return r.clearFocused(ctx, &e)
		}
	}
	return codeErr(CodeNoMatch, "clear without predicate needs a focused element")
}

func (r *runner) doNavigate(ctx context.Context, st Step) error {
	t := strings.TrimSpace(st.Target)
	switch strings.ToLower(t) {
	case "":
		return codeErr(CodeValidation, "navigate requires target (home, back, recent_apps or a URL)")
	case "home", "back", "recent_apps":
		return r.dev.PressKey(ctx, device.Key(strings.ToLower(t)))
	}
	if strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") {
		u, err := capability[device.URLOpener](r.dev, "navigate")
		if err != nil {
			return err
		}
		return u.OpenURL(ctx, t)
	}
	return codeErr(CodeValidation, "navigate target must be home, back, recent_apps or an http(s) URL")
}

func (r *runner) doSetPermissions(ctx context.Context, st Step) error {
	if st.BundleID == "" || len(st.Permissions) == 0 {
		return codeErr(CodeValidation, "set_permissions requires bundleId and permissions")
	}
	p, err := capability[device.PermissionManager](r.dev, "set_permissions")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(st.Permissions))
	for n := range st.Permissions {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if (names[i] == "all") != (names[j] == "all") {
			return names[i] == "all" // "all" applies first
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		switch strings.ToLower(st.Permissions[n]) {
		case "allow":
			err = p.SetPermission(ctx, st.BundleID, n, true)
		case "deny":
			err = p.SetPermission(ctx, st.BundleID, n, false)
		case "unset":
			return unsupported("set_permissions", "unset is not supported")
		default:
			return codeErr(CodeValidation, "permission %q value must be allow, deny or unset", n)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ---- observe / extract ----

func (r *runner) doObserve(ctx context.Context, st Step, oc *outcome) error {
	include := []string{"ui_tree", "screenshot"}
	if len(st.Include) > 0 {
		var list []string
		if err := json.Unmarshal(st.Include, &list); err != nil {
			return codeErr(CodeValidation, "observe include must be an array of strings")
		}
		include = list
	}
	wantTree, wantShot := false, false
	for _, inc := range include {
		switch normKey(inc) {
		case "uitree":
			wantTree = true
		case "screenshot":
			wantShot = true
		case "installedapps":
			apps, err := r.dev.ListApps(ctx)
			if err != nil {
				return err
			}
			oc.set("installedApps", len(apps))
		case "ocr":
			lines, _, err := r.ocrLines(ctx)
			if err != nil {
				return err
			}
			oc.set("ocrText", ocrTexts(lines))
		case "activity", "pageinfo":
			oc.warn("observe include %q is not supported and was ignored", inc)
		default:
			return codeErr(CodeValidation, "unknown observe include %q", inc)
		}
	}
	if wantTree || wantShot {
		snap, err := r.takeSnapshot(ctx, wantShot, wantTree)
		if err != nil {
			return err
		}
		r.lastObs = snap
		if wantTree {
			oc.set("elements", len(snap.nodes))
			oc.set("texts", snap.texts(200))
		}
		if wantShot {
			oc.set("screenshotBytes", len(snap.png))
			if len(snap.png) > 0 && !st.Compact {
				oc.screenshot = b64(snap.png)
			}
		}
		if st.StoreAs != "" {
			r.vars[st.StoreAs] = strings.Join(snap.texts(200), "\n")
		}
	}
	return nil
}

func (r *runner) doExtract(ctx context.Context, st Step, oc *outcome) error {
	if st.ExtractKey == "" {
		return codeErr(CodeValidation, "extract requires extract_key")
	}
	kind := strings.ToLower(st.ExtractKind)
	if kind == "" {
		switch {
		case st.Selector != nil:
			kind = "text"
		case st.Text != "":
			kind = "literal"
		default:
			return codeErr(CodeValidation, "extract needs a predicate (text), text (literal) or extract_kind screenshot")
		}
	}
	switch kind {
	case "literal":
		r.vars[st.ExtractKey] = st.Text
	case "text":
		el, err := r.find(ctx, st.Selector, "extract")
		if err != nil {
			return err
		}
		b := el.Bounds
		oc.matched = &b
		val := textOf(el)
		if st.ExtractRegex != "" {
			re, err := compileRegex(st.ExtractRegex)
			if err != nil {
				return err
			}
			m := re.FindStringSubmatch(val)
			if m == nil {
				return codeErr(CodeNoMatch, "extract_regex %q did not match %q", st.ExtractRegex, val)
			}
			val = m[0]
			if len(m) > 1 {
				val = m[1]
			}
		}
		r.vars[st.ExtractKey] = val
	case "screenshot":
		shot, err := r.dev.Screenshot(ctx)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(shot.PNG)
		r.vars[st.ExtractKey] = hex.EncodeToString(sum[:])
		if st.FilePath != "" {
			oc.warn("file_path ignored: only the screenshot hash is stored")
		}
	default:
		return codeErr(CodeValidation, "extract_kind must be text, screenshot or literal")
	}
	oc.set("value", r.vars[st.ExtractKey])
	return nil
}

// ---- waits ----

func (r *runner) doWaitFor(ctx context.Context, forceStable bool, st Step, oc *outcome) error {
	if st.Selector != nil && st.Selector.CSSSelector != "" {
		return errWeb()
	}
	timeout := st.TimeoutMs
	if timeout <= 0 {
		timeout = defaultWaitTimeoutMs
		if r.script.Defaults.TimeoutMs > 0 {
			timeout = r.script.Defaults.TimeoutMs
		}
	}
	poll := waitPollInterval
	if st.PollIntervalMs > 0 {
		poll = msDuration(st.PollIntervalMs)
	}
	stable := forceStable || st.Stable || st.Selector == nil || st.Selector.isEmpty()
	wctx, cancel := context.WithTimeout(ctx, msDuration(timeout))
	defer cancel()
	timedOut := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		what := "the screen to stop changing"
		if st.Selector != nil && !st.Selector.isEmpty() {
			what = describe(*st.Selector)
		}
		return codeErr(CodeTimeout, "%w: timed out after %dms waiting for %s", device.ErrElementNotFound, timeout, what)
	}
	if st.Selector != nil && !st.Selector.isEmpty() {
		for {
			el, err := r.find(wctx, st.Selector, "wait_for")
			if err == nil {
				b := el.Bounds
				oc.matched = &b
				break
			}
			if !errors.Is(err, device.ErrElementNotFound) {
				if ctx.Err() == nil && wctx.Err() != nil {
					return timedOut()
				}
				return err
			}
			if sleep(wctx, poll) != nil {
				return timedOut()
			}
		}
	}
	if !stable {
		return nil
	}
	prev := ""
	for {
		cur, err := r.treeSignature(wctx)
		if err != nil {
			if ctx.Err() == nil && wctx.Err() != nil {
				return timedOut()
			}
			return err
		}
		if cur == prev {
			return nil
		}
		prev = cur
		if sleep(wctx, poll) != nil {
			return timedOut()
		}
	}
}

func (r *runner) treeSignature(ctx context.Context) (string, error) {
	t, err := r.dev.UITree(ctx)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(t)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// doScrollUntilVisible swipes until the selector is visible or maxSwipes is reached.
func (r *runner) doScrollUntilVisible(ctx context.Context, st Step, oc *outcome) error {
	if err := checkSelector(st.Selector, "scroll_until_visible"); err != nil {
		return err
	}
	down := true
	switch st.Direction {
	case "", "down":
	case "up":
		down = false
	default:
		return codeErr(CodeValidation, "scroll_until_visible: unknown direction %q (use up or down)", st.Direction)
	}
	max := st.MaxSwipes
	if max <= 0 {
		max = defaultMaxSwipes
	}
	for i := 0; ; i++ {
		el, err := r.find(ctx, st.Selector, "scroll_until_visible")
		if err == nil {
			b := el.Bounds
			oc.matched = &b
			return nil
		}
		if !errors.Is(err, device.ErrElementNotFound) {
			return err
		}
		if i >= max {
			dn := "up"
			if down {
				dn = "down"
			}
			return fmt.Errorf("%w: not visible after %d swipes (%s): %s", device.ErrElementNotFound, max, dn, describe(*st.Selector))
		}
		shot, err := r.dev.Screenshot(ctx)
		if err != nil {
			return err
		}
		w, h := shot.Width, shot.Height
		if w <= 0 || h <= 0 {
			tree, terr := r.dev.UITree(ctx)
			if terr != nil {
				return terr
			}
			w, h = tree.Bounds.X+tree.Bounds.W, tree.Bounds.Y+tree.Bounds.H
		}
		if w <= 0 || h <= 0 {
			return errors.New("scroll_until_visible: unknown screen size")
		}
		x, hi, lo := w/2, h*7/10, h*3/10
		y1, y2 := hi, lo // "down": content moves up, finger swipes up
		if !down {
			y1, y2 = lo, hi
		}
		if err := r.dev.Swipe(ctx, x, y1, x, y2, scrollSwipeMs); err != nil {
			return err
		}
		if err := sleep(ctx, scrollSettle); err != nil {
			return err
		}
	}
}
