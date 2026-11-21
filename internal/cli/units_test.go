package cli

import (
	"bytes"
	"strings"
	"testing"

	"phoneforge/internal/device"
)

func infos() []device.Info {
	return []device.Info{
		{ID: "android-RF8", Serial: "RF8", Name: "SM-G780G", Model: "SM-G780G", Platform: device.PlatformAndroid, State: "connected"},
		{ID: "ios-0000A", Serial: "0000A", Name: "iPhone 15", Model: "iPhone15,4", Platform: device.PlatformIOS, State: "connected"},
		{ID: "sim-1234", Serial: "1234", Name: "iPhone 17", Model: "iPhone-17", Platform: device.PlatformIOS, Simulator: true, State: "connected"},
	}
}

func TestPickDevice(t *testing.T) {
	cases := []struct {
		want   string
		wantID string
		err    string // substring; "" means success
	}{
		{"android-RF8", "android-RF8", ""},
		{"ANDROID-rf8", "android-RF8", ""},
		{"RF8", "android-RF8", ""},      // exact serial
		{"sm-g", "android-RF8", ""},     // name prefix
		{"iphone 15", "ios-0000A", ""},  // exact-ish via prefix
		{"ios-", "ios-0000A", ""},       // id prefix
		{"sim", "sim-1234", ""},         // id prefix
		{"780", "android-RF8", ""},      // substring
		{"iPhone 17", "sim-1234", ""},   // exact name
		{"iphone", "", "matches 2"},     // ambiguous
		{"nothing", "", "not found"},    // unknown
		{"", "", "3 devices connected"}, // implicit with many
	}
	for _, c := range cases {
		d, err := pickDevice(infos(), c.want)
		if c.err == "" {
			if err != nil || d.ID != c.wantID {
				t.Errorf("%q: got %v %v, want %s", c.want, d.ID, err, c.wantID)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%q: err=%v, want substring %q", c.want, err, c.err)
		}
	}
	// implicit single device; offline devices do not count
	one := []device.Info{{ID: "a", State: "connected"}, {ID: "b", State: "offline"}}
	if d, err := pickDevice(one, ""); err != nil || d.ID != "a" {
		t.Errorf("single: %v %v", d, err)
	}
	if _, err := pickDevice(nil, ""); err == nil {
		t.Error("no devices should error")
	}
}

func TestFindElementsRanking(t *testing.T) {
	root := device.Element{Bounds: device.Rect{W: 100, H: 100}, Enabled: true, Children: []device.Element{
		{Text: "Open settings now", Bounds: device.Rect{X: 0, Y: 0, W: 10, H: 10}, Enabled: true},
		{Text: "Settings", Bounds: device.Rect{X: 10, Y: 10, W: 10, H: 10}, Enabled: true, Clickable: true},
		{Label: "settings", Bounds: device.Rect{X: 30, Y: 30, W: 10, H: 10}, Enabled: true},
		{Text: "Settings", Bounds: device.Rect{X: 500, Y: 500, W: 10, H: 10}, Enabled: true, Clickable: true}, // offscreen
		{Text: "Settings", Bounds: device.Rect{X: 50, Y: 50, W: 0, H: 0}, Enabled: true},                      // zero size
	}}
	m := findElements(root, elemQuery{Text: "settings"})
	if len(m) != 3 {
		t.Fatalf("got %d matches", len(m))
	}
	if m[0].X != 15 || m[0].Y != 15 { // exact + clickable first
		t.Errorf("best = %+v", m[0])
	}
	if m[1].X != 35 { // exact label, not clickable, before substring
		t.Errorf("second = %+v", m[1])
	}
	if got := findElements(root, elemQuery{Text: "zzz"}); len(got) != 0 {
		t.Error("unexpected match")
	}
	r2 := device.Element{Bounds: device.Rect{W: 10, H: 10}, Children: []device.Element{
		{ResourceID: "com.x:id/ok", Bounds: device.Rect{W: 4, H: 4}, Enabled: true},
	}}
	if len(findElements(r2, elemQuery{ID: "ok"})) != 1 || len(findElements(r2, elemQuery{ID: "com.x:id/ok"})) != 1 || len(findElements(r2, elemQuery{ID: "k"})) != 0 {
		t.Error("id matching wrong")
	}
}

func TestNormalizeServer(t *testing.T) {
	for in, want := range map[string]string{
		"":                           "http://127.0.0.1:8700",
		"127.0.0.1:9000":             "http://127.0.0.1:9000",
		"http://h:1/":                "http://h:1",
		"http://h:1/api/v1":          "http://h:1",
		"https://farm.example.com/x": "https://farm.example.com/x",
	} {
		if got := normalizeServer(in); got != want {
			t.Errorf("normalizeServer(%q)=%q want %q", in, got, want)
		}
	}
}

func TestFormatDevicesAndApps(t *testing.T) {
	var b bytes.Buffer
	formatDevices(&b, infos())
	out := b.String()
	if !strings.HasPrefix(out, "ID") || !strings.Contains(out, "ios simulator") {
		t.Errorf("devices:\n%s", out)
	}
	b.Reset()
	formatDevices(&b, nil)
	if !strings.Contains(b.String(), "no devices") {
		t.Errorf("empty: %q", b.String())
	}
	b.Reset()
	formatApps(&b, []device.AppInfo{{BundleID: "com.a", Name: "A", Version: "1.0", Build: "2"}, {BundleID: "com.b"}})
	out = b.String()
	if !strings.Contains(out, "1.0 (2)") || !strings.Contains(out, "com.b") {
		t.Errorf("apps:\n%s", out)
	}
}

func TestAgentRenderer(t *testing.T) {
	var b bytes.Buffer
	r := &agentRenderer{w: &b}
	r.stdoutLine(`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"tap failed"},{"type":"tool_result","content":"fine"}]}}`)
	r.stdoutLine("plain text from another agent")
	r.stdoutLine(`{"type":"result","result":"nope","is_error":true}`)
	out := b.String()
	if !strings.Contains(out, "! tap failed") || strings.Contains(out, "fine") || !strings.Contains(out, "plain text from another agent") || !strings.Contains(out, "✗ nope") {
		t.Errorf("render:\n%s", out)
	}
	if got := formatToolCall("mcp__phoneforge__launch_app", map[string]any{"bundleId": "com.x", "deviceId": "d"}); got != `launch_app(bundleId="com.x")` {
		t.Errorf("tool call=%q", got)
	}
}
