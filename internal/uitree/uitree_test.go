package uitree

import (
	"fmt"
	"strings"
	"testing"

	"phoneforge/internal/device"
)

func r(x, y, w, h int) device.Rect { return device.Rect{X: x, Y: y, W: w, H: h} }

func el(role string, b device.Rect, kids ...device.Element) device.Element {
	return device.Element{Role: role, Bounds: b, Enabled: true, Children: kids}
}

func iosTree() device.Element {
	btn := el("XCUIElementTypeButton", r(16, 311, 370, 53))
	btn.Label, btn.Text, btn.ResourceID, btn.Clickable = "Ayarlar", "Ayarlar", "com.apple.settings", true
	dup := el("XCUIElementTypeStaticText", r(30, 320, 100, 20))
	dup.Text, dup.Label = "Ayarlar", "Ayarlar"
	btn.Children = []device.Element{dup}

	field := el("XCUIElementTypeSearchField", r(10, 100, 300, 40))
	field.Focused = true
	off := el("XCUIElementTypeButton", r(786, 0, 100, 50))
	off.Label, off.Clickable = "OffPage", true
	zero := el("XCUIElementTypeIcon", r(0, 0, 0, 0))
	zero.Label, zero.Clickable = "ZeroIcon", true
	blank := el("XCUIElementTypeOther", r(0, 0, 393, 100))
	blank.Text = " "
	dis := el("XCUIElementTypeButton", r(10, 500, 100, 40))
	dis.Label, dis.Clickable, dis.Enabled = "Gizli", true, false

	layout := el("XCUIElementTypeOther", r(0, 0, 393, 852), btn, field, off, zero, blank, dis)
	win := el("XCUIElementTypeWindow", r(0, 0, 393, 852), layout)
	app := el("XCUIElementTypeApplication", r(0, 0, 393, 852), win)
	app.Text = " "
	return app
}

func androidTree() device.Element {
	tv := el("android.widget.TextView", r(100, 200, 400, 60))
	tv.Text = "Wi-Fi"
	row := el("android.widget.LinearLayout", r(0, 180, 1080, 120), tv)
	row.Clickable, row.ResourceID = true, "com.android:id/row"
	edit := el("android.widget.EditText", r(50, 400, 900, 100))
	edit.ResourceID = "com.android:id/search"
	plain := el("android.widget.TextView", r(0, 2000, 100, 50))
	plain.Text = "Below"
	under := el("android.widget.TextView", r(0, 3000, 100, 50))
	under.Text = "Offscreen"
	frame := el("android.widget.FrameLayout", r(0, 0, 1080, 2168), row, edit, plain, under)
	return el("hierarchy", r(0, 0, 0, 0), frame)
}

func TestIOSPruneCoordsDedupe(t *testing.T) {
	out := Format(iosTree(), Options{DeviceID: "ios-1"})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "# device ios-1 | screen 393x852 pt | 3 nodes | x,y = tap center" {
		t.Fatalf("header: %q", lines[0])
	}
	want := `[1] Button "Ayarlar" id=com.apple.settings @(201,337) 370x53 clickable`
	if lines[1] != want {
		t.Fatalf("line1:\n got %q\nwant %q", lines[1], want)
	}
	for _, bad := range []string{"OffPage", "ZeroIcon", "StaticText", "Other", "Window", "Application"} {
		if strings.Contains(out, bad) {
			t.Errorf("should be pruned: %s\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "SearchField @(160,120) 300x40 editable focused") {
		t.Errorf("search field line missing:\n%s", out)
	}
	if !strings.Contains(out, `"Gizli" @(60,520) 100x40 clickable disabled`) {
		t.Errorf("disabled flag missing:\n%s", out)
	}
	if strings.Count(out, "\n") != 4 {
		t.Errorf("want header + 3 lines:\n%s", out)
	}
}

func TestIncludeOffscreen(t *testing.T) {
	out := Format(iosTree(), Options{IncludeOffscreen: true})
	if !strings.Contains(out, "OffPage") {
		t.Fatalf("offscreen node missing:\n%s", out)
	}
	if strings.Contains(out, "ZeroIcon") {
		t.Fatalf("zero-size node must stay pruned")
	}
}

func TestAndroidIndentAndPixels(t *testing.T) {
	out := Format(androidTree(), Options{})
	if !strings.Contains(out, "screen 1080x2168 px | 4 nodes") {
		t.Fatalf("header:\n%s", out)
	}
	if !strings.Contains(out, "[1] LinearLayout id=com.android:id/row @(540,240) 1080x120 clickable\n") {
		t.Fatalf("row:\n%s", out)
	}
	if !strings.Contains(out, "\n  [2] TextView \"Wi-Fi\" @(300,230) 400x60\n") {
		t.Fatalf("child should be indented under clickable parent:\n%s", out)
	}
	if !strings.Contains(out, "[3] EditText id=com.android:id/search @(500,450) 900x100 editable\n") {
		t.Fatalf("edit:\n%s", out)
	}
	if strings.Contains(out, "Offscreen") {
		t.Fatalf("offscreen leaked:\n%s", out)
	}
}

func TestFilterKeepsClickableAncestor(t *testing.T) {
	out := Format(androidTree(), Options{Filter: "WI-FI"})
	if !strings.Contains(out, `matching "WI-FI"`) || !strings.Contains(out, "2 nodes") {
		t.Fatalf("header:\n%s", out)
	}
	if !strings.Contains(out, "[1] LinearLayout") || !strings.Contains(out, "  [2] TextView \"Wi-Fi\"") {
		t.Fatalf("ancestor+match expected:\n%s", out)
	}
	if strings.Contains(out, "EditText") {
		t.Fatalf("non-matching leaked:\n%s", out)
	}
	if got := Format(androidTree(), Options{Filter: "search"}); !strings.Contains(got, "EditText") || strings.Contains(got, "LinearLayout") {
		t.Fatalf("id filter:\n%s", got)
	}
}

func TestMaxNodesAndCharTruncation(t *testing.T) {
	var kids []device.Element
	for i := 0; i < 50; i++ {
		b := el("android.widget.Button", r(0, i*10+1, 100, 10))
		b.Text = fmt.Sprintf("Item number %d", i)
		kids = append(kids, b)
	}
	root := el("android.widget.FrameLayout", r(0, 0, 1080, 2168), kids...)
	out := Format(root, Options{MaxNodes: 10})
	if !strings.Contains(out, "50 nodes") || !strings.Contains(out, "showing 10 of 50") || !strings.Contains(out, "`filter`") {
		t.Fatalf("max_nodes note:\n%s", out)
	}
	out = Format(root, Options{MaxChars: 400})
	if len(out) > 400+300 || !strings.Contains(out, "exceeded 400 chars") || !strings.Contains(out, "`filter`") {
		t.Fatalf("char truncation (len %d):\n%s", len(out), out)
	}
}

func TestTruncate(t *testing.T) {
	if Truncate("short", 100, "x") != "short" {
		t.Fatal("short unchanged")
	}
	s := strings.Repeat("ü", 100)
	out := Truncate(s, 51, "hint")
	if !strings.Contains(out, "truncated") || !strings.Contains(out, "hint") {
		t.Fatal(out)
	}
	if !strings.HasPrefix(out, strings.Repeat("ü", 25)) {
		t.Fatal("rune boundary")
	}
}
