package dsl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strconv"
	"strings"
	"time"

	"phoneforge/internal/device"
)

// snapshot is a captured screen state used by observe and assert_screen_changed.
type snapshot struct {
	png      []byte
	nodes    []node
	screen   device.Rect
	treeHash [32]byte
	hasTree  bool
}

func (s *snapshot) texts(limit int) []string {
	var out []string
	for _, n := range s.nodes {
		if t := textOf(n.el); t != "" {
			out = append(out, t)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

func (r *runner) takeSnapshot(ctx context.Context, shot, tree bool) (*snapshot, error) {
	s := &snapshot{}
	if shot {
		sc, err := r.dev.Screenshot(ctx)
		if err != nil {
			return nil, err
		}
		s.png = sc.PNG
	}
	if tree {
		t, err := r.dev.UITree(ctx)
		if err != nil {
			return nil, fmt.Errorf("ui tree: %w", err)
		}
		b, _ := json.Marshal(t)
		s.treeHash = sha256.Sum256(b)
		s.hasTree = true
		s.nodes, s.screen = flatten(t), t.Bounds
	}
	return s, nil
}

// changePercent returns how much of the screen differs between a and b (0-100).
// Screenshots are compared pixel-wise; without usable screenshots the UI tree
// hash decides (0 or 100).
func changePercent(a, b *snapshot) float64 {
	if len(a.png) > 0 && len(b.png) > 0 {
		if bytes.Equal(a.png, b.png) {
			return 0
		}
		ia, errA := png.Decode(bytes.NewReader(a.png))
		ib, errB := png.Decode(bytes.NewReader(b.png))
		if errA != nil || errB != nil {
			return 100
		}
		return imageDiff(ia, ib)
	}
	if a.hasTree && b.hasTree && a.treeHash == b.treeHash {
		return 0
	}
	if len(a.png) == 0 && len(b.png) == 0 && !(a.hasTree && b.hasTree) {
		return 0
	}
	return 100
}

func imageDiff(a, b image.Image) float64 {
	ba, bb := a.Bounds(), b.Bounds()
	if ba.Dx() != bb.Dx() || ba.Dy() != bb.Dy() {
		return 100
	}
	stepX, stepY := ba.Dx()/200+1, ba.Dy()/400+1
	var total, diff int
	for y := 0; y < ba.Dy(); y += stepY {
		for x := 0; x < ba.Dx(); x += stepX {
			r1, g1, b1, _ := a.At(ba.Min.X+x, ba.Min.Y+y).RGBA()
			r2, g2, b2, _ := b.At(bb.Min.X+x, bb.Min.Y+y).RGBA()
			total++
			if absDiff(r1, r2) > 0x0800 || absDiff(g1, g2) > 0x0800 || absDiff(b1, b2) > 0x0800 {
				diff++
			}
		}
	}
	if total == 0 {
		return 0
	}
	return float64(diff) * 100 / float64(total)
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

func assertFail(st Step, format string, args ...any) error {
	if st.Message != "" {
		return codeErr(CodeAssertionFailed, "%s", st.Message)
	}
	return codeErr(CodeAssertionFailed, format, args...)
}

func (r *runner) doAssert(ctx context.Context, canon string, st Step, oc *outcome) error {
	switch canon {
	case "assert_screen_changed":
		return r.assertScreenChanged(ctx, st, oc)
	}
	if st.Selector != nil && st.Selector.CSSSelector != "" {
		return errWeb()
	}
	if st.Selector == nil {
		return codeErr(CodeValidation, "%s requires predicate", st.Action)
	}
	if st.Selector.isEmpty() {
		return codeErr(CodeValidation, "selector has no predicates")
	}
	switch canon {
	case "assert_exists":
		el, err := r.pollFind(ctx, st, true)
		if err != nil {
			if errors.Is(err, device.ErrElementNotFound) {
				return &stepError{code: CodeAssertionFailed, err: overrideMsg(st, err)}
			}
			return err
		}
		b := el.Bounds
		oc.matched = &b
		return nil
	case "assert_not_exists":
		el, err := r.pollFind(ctx, st, false)
		if err != nil {
			if errors.Is(err, errStillThere) {
				return assertFail(st, "element unexpectedly visible: %s", describe(*st.Selector))
			}
			return err
		}
		_ = el
		return nil
	case "assert_count":
		if st.Expected == nil {
			return codeErr(CodeValidation, "assert_count requires expected")
		}
		nodes, screen, err := r.tree(ctx)
		if err != nil {
			return err
		}
		idx, err := resolve(nodes, screen, *st.Selector)
		if err != nil {
			return err
		}
		n := countOutermost(nodes, idx)
		oc.set("matchCount", n)
		if n != *st.Expected {
			return assertFail(st, "expected %d matches, found %d: %s", *st.Expected, n, describe(*st.Selector))
		}
		return nil
	case "assert_property":
		return r.assertProperty(ctx, st, oc)
	}
	return nil
}

func overrideMsg(st Step, err error) error {
	if st.Message != "" {
		return errors.New(st.Message)
	}
	return err
}

var errStillThere = errors.New("element still present")

// pollFind checks for the predicate once (TimeoutMs 0) or until the wait
// elapses. want=true waits for it to appear; want=false waits for it to
// disappear (errStillThere when it never does).
func (r *runner) pollFind(ctx context.Context, st Step, want bool) (device.Element, error) {
	deadline := time.Now().Add(msDuration(st.TimeoutMs))
	for {
		nodes, screen, err := r.tree(ctx)
		if err != nil {
			return device.Element{}, err
		}
		idx, err := resolve(nodes, screen, *st.Selector)
		if err != nil {
			return device.Element{}, err
		}
		i, ok := pick(idx, *st.Selector)
		if want && ok {
			return nodes[i].el, nil
		}
		if !want && !ok {
			return device.Element{}, nil
		}
		if time.Now().After(deadline) || st.TimeoutMs <= 0 {
			if want {
				return device.Element{}, fmt.Errorf("%w: %s", device.ErrElementNotFound, describe(*st.Selector))
			}
			return nodes[i].el, errStillThere
		}
		if err := sleep(ctx, waitPollInterval); err != nil {
			return device.Element{}, err
		}
	}
}

func boolStr(b bool) string { return strconv.FormatBool(b) }

func (r *runner) assertProperty(ctx context.Context, st Step, oc *outcome) error {
	if st.Property == "" {
		return codeErr(CodeValidation, "assert_property requires property")
	}
	if st.ExpectedValue == nil {
		return codeErr(CodeValidation, "assert_property requires expected_value")
	}
	nodes, screen, err := r.tree(ctx)
	if err != nil {
		return err
	}
	idx, err := resolve(nodes, screen, *st.Selector)
	if err != nil {
		return err
	}
	if len(idx) == 0 {
		return fmt.Errorf("%w: %s", device.ErrElementNotFound, describe(*st.Selector))
	}
	if len(idx) > 1 && st.Selector.Index == 0 && st.Selector.Near == nil && st.Selector.BoundsHint == "" {
		return codeErr(CodeAmbiguousMatch, "assert_property predicate matched %d elements; add index, near or bounds_hint: %s", len(idx), describe(*st.Selector))
	}
	i, ok := pick(idx, *st.Selector)
	if !ok {
		return fmt.Errorf("%w: %s", device.ErrElementNotFound, describe(*st.Selector))
	}
	el := nodes[i].el
	b := el.Bounds
	oc.matched = &b
	role := strings.ToLower(el.Role)
	var got string
	switch normKey(st.Property) {
	case "enabled":
		got = boolStr(el.Enabled)
	case "visible":
		got = boolStr(isVisible(el, screen))
	case "focused":
		got = boolStr(el.Focused)
	case "text", "value":
		got = el.Text
		if normKey(st.Property) == "text" {
			got = textOf(el)
		}
	case "editable":
		got = boolStr(typeMatches(role, "input"))
	case "scrollable":
		got = boolStr(typeMatches(role, "scrollview"))
	case "selected":
		return codeErr(CodeValidation, "property selected is not available: the UI tree has no selected state")
	default:
		return codeErr(CodeValidation, "unknown property %q (enabled, visible, focused, editable, scrollable, value, text)", st.Property)
	}
	oc.set("propertyValue", got)
	want := string(*st.ExpectedValue)
	if got != want {
		return assertFail(st, "property %s is %q, expected %q", st.Property, got, want)
	}
	return nil
}

func (r *runner) assertScreenChanged(ctx context.Context, st Step, oc *outcome) error {
	threshold := 50.0
	if st.ThresholdPercent != nil {
		threshold = *st.ThresholdPercent
	}
	base := r.lastObs
	if base == nil {
		base = r.initial
	}
	if base == nil {
		return codeErr(CodeValidation, "assert_screen_changed has no baseline: run observe first")
	}
	cur, err := r.takeSnapshot(ctx, true, true)
	if err != nil {
		return err
	}
	pct := changePercent(base, cur)
	oc.set("screenChangePercent", pct)
	if pct < threshold {
		return assertFail(st, "screen changed %.1f%%, expected at least %.1f%%", pct, threshold)
	}
	return nil
}
