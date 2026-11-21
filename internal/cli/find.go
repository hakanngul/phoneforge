package cli

import (
	"strings"

	"phoneforge/internal/device"
)

// elemQuery selects elements of a UI tree.
type elemQuery struct {
	Text string // case-insensitive; matches text or label
	ID   string // resource id: exact, or the part after "/" or ":id/"
}

// elemMatch is one hit with its tap-ready center.
type elemMatch struct {
	El   device.Element
	X, Y int
}

// findElements returns on-screen matches ordered best first: exact matches
// before substring matches, then clickable/enabled ones, then document order.
func findElements(root device.Element, q elemQuery) []elemMatch {
	sw, sh := treeSize(root)
	type scored struct {
		m     elemMatch
		score int
		order int
	}
	var all []scored
	text := strings.ToLower(strings.TrimSpace(q.Text))
	id := strings.TrimSpace(q.ID)
	var walk func(e device.Element)
	walk = func(e device.Element) {
		b := e.Bounds
		onscreen := b.W > 0 && b.H > 0 && (sw <= 0 || sh <= 0 || (b.X < sw && b.Y < sh && b.X+b.W > 0 && b.Y+b.H > 0))
		if onscreen {
			score, ok := 0, false
			if text != "" {
				for _, s := range []string{e.Text, e.Label} {
					ls := strings.ToLower(strings.TrimSpace(s))
					if ls == "" {
						continue
					}
					if ls == text {
						score, ok = 0, true
						break
					}
					if strings.Contains(ls, text) {
						score, ok = 10, true
					}
				}
			}
			if id != "" && idMatches(e.ResourceID, id) {
				score, ok = 0, true
			}
			if ok {
				if !e.Clickable {
					score += 2
				}
				if !e.Enabled {
					score += 4
				}
				x, y := b.Center()
				all = append(all, scored{elemMatch{e, x, y}, score, len(all)})
			}
		}
		for _, c := range e.Children {
			walk(c)
		}
	}
	walk(root)
	// stable selection sort by (score, order)
	out := make([]elemMatch, 0, len(all))
	used := make([]bool, len(all))
	for range all {
		best := -1
		for i, s := range all {
			if used[i] {
				continue
			}
			if best < 0 || s.score < all[best].score {
				best = i
			}
		}
		used[best] = true
		out = append(out, all[best].m)
	}
	return out
}

func idMatches(resourceID, want string) bool {
	rid := strings.TrimSpace(resourceID)
	if rid == "" {
		return false
	}
	if rid == want {
		return true
	}
	if i := strings.LastIndex(rid, "/"); i >= 0 && rid[i+1:] == want {
		return true
	}
	return false
}

// treeSize derives the screen size from the first node with a real size.
func treeSize(root device.Element) (int, int) {
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
