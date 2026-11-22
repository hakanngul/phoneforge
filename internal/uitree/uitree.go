// Package uitree renders a device.Element hierarchy as a compact, LLM-friendly
// text list: one line per meaningful node with a stable index, role, label,
// tap-ready center coordinates, size and state flags.
package uitree

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"pocketlab/internal/device"
)

const (
	// DefaultMaxNodes is the node cap when Options.MaxNodes is zero.
	DefaultMaxNodes = 300
	// DefaultMaxChars is the output cap when Options.MaxChars is zero.
	DefaultMaxChars = 20000
	// maxNameLen clips very long labels.
	maxNameLen = 100
)

// Options controls Format.
type Options struct {
	DeviceID         string // shown in the header when set
	Filter           string // case-insensitive substring on text/label/id
	MaxNodes         int    // default DefaultMaxNodes
	MaxChars         int    // default DefaultMaxChars
	IncludeOffscreen bool   // keep nodes outside the screen rectangle
}

// node is one candidate line before indentation/indexing.
type node struct {
	el        device.Element
	name      string
	desc      string
	id        string
	editable  bool
	parent    *node // nearest emitted ancestor
	selected  bool
	depth     int
	order     int
	clickable bool
}

// Format renders root as compact text.
func Format(root device.Element, opt Options) string {
	if opt.MaxNodes <= 0 {
		opt.MaxNodes = DefaultMaxNodes
	}
	if opt.MaxChars <= 0 {
		opt.MaxChars = DefaultMaxChars
	}
	sw, sh := screenSize(root)
	unit := "px"
	if strings.HasPrefix(root.Role, "XCUIElementType") {
		unit = "pt"
	}

	var all []*node
	var walk func(e device.Element, parent *node)
	walk = func(e device.Element, parent *node) {
		cur := parent
		if n := candidate(e, parent, sw, sh, opt.IncludeOffscreen); n != nil {
			n.order = len(all)
			all = append(all, n)
			cur = n
		}
		for _, c := range e.Children {
			walk(c, cur)
		}
	}
	walk(root, nil)

	selected := all
	if f := strings.ToLower(strings.TrimSpace(opt.Filter)); f != "" {
		selected = nil
		for _, n := range all {
			if matches(n, f) {
				n.selected = true
				for a := n.parent; a != nil; a = a.parent {
					if a.clickable {
						a.selected = true
						break
					}
				}
			}
		}
		for _, n := range all {
			if n.selected {
				selected = append(selected, n)
			}
		}
		// Indentation is relative to the retained set.
		for _, n := range selected {
			p := n.parent
			for p != nil && !p.selected {
				p = p.parent
			}
			n.parent = p
		}
	}
	total := len(selected)
	cut := ""
	if total > opt.MaxNodes {
		selected = selected[:opt.MaxNodes]
		cut = fmt.Sprintf("nodes capped at max_nodes=%d", opt.MaxNodes)
	}
	for _, n := range selected {
		n.depth = 0
		if n.parent != nil {
			n.depth = n.parent.depth + 1
		}
	}

	var head strings.Builder
	head.WriteString("# ")
	if opt.DeviceID != "" {
		fmt.Fprintf(&head, "device %s | ", opt.DeviceID)
	}
	fmt.Fprintf(&head, "screen %dx%d %s | %d nodes", sw, sh, unit, total)
	if opt.Filter != "" {
		fmt.Fprintf(&head, " matching %q", opt.Filter)
	}
	head.WriteString(" | x,y = tap center\n")

	var sb strings.Builder
	sb.WriteString(head.String())
	shown := 0
	for i, n := range selected {
		line := formatLine(i+1, n)
		if sb.Len()+len(line) > opt.MaxChars {
			cut = fmt.Sprintf("output exceeded %d chars", opt.MaxChars)
			break
		}
		sb.WriteString(line)
		shown++
	}
	if shown < total {
		if cut == "" {
			cut = "truncated"
		}
		fmt.Fprintf(&sb, "... truncated (%s): showing %d of %d nodes. Use the `filter` argument (e.g. a label or id substring) to narrow the result.\n", cut, shown, total)
	}
	return sb.String()
}

// Truncate clips s to max bytes (on a rune boundary) and appends a note.
func Truncate(s string, max int, hint string) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n... truncated: output was %d chars, limit %d. %s", len(s), max, hint)
}

func formatLine(idx int, n *node) string {
	var sb strings.Builder
	sb.WriteString(strings.Repeat("  ", n.depth))
	fmt.Fprintf(&sb, "[%d] %s", idx, ShortRole(n.el.Role))
	if n.name != "" {
		fmt.Fprintf(&sb, " %q", n.name)
	}
	if n.desc != "" {
		fmt.Fprintf(&sb, " desc=%q", n.desc)
	}
	if n.id != "" {
		fmt.Fprintf(&sb, " id=%s", n.id)
	}
	cx, cy := n.el.Bounds.Center()
	fmt.Fprintf(&sb, " @(%d,%d) %dx%d", cx, cy, n.el.Bounds.W, n.el.Bounds.H)
	if n.el.Clickable {
		sb.WriteString(" clickable")
	}
	if n.editable {
		sb.WriteString(" editable")
	}
	if n.el.Focused {
		sb.WriteString(" focused")
	}
	if !n.el.Enabled {
		sb.WriteString(" disabled")
	}
	sb.WriteByte('\n')
	return sb.String()
}

// ShortRole strips platform class prefixes.
func ShortRole(r string) string {
	for _, p := range []string{"XCUIElementType", "android.widget.", "android.view.", "androidx.", "android."} {
		if strings.HasPrefix(r, p) {
			r = strings.TrimPrefix(r, p)
			break
		}
	}
	if i := strings.LastIndex(r, "."); i >= 0 {
		r = r[i+1:]
	}
	if r == "" {
		return "Node"
	}
	return r
}

func isEditable(role string) bool {
	for _, k := range []string{"EditText", "TextField", "SecureTextField", "SearchField", "TextView"} {
		if strings.Contains(role, k) {
			// Android TextView is read-only; iOS TextView is editable.
			if k == "TextView" && !strings.HasPrefix(role, "XCUIElementType") {
				return false
			}
			return true
		}
	}
	return false
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxNameLen {
		r := []rune(s)
		return string(r[:maxNameLen]) + "..."
	}
	return s
}

// candidate returns the line node for e, or nil when e is pruned.
func candidate(e device.Element, parent *node, sw, sh int, offscreen bool) *node {
	b := e.Bounds
	if b.W <= 0 || b.H <= 0 {
		return nil
	}
	if !offscreen && sw > 0 && sh > 0 && (b.X >= sw || b.Y >= sh || b.X+b.W <= 0 || b.Y+b.H <= 0) {
		return nil
	}
	text, label, id := clip(e.Text), clip(e.Label), strings.TrimSpace(e.ResourceID)
	editable := isEditable(e.Role)
	if text == "" && label == "" && id == "" && !e.Clickable && !editable && !e.Focused {
		return nil
	}
	// iOS "Other" containers that only carry an accessibility identifier are
	// layout noise (e.g. label-view); keep them only if interactive or named.
	if text == "" && label == "" && !e.Clickable && !editable && !e.Focused && e.Role == "XCUIElementTypeOther" {
		return nil
	}
	n := &node{el: e, parent: parent, editable: editable, clickable: e.Clickable}
	n.name = text
	if n.name == "" {
		n.name = label
	} else if label != "" && label != text {
		n.desc = label
	}
	if id != n.name && id != n.desc {
		n.id = id
	}
	// A child that just repeats its parent's text adds nothing.
	if parent != nil && n.name != "" && !e.Clickable && !editable && !e.Focused &&
		(n.name == parent.name || n.name == parent.desc) {
		return nil
	}
	return n
}

func matches(n *node, f string) bool {
	for _, s := range []string{n.el.Text, n.el.Label, n.el.ResourceID} {
		if s != "" && strings.Contains(strings.ToLower(s), f) {
			return true
		}
	}
	return false
}

// screenSize derives the screen size from the first node with a real size.
func screenSize(root device.Element) (int, int) {
	q := []device.Element{root}
	for len(q) > 0 {
		e := q[0]
		q = q[1:]
		if e.Bounds.W > 0 && e.Bounds.H > 0 {
			return e.Bounds.X + e.Bounds.W, e.Bounds.Y + e.Bounds.H
		}
		q = append(q, e.Children...)
	}
	return 0, 0
}
