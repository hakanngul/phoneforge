package dsl

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"

	"pocketlab/internal/device"
)

// UnmarshalJSON decodes a near predicate: the anchor's predicate keys plus
// direction and max_distance.
func (n *Near) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var out Near
	rest := map[string]json.RawMessage{}
	for k, v := range raw {
		switch normKey(k) {
		case "direction":
			if err := json.Unmarshal(v, &out.Direction); err != nil {
				return fmt.Errorf("near.direction: %w", err)
			}
		case "maxdistance":
			if err := json.Unmarshal(v, &out.MaxDistance); err != nil {
				return fmt.Errorf("near.max_distance: %w", err)
			}
		default:
			rest[k] = v
		}
	}
	b, _ := json.Marshal(rest)
	if err := json.Unmarshal(b, &out.Selector); err != nil {
		return err
	}
	switch strings.ToLower(out.Direction) {
	case "", "above", "below", "left", "right", "any":
	default:
		return fmt.Errorf("near.direction %q (use above, below, left, right or any)", out.Direction)
	}
	out.Direction = strings.ToLower(out.Direction)
	*n = out
	return nil
}

// isEmpty reports whether the selector has no predicate fields (Index alone
// does not count).
func (s Selector) isEmpty() bool {
	return s.Text == "" && s.TextContains == "" && s.TextStartsWith == "" && s.TextRegex == "" &&
		s.Value == "" && s.ValueContains == "" && s.ResourceID == "" && s.AccessibilityID == "" &&
		s.Label == "" && s.LabelContains == "" && s.Role == "" && s.Type == "" &&
		s.Enabled == nil && s.Visible == nil && s.Selected == nil && s.Near == nil &&
		s.BoundsHint == "" && s.ParentOf == nil && s.CSSSelector == ""
}

var regexCache sync.Map

func compileRegex(p string) (*regexp.Regexp, error) {
	if v, ok := regexCache.Load(p); ok {
		return v.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, codeErr(CodeValidation, "invalid text_regex %q: %v", p, err)
	}
	regexCache.Store(p, re)
	return re, nil
}

// check reports predicates that cannot be evaluated.
func (s Selector) check() error {
	if s.CSSSelector != "" {
		return errWeb()
	}
	if s.Selected != nil {
		return codeErr(CodeValidation, "selected predicate is not supported: the UI tree has no selected state")
	}
	if s.TextRegex != "" {
		if _, err := compileRegex(s.TextRegex); err != nil {
			return err
		}
	}
	if s.BoundsHint != "" {
		if _, ok := hintRegion(s.BoundsHint, device.Rect{W: 100, H: 100}); !ok {
			return codeErr(CodeValidation, "unknown bounds_hint %q", s.BoundsHint)
		}
	}
	if s.Near != nil {
		if err := s.Near.Selector.check(); err != nil {
			return err
		}
	}
	if s.ParentOf != nil {
		return s.ParentOf.check()
	}
	return nil
}

// textOf is the text a text predicate compares against: Text, else Label.
func textOf(e device.Element) string {
	if e.Text != "" {
		return e.Text
	}
	return e.Label
}

func isVisible(e device.Element, screen device.Rect) bool {
	b := e.Bounds
	if b.W <= 0 || b.H <= 0 {
		return false
	}
	if screen.W <= 0 || screen.H <= 0 {
		return true
	}
	return b.X < screen.X+screen.W && b.X+b.W > screen.X && b.Y < screen.Y+screen.H && b.Y+b.H > screen.Y
}

func fullyVisible(e device.Element, screen device.Rect) bool {
	b := e.Bounds
	if b.W <= 0 || b.H <= 0 {
		return false
	}
	if screen.W <= 0 || screen.H <= 0 {
		return true
	}
	return b.X >= screen.X && b.Y >= screen.Y && b.X+b.W <= screen.X+screen.W && b.Y+b.H <= screen.Y+screen.H
}

// typeMatches evaluates the portable `type` predicate against a raw role.
func typeMatches(role, t string) bool {
	r, want := strings.ToLower(role), strings.ToLower(strings.TrimSpace(t))
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(r, s) {
				return true
			}
		}
		return false
	}
	base := r
	if i := strings.LastIndexAny(base, "./"); i >= 0 {
		base = base[i+1:]
	}
	switch want {
	case "*":
		return true
	case "button":
		return has("button") && !has("radiobutton")
	case "input", "text_field", "textfield", "text_box", "textbox", "field":
		return has("edittext", "textfield", "securetextfield", "searchfield", "textbox", "textarea", "input")
	case "text", "label":
		return base == "textview" || base == "text" || base == "label" || has("statictext", "checkedtextview")
	case "image":
		return has("image")
	case "cell":
		return has("cell", "listitem", "tablerow")
	case "scrollview":
		return has("scroll", "recyclerview", "listview", "tableview", "collectionview", "table")
	case "switch", "checkbox", "check_box", "toggle":
		return has("switch", "checkbox", "toggle")
	case "slider":
		return has("slider", "seekbar")
	case "link":
		return has("link")
	case "icon":
		return has("icon")
	}
	return r == want || strings.HasSuffix(r, want)
}

func accessibilityMatch(e device.Element, id string) bool {
	if e.ResourceID == id || e.Label == id {
		return true
	}
	if i := strings.LastIndex(e.ResourceID, "/"); i >= 0 && e.ResourceID[i+1:] == id {
		return true
	}
	return false
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// matchBase tests every predicate except near, bounds_hint, parent_of and index.
func (s Selector) matchBase(e device.Element, screen device.Rect) bool {
	t := textOf(e)
	if s.Text != "" && t != s.Text {
		return false
	}
	if s.TextContains != "" && !containsFold(t, s.TextContains) {
		return false
	}
	if s.TextStartsWith != "" && !strings.HasPrefix(t, s.TextStartsWith) {
		return false
	}
	if s.TextRegex != "" {
		re, err := compileRegex(s.TextRegex)
		if err != nil || !re.MatchString(t) {
			return false
		}
	}
	if s.Value != "" && e.Text != s.Value {
		return false
	}
	if s.ValueContains != "" && !containsFold(e.Text, s.ValueContains) {
		return false
	}
	if s.ResourceID != "" && e.ResourceID != s.ResourceID {
		return false
	}
	if s.AccessibilityID != "" && !accessibilityMatch(e, s.AccessibilityID) {
		return false
	}
	if s.Label != "" && e.Label != s.Label {
		return false
	}
	if s.LabelContains != "" && !containsFold(e.Label, s.LabelContains) {
		return false
	}
	if s.Role != "" && e.Role != s.Role {
		return false
	}
	if s.Type != "" && !typeMatches(e.Role, s.Type) {
		return false
	}
	if s.Enabled != nil && e.Enabled != *s.Enabled {
		return false
	}
	if s.Visible != nil && isVisible(e, screen) != *s.Visible {
		return false
	}
	return true
}

type node struct {
	el     device.Element
	parent int
}

func flatten(root device.Element) []node {
	var out []node
	var walk func(e device.Element, parent int)
	walk = func(e device.Element, parent int) {
		idx := len(out)
		out = append(out, node{el: e, parent: parent})
		for _, c := range e.Children {
			walk(c, idx)
		}
	}
	walk(root, -1)
	return out
}

func center(r device.Rect) (float64, float64) {
	return float64(r.X) + float64(r.W)/2, float64(r.Y) + float64(r.H)/2
}

func hintRegion(hint string, screen device.Rect) (device.Rect, bool) {
	w, h := screen.W, screen.H
	hw, hh := w/2, h/2
	x, y := screen.X, screen.Y
	switch hint {
	case "top_half":
		return device.Rect{X: x, Y: y, W: w, H: hh}, true
	case "bottom_half":
		return device.Rect{X: x, Y: y + hh, W: w, H: h - hh}, true
	case "left_half":
		return device.Rect{X: x, Y: y, W: hw, H: h}, true
	case "right_half":
		return device.Rect{X: x + hw, Y: y, W: w - hw, H: h}, true
	case "center":
		return device.Rect{X: x + w/4, Y: y + h/4, W: hw, H: hh}, true
	case "top_left":
		return device.Rect{X: x, Y: y, W: hw, H: hh}, true
	case "top_right":
		return device.Rect{X: x + hw, Y: y, W: w - hw, H: hh}, true
	case "bottom_left":
		return device.Rect{X: x, Y: y + hh, W: hw, H: h - hh}, true
	case "bottom_right":
		return device.Rect{X: x + hw, Y: y + hh, W: w - hw, H: h - hh}, true
	}
	return device.Rect{}, false
}

// resolve returns the indices (into nodes) of every element satisfying sel,
// ordered: by distance from the near anchor when near is set, by distance to
// the bounds_hint region centre when that is set, else depth-first order.
// Index is not applied.
func resolve(nodes []node, screen device.Rect, sel Selector) ([]int, error) {
	if err := sel.check(); err != nil {
		return nil, err
	}
	var idx []int
	for i, n := range nodes {
		if sel.matchBase(n.el, screen) {
			idx = append(idx, i)
		}
	}
	if sel.ParentOf != nil {
		kids, err := resolve(nodes, screen, *sel.ParentOf)
		if err != nil {
			return nil, err
		}
		base := map[int]bool{}
		for _, i := range idx {
			base[i] = true
		}
		chosen := map[int]bool{}
		for _, k := range kids {
			for p := nodes[k].parent; p >= 0; p = nodes[p].parent {
				if base[p] {
					chosen[p] = true
					break
				}
			}
		}
		var f []int
		for _, i := range idx {
			if chosen[i] {
				f = append(f, i)
			}
		}
		idx = f
	}
	switch {
	case sel.Near != nil:
		anchors, err := resolve(nodes, screen, sel.Near.Selector)
		if err != nil {
			return nil, err
		}
		ai := sel.Near.Selector.Index
		if ai < 0 || ai >= len(anchors) {
			return nil, nil
		}
		anchor := anchors[ai]
		ax, ay := center(nodes[anchor].el.Bounds)
		type cand struct {
			i int
			d float64
		}
		var cs []cand
		for _, i := range idx {
			if i == anchor {
				continue
			}
			cx, cy := center(nodes[i].el.Bounds)
			switch sel.Near.Direction {
			case "above":
				if cy >= ay {
					continue
				}
			case "below":
				if cy <= ay {
					continue
				}
			case "left":
				if cx >= ax {
					continue
				}
			case "right":
				if cx <= ax {
					continue
				}
			}
			d := math.Hypot(cx-ax, cy-ay)
			if sel.Near.MaxDistance > 0 && d > sel.Near.MaxDistance {
				continue
			}
			cs = append(cs, cand{i, d})
		}
		sort.SliceStable(cs, func(a, b int) bool { return cs[a].d < cs[b].d })
		idx = idx[:0:0]
		for _, c := range cs {
			idx = append(idx, c.i)
		}
	case sel.BoundsHint != "":
		reg, _ := hintRegion(sel.BoundsHint, screen)
		rx, ry := center(reg)
		sort.SliceStable(idx, func(a, b int) bool {
			ax, ay := center(nodes[idx[a]].el.Bounds)
			bx, by := center(nodes[idx[b]].el.Bounds)
			return math.Hypot(ax-rx, ay-ry) < math.Hypot(bx-rx, by-ry)
		})
	}
	return idx, nil
}

// pick applies sel.Index to an ordered match list.
func pick(idx []int, sel Selector) (int, bool) {
	if sel.Index < 0 || sel.Index >= len(idx) {
		return 0, false
	}
	return idx[sel.Index], true
}

// countOutermost counts matches, counting nested duplicates (a match inside
// another match) once.
func countOutermost(nodes []node, idx []int) int {
	set := map[int]bool{}
	for _, i := range idx {
		set[i] = true
	}
	n := 0
	for _, i := range idx {
		nested := false
		for p := nodes[i].parent; p >= 0; p = nodes[p].parent {
			if set[p] {
				nested = true
				break
			}
		}
		if !nested {
			n++
		}
	}
	return n
}

// Match searches root depth-first (pre-order) for elements satisfying all
// non-empty predicates of sel (after near / bounds_hint ordering) and returns
// the sel.Index-th (0-based) match. A selector with no predicates, an invalid
// one, or a negative or out-of-range Index yields (zero, false).
func Match(root device.Element, sel Selector) (device.Element, bool) {
	if sel.isEmpty() {
		return device.Element{}, false
	}
	nodes := flatten(root)
	idx, err := resolve(nodes, root.Bounds, sel)
	if err != nil {
		return device.Element{}, false
	}
	i, ok := pick(idx, sel)
	if !ok {
		return device.Element{}, false
	}
	return nodes[i].el, true
}

func describe(s Selector) string {
	b, err := json.Marshal(s)
	if err != nil {
		return "{}"
	}
	return string(b)
}
