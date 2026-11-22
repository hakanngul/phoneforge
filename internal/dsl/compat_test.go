package dsl

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"pocketlab/internal/device"
)

func run(t *testing.T, dev device.Device, script string) Result {
	t.Helper()
	res, err := Execute(context.Background(), dev, []byte(script))
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, script)
	}
	return res
}

// capDevice adds the dsl-level optional capabilities on top of extraDevice.
type capDevice struct {
	*extraDevice
}

func (c *capDevice) TwoFingerTap(_ context.Context, x, y int) error {
	return c.rec(fmt.Sprintf("2tap %d,%d", x, y))
}
func (c *capDevice) SetLocation(_ context.Context, la, lo float64) error {
	return c.rec(fmt.Sprintf("loc %.1f,%.1f", la, lo))
}
func (c *capDevice) ResetLocation(context.Context) error { return c.rec("loc reset") }
func (c *capDevice) Biometrics(_ context.Context, s, sensor string) error {
	return c.rec("bio " + s + " " + sensor)
}
func (c *capDevice) Fold(_ context.Context, s string, a *float64) error {
	return c.rec("fold " + s)
}
func (c *capDevice) Siri(_ context.Context, p string) (string, error) {
	c.rec("siri " + p)
	return "reply", nil
}
func (c *capDevice) SelectPickerValue(_ context.Context, x, y int, v string) error {
	return c.rec(fmt.Sprintf("pick %d,%d %s", x, y, v))
}
func (c *capDevice) DragPath(_ context.Context, pts []PathPoint) error {
	return c.rec(fmt.Sprintf("path %d", len(pts)))
}
func (c *capDevice) GetClipboard(context.Context) (string, error) { return "clip!", nil }

func newCap() *capDevice { return &capDevice{&extraDevice{fakeDevice: newFake()}} }

func TestActionAliasTable(t *testing.T) {
	cases := map[string]string{
		"open_app": "launch_app", "launch_app": "launch_app", "start_app": "launch_app", "Open-App": "launch_app",
		"kill_app": "stop_app", "terminate_app": "stop_app", "close_app": "stop_app", "force_stop": "stop_app", "stop_app": "stop_app",
		"open_link": "open_url", "openurl": "open_url", "deeplink": "open_url", "open_deeplink": "open_url", "open_deep_link": "open_url",
		"tap_on": "tap", "click": "tap", "touch": "tap", "double_tap_on": "double_tap",
		"long_press_on": "long_press", "long_tap": "long_press",
		"input_text": "type", "type_text": "type", "enter_text": "type", "input": "type", "set_text": "type",
		"type_password": "type_secret", "input_secret": "type_secret", "type_credential": "type_secret",
		"clear_text": "clear", "clear_field": "clear", "erase_text": "clear", "erase": "clear",
		"hide_keyboard": "dismiss_keyboard", "copy_text_from": "copy_text", "paste": "paste_text", "key_press": "press_key",
		"pick": "select_picker", "set_picker": "select_picker", "picker_select": "select_picker", "select_wheel": "select_picker",
		"take_screenshot": "screenshot", "capture_screenshot": "screenshot",
		"extended_wait_until": "wait_for", "wait_for_element": "wait_for", "wait_until_visible": "wait_for",
		"delay": "wait", "sleep": "wait", "pause": "wait",
		"biometric": "biometrics", "face_id": "biometrics", "touch_id": "biometrics", "fingerprint": "biometrics",
		"set_fold": "fold", "fold_device": "fold", "w3c_actions": "perform_actions", "perform_gesture": "perform_actions",
		"eval": "eval_script", "script": "run_script",
		"assert": "assert_exists", "assert_visible": "assert_exists", "assert_present": "assert_exists",
		"assert_not_visible": "assert_not_exists", "assert_absent": "assert_not_exists", "assert_hidden": "assert_not_exists",
		"two_finger_tap": "two_finger_tap", "pinch": "pinch", "scroll": "scroll", "drag_path": "drag_path", "toggle": "toggle",
		"navigate": "navigate", "observe": "observe", "audit": "audit", "record_start": "record_start", "record_stop": "record_stop",
		"checkpoint": "checkpoint", "set_location": "set_location", "reset_location": "reset_location", "set_permissions": "set_permissions",
		"siri": "siri", "metrics_start": "metrics_start", "metrics_stop": "metrics_stop", "extract": "extract",
		"request_user_input": "request_user_input", "select_web_context": "select_web_context", "execute_js": "execute_js",
		"assert_count": "assert_count", "assert_property": "assert_property", "assert_screen_changed": "assert_screen_changed",
		"if_exists": "if_exists", "repeat": "repeat", "wait_stable": "wait_stable", "type_secret": "type_secret",
	}
	for in, want := range cases {
		got, ok := CanonicalAction(in)
		if !ok || got != want {
			t.Errorf("CanonicalAction(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	_, err := Parse([]byte(`{"steps":[{"action":"teleport"}]}`))
	if err == nil || !strings.Contains(err.Error(), `"teleport"`) {
		t.Fatalf("unknown action error = %v", err)
	}
	_, err = Parse([]byte(`{"steps":[{"action":"if_exists","predicate":{"text":"x"},"then":[{"action":"nope"}]}]}`))
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("nested unknown action error = %v", err)
	}
}

func TestAliasExecution(t *testing.T) {
	tests := []struct {
		name   string
		step   string
		want   string // joined calls
		mutate func(c *capDevice)
	}{
		{"open_app", `{"action":"open_app","bundle_id":"com.x"}`, "", nil},
		{"open_link", `{"action":"open_link","url":"myapp://p/1"}`, "url myapp://p/1", nil},
		{"deeplink", `{"action":"deeplink","url":"u://x"}`, "url u://x", nil},
		{"click predicate", `{"action":"click","predicate":{"text":"Welcome back"}}`, "", nil},
		{"double_tap_on", `{"action":"double_tap_on","predicate":{"text":"Welcome back"}}`, "dt 200,25", nil},
		{"two_finger_tap", `{"action":"two_finger_tap","coords":{"x":10,"y":20}}`, "2tap 10,20", nil},
		{"pinch scale", `{"action":"pinch","coords":[300,400],"scale":0.5}`, "", nil},
		{"hide_keyboard", `{"action":"hide_keyboard"}`, "kb", nil},
		{"paste", `{"action":"paste"}`, "", nil},
		{"set_location", `{"action":"set_location","lat":37.7,"lon":-122.4}`, "loc 37.7,-122.4", nil},
		{"reset_location", `{"action":"reset_location"}`, "loc reset", nil},
		{"biometrics fail", `{"action":"biometrics","state":"fail","sensor":"FaceID"}`, "bio nomatch faceid", nil},
		{"fold", `{"action":"fold","state":"closed"}`, "fold closed", nil},
		{"siri", `{"action":"siri","prompt":"hi"}`, "siri hi", nil},
		{"select_picker", `{"action":"select_picker","coords":{"x":1,"y":2},"value":"May"}`, "pick 1,2 May", nil},
		{"drag_path", `{"action":"drag_path","points":[{"x":1,"y":1},{"x":5,"y":5,"duration_ms":100}]}`, "path 2", nil},
		{"set_permissions", `{"action":"set_permissions","bundle_id":"com.x","permissions":{"camera":"allow","all":"deny"}}`,
			"perm com.x all false|perm com.x camera true", nil},
		{"drag from/to_element", `{"action":"drag","from":{"predicate":{"text":"Welcome back"}},"to_element":{"predicate":{"text":"Login","index":1}}}`,
			"drag 200,25>200,650 500", nil},
		{"drag from_coords", `{"action":"drag","from_coords":[1,2],"to_coords":{"x":3,"y":4},"duration_ms":50}`, "drag 1,2>3,4 50", nil},
		{"navigate url", `{"action":"navigate","target":"https://e.com"}`, "url https://e.com", nil},
		{"copy_text clipboard", `{"action":"copy_text","predicate":{"text":"Welcome back"},"store_as":"w"}`, "clip Welcome back", nil},
		{"set_clipboard", `{"action":"set_clipboard","text":"hi"}`, "clip hi", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newCap()
			res := run(t, c, hdr+`[`+tc.step+`]}`)
			if !res.Passed {
				t.Fatalf("failed: %+v", res.Steps)
			}
			if got := strings.Join(c.calls, "|"); got != tc.want {
				t.Fatalf("calls = %q want %q", got, tc.want)
			}
		})
	}
}

func TestAliasSideEffects(t *testing.T) {
	c := newCap()
	res := run(t, c, hdr+`[
		{"action":"click","predicate":{"text":"Welcome back"}},
		{"action":"long_tap","predicate":{"text":"Welcome back"},"duration_ms":900},
		{"action":"input_text","text":"hello","predicate":{"text":"Welcome back"},"clear_first":true,"dismiss_keyboard":true},
		{"action":"key_press","key":"ENTER"},
		{"action":"navigate","target":"back"},
		{"action":"paste_text"},
		{"action":"take_screenshot"},
		{"action":"kill_app","bundle_id":"com.x"},
		{"action":"delay","duration_ms":1},
		{"action":"open_app","bundle_id":"com.x","fresh":true}]}`)
	if !res.Passed {
		t.Fatalf("%+v", res.Steps)
	}
	f := c.fakeDevice
	if len(f.taps) != 3 { // click, long_tap, type's focusing tap
		t.Fatalf("taps = %v", f.taps)
	}
	if got := strings.Join(f.typed, ","); got != "hello,clip!" {
		t.Fatalf("typed = %q", got)
	}
	// clear_first presses delete len("Welcome back")+1 times, then enter, back
	keys := f.keys
	dels := 0
	for _, k := range keys {
		if k == device.KeyDelete {
			dels++
		}
	}
	if dels != 13 || keys[len(keys)-2] != "enter" || keys[len(keys)-1] != "back" {
		t.Fatalf("keys = %v", keys)
	}
	if len(f.launch) != 1 || !strings.Contains(strings.Join(c.calls, "|"), "kb") {
		t.Fatalf("launch=%v calls=%v", f.launch, c.calls)
	}
}

func TestUnsupportedActionsFailClearly(t *testing.T) {
	for _, step := range []string{
		`{"action":"two_finger_tap","coords":[1,2]}`, `{"action":"drag_path","points":[{"x":1,"y":1},{"x":2,"y":2,"duration_ms":1}]}`,
		`{"action":"select_picker","coords":[1,2],"value":"x"}`, `{"action":"set_location","lat":1,"lon":2}`,
		`{"action":"biometrics","state":"match"}`, `{"action":"fold","state":"open"}`, `{"action":"siri","prompt":"x"}`,
		`{"action":"audit"}`, `{"action":"record_start"}`, `{"action":"metrics_start"}`, `{"action":"eval_script","script":"1"}`,
		`{"action":"run_script","script_path":"a.js"}`, `{"action":"perform_actions","actions":[]}`,
		`{"action":"toggle","predicate":{"text":"Welcome back"},"state":"on"}`,
	} {
		res := run(t, newFake(), hdr+`[`+step+`]}`)
		if res.Passed || !strings.Contains(res.Steps[0].Error, "operation not supported") {
			t.Errorf("%s: %+v", step, res.Steps[0])
		}
	}
	for _, step := range []string{`{"action":"request_user_input"}`, `{"action":"set_location","lat":0,"lon":0}`,
		`{"action":"biometrics","state":"x"}`, `{"action":"fold"}`, `{"action":"drag_path","points":[{"x":1,"y":1}]}`} {
		if res := run(t, newCap(), hdr+`[`+step+`]}`); res.Passed {
			t.Errorf("%s should fail", step)
		}
	}
}

func TestWebContextRejected(t *testing.T) {
	for _, step := range []string{
		`{"action":"tap","context":"web","predicate":{"css_selector":"#a"}}`,
		`{"action":"execute_js","script":"1"}`, `{"action":"select_web_context","url_contains":"x"}`,
		`{"action":"tap","predicate":{"css_selector":"#a"}}`, `{"action":"assert_exists","predicate":{"css_selector":"#a"}}`,
		`{"action":"wait_for","predicate":{"css_selector":"#a"},"timeout_ms":5}`,
	} {
		res := run(t, newFake(), hdr+`[`+step+`]}`)
		s := res.Steps[0]
		if res.Passed || !strings.Contains(s.Error, "web context not supported yet") || s.Code != CodeContextNotAvail {
			t.Errorf("%s: %+v", step, s)
		}
	}
}

func TestKeySpellingsAndParams(t *testing.T) {
	f := newFake()
	res := run(t, f, `{"version":"0.2","mode":"deterministic","params":{"who":"Login","n":3,"sec":{"default":"x"}},
		"defaults":{"timeout_ms":5000,"settleMs":0},"steps":[
		{"action":"Tap On","predicate":{"text":"${who}"},"onFail":"skip","id":"s1"},
		{"action":"tap","coords":{"x_percent":50,"y_percent":10}},
		{"action":"type","text":"${who}-${n}-${sec}"}]}`)
	if !res.Passed {
		t.Fatalf("%+v", res.Steps)
	}
	if res.Steps[0].ID != "s1" || f.taps[0] != (tap{200, 250}) || f.taps[1] != (tap{540, 192}) {
		t.Fatalf("taps=%v id=%q", f.taps, res.Steps[0].ID)
	}
	if f.typed[0] != "Login-3-x" {
		t.Fatalf("typed = %v", f.typed)
	}
	// overrides and unknown variables
	s, _ := Parse([]byte(`{"params":{"a":"1"},"steps":[{"action":"type","text":"${a}${b}"}]}`))
	f2 := newFake()
	r2, err := ExecuteScript(context.Background(), f2, s, Options{Params: map[string]string{"a": "Z", "b": "Y"}})
	if err != nil || !r2.Passed || f2.typed[0] != "ZY" {
		t.Fatalf("%v %+v %v", err, r2, f2.typed)
	}
	r3 := run(t, newFake(), `{"steps":[{"action":"type","text":"${missing}"}]}`)
	if r3.Passed || !strings.Contains(r3.Steps[0].Error, "unknown variable: ${missing}") {
		t.Fatalf("%+v", r3.Steps[0])
	}
	if _, err := Execute(context.Background(), newFake(), []byte(`{"params":{"req":null},"steps":[]}`)); err == nil {
		t.Fatal("param without value accepted")
	}
	for _, bad := range []string{`{"alerts":"maybe","steps":[]}`, `{"mode":"x","steps":[]}`,
		`{"steps":[{"action":"tap","on_fail":"explode"}]}`, `{"steps":[{"action":"tap","selector":{"nokey":1}}]}`,
		`{"steps":[{"action":"tap","predicate":{"text_regex":"("}}]}`, `{"steps":[{"action":"tap","selector":{},"predicate":{}}]}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func predTree() device.Element {
	r := func(x, y, w, h int) device.Rect { return device.Rect{X: x, Y: y, W: w, H: h} }
	return device.Element{Role: "FrameLayout", Bounds: r(0, 0, 1000, 2000), Children: []device.Element{
		{Role: "android.widget.TextView", Text: "Email", Bounds: r(100, 100, 200, 50)},
		{Role: "android.widget.EditText", ResourceID: "com.x:id/email", Enabled: true, Bounds: r(100, 160, 600, 80)},
		{Role: "android.widget.TextView", Text: "Password", Bounds: r(100, 400, 200, 50)},
		{Role: "android.widget.EditText", ResourceID: "com.x:id/pass", Bounds: r(100, 460, 600, 80)},
		{Role: "android.widget.Button", Text: "Save", Enabled: true, Bounds: r(100, 1800, 300, 100)},
		{Role: "android.widget.Button", Text: "Save draft", Bounds: r(500, 1800, 300, 100)},
		{Role: "androidx.cardview.widget.CardView", ResourceID: "com.x:id/row", Bounds: r(0, 1000, 1000, 200), Children: []device.Element{
			{Role: "android.widget.TextView", Text: "Delete", Bounds: r(10, 1010, 100, 50)},
			{Role: "android.widget.Button", Label: "Edit", Bounds: r(800, 1010, 100, 50)},
		}},
		{Role: "XCUIElementTypeStaticText", Label: "Hello World", Bounds: r(0, 600, 200, 50)},
		{Role: "android.widget.TextView", Text: "Far", Bounds: r(0, 3000, 100, 50)},
	}}
}

func TestPredicates(t *testing.T) {
	tests := []struct {
		name string
		sel  string
		want string // text/label/resourceId of the match, "" = no match
	}{
		{"text exact", `{"text":"Save"}`, "Save"},
		{"text falls back to label", `{"text":"Edit"}`, "Edit"},
		{"text_contains case-insens", `{"text_contains":"sAvE"}`, "Save"},
		{"text_contains index", `{"text_contains":"save","index":1}`, "Save draft"},
		{"text_starts_with", `{"text_starts_with":"Save d"}`, "Save draft"},
		{"text_regex", `{"text_regex":"^Save\\s\\w+$"}`, "Save draft"},
		{"regex alias", `{"regex":"^Pass"}`, "Password"},
		{"regex case flag", `{"text_regex":"(?i)^password$"}`, "Password"},
		{"accessibility_id resource suffix", `{"accessibility_id":"email"}`, "com.x:id/email"},
		{"accessibility_id label", `{"accessibility_id":"Edit"}`, "Edit"},
		{"accessibility_id full", `{"accessibility_id":"com.x:id/pass"}`, "com.x:id/pass"},
		{"type input", `{"type":"input"}`, "com.x:id/email"},
		{"type textfield synonym", `{"type":"text_field","index":1}`, "com.x:id/pass"},
		{"type button", `{"type":"button"}`, "Save"},
		{"type text", `{"type":"text","text_contains":"word"}`, "Password"},
		{"type raw suffix", `{"type":"StaticText"}`, "Hello World"},
		{"type star", `{"type":"*","text":"Far"}`, "Far"},
		{"enabled", `{"type":"button","enabled":true}`, "Save"},
		{"enabled false", `{"type":"button","enabled":false,"index":0}`, "Save draft"},
		{"visible false", `{"visible":false,"text_contains":"Far"}`, "Far"},
		{"visible true excludes offscreen", `{"visible":true,"text":"Far"}`, ""},
		{"value", `{"value":"Email"}`, "Email"},
		{"value_contains", `{"value_contains":"mai"}`, "Email"},
		{"label_contains", `{"label_contains":"hello"}`, "Hello World"},
		{"near below", `{"type":"input","near":{"text":"Password","direction":"below"}}`, "com.x:id/pass"},
		{"near above", `{"type":"input","near":{"text":"Password","direction":"above"}}`, "com.x:id/email"},
		{"near closest", `{"type":"button","near":{"text":"Delete"}}`, "Edit"},
		{"near index from closest", `{"type":"button","near":{"text":"Delete"},"index":1}`, "Save"},
		{"near max_distance miss", `{"type":"button","near":{"text":"Delete","max_distance":10}}`, ""},
		{"near camelCase", `{"type":"input","near":{"textContains":"pass","direction":"any","maxDistance":300}}`, "com.x:id/pass"},
		{"near no anchor", `{"type":"button","near":{"text":"Nope"}}`, ""},
		{"bounds_hint bottom_right", `{"type":"button","bounds_hint":"bottom_right"}`, "Save draft"},
		{"bounds_hint bottom_left", `{"type":"button","bounds_hint":"bottom_left"}`, "Save"},

		{"parent_of", `{"type":"*","parent_of":{"text":"Delete"},"resource_id":"com.x:id/row"}`, "com.x:id/row"},
		{"parent_of nearest container", `{"role":"androidx.cardview.widget.CardView","parent_of":{"label":"Edit"}}`, "com.x:id/row"},
		{"index out of range", `{"type":"button","index":9}`, ""},
		{"negative index", `{"type":"button","index":-1}`, ""},
		{"all keys must match", `{"text":"Save","type":"input"}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sel Selector
			if err := jsonUnmarshal(tc.sel, &sel); err != nil {
				t.Fatal(err)
			}
			el, ok := Match(predTree(), sel)
			got := el.Text
			if got == "" {
				got = el.Label
			}
			if el.ResourceID != "" && got == "" {
				got = el.ResourceID
			}
			if el.Text == "" && el.Label == "" {
				got = el.ResourceID
			}
			if tc.want == "" && ok {
				t.Fatalf("expected no match, got %q", got)
			}
			if tc.want != "" && (!ok || got != tc.want) {
				t.Fatalf("got %q (ok=%v) want %q", got, ok, tc.want)
			}
		})
	}
	// selected is not representable, css_selector is web only.
	f := newFake()
	f.tree = predTree()
	for _, sel := range []string{`{"selected":true}`, `{"css_selector":"a"}`} {
		res := run(t, f, hdr+`[{"action":"tap","predicate":`+sel+`}]}`)
		if res.Passed {
			t.Errorf("%s should fail", sel)
		}
	}
}

func TestPredicateJSONErrors(t *testing.T) {
	var s Selector
	if err := jsonUnmarshal(`{"text":"a","bogus":1}`, &s); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("err = %v", err)
	}
	if err := jsonUnmarshal(`{"near":{"text":"a","direction":"sideways"}}`, &s); err == nil {
		t.Fatal("bad direction accepted")
	}
}

func TestIfExists(t *testing.T) {
	f := newFake()
	res := run(t, f, hdr+`[
		{"action":"if_exists","predicate":{"text":"Welcome back"},
			"then":[{"action":"tap","x":1,"y":1},{"action":"tap","x":2,"y":2}],
			"else":[{"action":"tap","x":9,"y":9}]},
		{"action":"if_exists","predicate":{"text":"Nope"},
			"then":[{"action":"tap","x":3,"y":3}],
			"else":[{"action":"tap","x":4,"y":4}]},
		{"action":"if_exists","predicate":{"text":"Nope"},"then":[{"action":"tap","x":5,"y":5}]},
		{"action":"if_exists","predicate":{"text":"Welcome back"},
			"then":[{"action":"assert_visible","predicate":{"text":"Nope"},"on_fail":"skip"},{"action":"tap","x":6,"y":6}]}]}`)
	if !res.Passed {
		t.Fatalf("%+v", res.Steps)
	}
	want := []tap{{1, 1}, {2, 2}, {4, 4}}
	if len(f.taps) != len(want) {
		t.Fatalf("taps = %v", f.taps)
	}
	for i, w := range want {
		if f.taps[i] != w {
			t.Fatalf("taps = %v", f.taps)
		}
	}
	if c := res.Steps[0].ConditionMet; c == nil || !*c || len(res.Steps[0].ThenResults) != 2 {
		t.Fatalf("%+v", res.Steps[0])
	}
	if c := res.Steps[1].ConditionMet; c == nil || *c || len(res.Steps[1].ElseResults) != 1 {
		t.Fatalf("%+v", res.Steps[1])
	}
	// inner failure: step passes, inner on_fail ignored, later inner steps skipped
	s3 := res.Steps[3]
	if s3.Status != StatusOK || s3.ThenResults[0].Status != StatusFailed || s3.ThenResults[1].Status != StatusSkipped {
		t.Fatalf("%+v", s3)
	}
}

func TestRepeat(t *testing.T) {
	// times with ${} and repeat_index
	f := newFake()
	res := run(t, f, `{"params":{"n":"3"},"defaults":{"settleMs":0},"steps":[
		{"action":"repeat","times":"${n}","body":[{"action":"type","text":"i${repeat_index}"}]},
		{"action":"repeat","times":2,"body":[{"action":"tap","x":1,"y":1}]}]}`)
	if !res.Passed || strings.Join(f.typed, ",") != "i0,i1,i2" || len(f.taps) != 2 {
		t.Fatalf("%+v typed=%v taps=%v", res.Steps, f.typed, f.taps)
	}
	if it := res.Steps[0].Iterations; it == nil || *it != 3 || len(res.Steps[0].BodyResults) != 3 {
		t.Fatalf("%+v", res.Steps[0])
	}

	// until: tree changes after 2 swipes
	g := newFake()
	g.onSwipe = func(n int) {
		if n == 2 {
			g.tree.Children = append(g.tree.Children, device.Element{Role: "Button", Text: "Done", Bounds: device.Rect{W: 5, H: 5}})
		}
	}
	res = run(t, g, hdr+`[{"action":"repeat","until":{"text":"Done","type":"button"},"max_iterations":10,
		"body":[{"action":"swipe","direction":"up"}]}]}`)
	if !res.Passed || len(g.swipes) != 2 {
		t.Fatalf("%+v swipes=%d", res.Steps, len(g.swipes))
	}
	// until never met -> failure at the cap
	res = run(t, newFake(), hdr+`[{"action":"repeat","until":{"text":"Never"},"max_iterations":3,"body":[{"action":"wait","ms":1}]}]}`)
	if res.Passed || res.Steps[0].Code != CodeTimeout || *res.Steps[0].Iterations != 3 {
		t.Fatalf("%+v", res.Steps[0])
	}
	// while: loop while present; max caps it silently
	res = run(t, newFake(), hdr+`[{"action":"repeat","while":{"text":"Welcome back"},"max_iterations":4,"body":[{"action":"wait","ms":1}]}]}`)
	if !res.Passed || *res.Steps[0].Iterations != 4 {
		t.Fatalf("%+v", res.Steps[0])
	}
	res = run(t, newFake(), hdr+`[{"action":"repeat","while":{"text":"Nope"},"body":[{"action":"tap","x":1,"y":1}]}]}`)
	if !res.Passed || *res.Steps[0].Iterations != 0 {
		t.Fatalf("%+v", res.Steps[0])
	}
	// failing body step fails the repeat and stops the script
	h := newFake()
	res = run(t, h, hdr+`[{"action":"repeat","times":5,"body":[{"action":"tap","x":1,"y":1},{"action":"assert_visible","predicate":{"text":"Nope"}}]},{"action":"tap","x":2,"y":2}]}`)
	if res.Passed || res.Steps[0].Status != StatusFailed || len(h.taps) != 1 || res.Steps[1].Status != StatusSkipped {
		t.Fatalf("%+v taps=%v", res.Steps, h.taps)
	}
	// validation
	for _, step := range []string{`{"action":"repeat","body":[{"action":"wait","ms":1}]}`,
		`{"action":"repeat","times":1,"until":{"text":"a"},"body":[{"action":"wait","ms":1}]}`,
		`{"action":"repeat","times":"abc","body":[{"action":"wait","ms":1}]}`, `{"action":"repeat","times":2}`,
		`{"action":"repeat","condition":"vars.a","body":[{"action":"wait","ms":1}]}`} {
		if r := run(t, newFake(), hdr+`[`+step+`]}`); r.Passed {
			t.Errorf("%s should fail", step)
		}
	}
}

type countingTree struct {
	*fakeDevice
	n       *int
	appear  int
	elem    device.Element
	vanish  int // element present until this many calls
	present bool
}

func (c *countingTree) UITree(context.Context) (device.Element, error) {
	*c.n++
	t := c.fakeDevice.tree
	show := false
	if c.present {
		show = *c.n < c.vanish
	} else {
		show = *c.n >= c.appear
	}
	if show {
		t.Children = append(append([]device.Element{}, t.Children...), c.elem)
	}
	return t, nil
}

func TestAssertions(t *testing.T) {
	fastTimers(t)
	// exists: single check, then polling with timeout_ms
	res := run(t, newFake(), hdr+`[{"action":"assert_exists","predicate":{"text":"Nope"},"message":"custom msg"}]}`)
	if res.Passed || res.Steps[0].Error != "custom msg" || res.Steps[0].Code != CodeAssertionFailed {
		t.Fatalf("%+v", res.Steps[0])
	}
	n := 0
	later := &countingTree{fakeDevice: newFake(), n: &n, appear: 3, elem: device.Element{Role: "TextView", Text: "Later", Bounds: device.Rect{W: 5, H: 5}}}
	if r := run(t, later, hdr+`[{"action":"assert_exists","predicate":{"text":"Later"},"timeout_ms":2000}]}`); !r.Passed || n < 3 {
		t.Fatalf("%+v n=%d", r.Steps, n)
	}
	n = 0
	later2 := &countingTree{fakeDevice: newFake(), n: &n, appear: 3, elem: device.Element{Role: "TextView", Text: "Later", Bounds: device.Rect{W: 5, H: 5}}}
	if r := run(t, later2, hdr+`[{"action":"assert_exists","predicate":{"text":"Later"}}]}`); r.Passed {
		t.Fatal("no timeout must check once")
	}
	// not_exists waits for disappearance
	n = 0
	gone := &countingTree{fakeDevice: newFake(), n: &n, present: true, vanish: 3, elem: device.Element{Role: "TextView", Text: "Spinner", Bounds: device.Rect{W: 5, H: 5}}}
	if r := run(t, gone, hdr+`[{"action":"assert_not_exists","predicate":{"text":"Spinner"},"timeout_ms":2000}]}`); !r.Passed {
		t.Fatalf("%+v", r.Steps)
	}
	for _, step := range []string{`{"action":"assert_absent","predicate":{"text":"Login"}}`} {
		if r := run(t, newFake(), hdr+`[`+step+`]}`); r.Passed || !strings.Contains(r.Steps[0].Error, "unexpectedly visible") {
			t.Fatalf("%+v", r.Steps[0])
		}
	}

	// count (nested duplicates count once) and property
	f := newFake()
	f.tree = predTree()
	f.tree.Children = append(f.tree.Children, device.Element{Role: "Cell", Text: "row", Bounds: device.Rect{W: 1, H: 1},
		Children: []device.Element{{Role: "Cell", Text: "row", Bounds: device.Rect{W: 1, H: 1}}}})
	tests := []struct {
		step string
		pass bool
		msg  string
	}{
		{`{"action":"assert_count","predicate":{"type":"button"},"expected":3}`, true, ""},
		{`{"action":"assert_count","predicate":{"text":"row"},"expected":1}`, true, ""},
		{`{"action":"assert_count","predicate":{"text":"row"},"expected":2}`, false, "expected 2 matches, found 1"},
		{`{"action":"assert_count","predicate":{"text":"nothing"},"expected":0}`, true, ""},
		{`{"action":"assert_count","predicate":{"text":"row"}}`, false, "requires expected"},
		{`{"action":"assert_property","predicate":{"text":"Save"},"property":"enabled","expected_value":true}`, true, ""},
		{`{"action":"assert_property","predicate":{"text":"Save draft"},"property":"enabled","expected_value":"false"}`, true, ""},
		{`{"action":"assert_property","predicate":{"text":"Save draft"},"property":"enabled","expected_value":true}`, false, `is "false"`},
		{`{"action":"assert_property","predicate":{"text":"Email"},"property":"text","expected_value":"Email"}`, true, ""},
		{`{"action":"assert_property","predicate":{"text":"Email"},"property":"visible","expected_value":true}`, true, ""},
		{`{"action":"assert_property","predicate":{"text":"Far"},"property":"visible","expected_value":false}`, true, ""},
		{`{"action":"assert_property","predicate":{"type":"input","bounds_hint":"top_half"},"property":"editable","expected_value":true}`, true, ""},
		{`{"action":"assert_property","predicate":{"type":"button"},"property":"text","expected_value":"x"}`, false, "matched 3 elements"},
		{`{"action":"assert_property","predicate":{"text":"Email"},"property":"selected","expected_value":true}`, false, "no selected state"},
		{`{"action":"assert_property","predicate":{"text":"Email"},"property":"bogus","expected_value":1}`, false, "unknown property"},
	}
	for _, tc := range tests {
		r := run(t, f, hdr+`[`+tc.step+`]}`)
		if r.Passed != tc.pass || (tc.msg != "" && !strings.Contains(r.Steps[0].Error, tc.msg)) {
			t.Errorf("%s -> %+v", tc.step, r.Steps[0])
		}
	}
}

func solidPNG(c color.Color, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func TestAssertScreenChanged(t *testing.T) {
	red, blue := solidPNG(color.RGBA{255, 0, 0, 255}, 40, 40), solidPNG(color.RGBA{0, 0, 255, 255}, 40, 40)
	f := newFake()
	f.shot = device.Screenshot{PNG: red}
	f.onSwipe = func(int) { f.shot = device.Screenshot{PNG: blue} }
	res := run(t, f, hdr+`[{"action":"observe"},{"action":"swipe","direction":"up"},
		{"action":"assert_screen_changed","threshold_percent":90}]}`)
	if !res.Passed || res.Steps[2].Result["screenChangePercent"].(float64) != 100 {
		t.Fatalf("%+v", res.Steps)
	}
	// unchanged screen fails
	g := newFake()
	g.shot = device.Screenshot{PNG: red}
	res = run(t, g, hdr+`[{"action":"observe"},{"action":"assert_screen_changed"}]}`)
	if res.Passed || res.Steps[1].Code != CodeAssertionFailed {
		t.Fatalf("%+v", res.Steps[1])
	}
	// no observe: baseline taken at script start
	h := newFake()
	h.shot = device.Screenshot{PNG: red}
	h.onSwipe = func(int) { h.shot = device.Screenshot{PNG: blue} }
	res = run(t, h, hdr+`[{"action":"swipe","x1":1,"y1":1,"x2":2,"y2":2},{"action":"assert_screen_changed"}]}`)
	if !res.Passed {
		t.Fatalf("%+v", res.Steps)
	}
	// without screenshots the UI tree decides
	k := newFake()
	k.onSwipe = func(int) { k.tree.Children = k.tree.Children[:1] }
	res = run(t, k, hdr+`[{"action":"observe"},{"action":"swipe","direction":"up"},{"action":"assert_screen_changed","threshold_percent":10}]}`)
	if !res.Passed {
		t.Fatalf("%+v", res.Steps)
	}
}

func TestFailureStrategies(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		status   string // status of step 0
		passed   bool
		next     string // status of step 1
		topState string
	}{
		{"replan", `{"steps":[{"action":"assert_visible","predicate":{"text":"x"},"on_fail":{"strategy":"replan"}},{"action":"tap","x":1,"y":1}]}`,
			StatusNeedsReplan, false, StatusSkipped, StatusNeedsReplan},
		{"require_user", `{"steps":[{"action":"assert_visible","predicate":{"text":"x"},"on_fail":"require_user"},{"action":"tap","x":1,"y":1}]}`,
			StatusNeedsUser, false, StatusSkipped, StatusNeedsUser},
		{"script-level skip", `{"on_fail":"skip","steps":[{"action":"assert_visible","predicate":{"text":"x"}},{"action":"tap","x":1,"y":1}]}`,
			StatusSkipped, true, StatusOK, "passed"},
		{"step overrides script", `{"on_fail":"skip","steps":[{"action":"assert_visible","predicate":{"text":"x"},"on_fail":"abort"},{"action":"tap","x":1,"y":1}]}`,
			StatusFailed, false, StatusSkipped, "failed"},
		{"retry then fallback skip", `{"steps":[{"action":"assert_visible","predicate":{"text":"x"},"on_fail":{"strategy":"retry","max_retries":2,"retry_delay_ms":1,"fallback_strategy":"skip"}},{"action":"tap","x":1,"y":1}]}`,
			StatusSkipped, true, StatusOK, "passed"},
		{"retry then fallback replan", `{"steps":[{"action":"assert_visible","predicate":{"text":"x"},"on_fail":{"strategy":"retry","max_retries":1,"fallback_strategy":"replan"}}]}`,
			StatusNeedsReplan, false, "", StatusNeedsReplan},
		{"retry default abort", `{"steps":[{"action":"assert_visible","predicate":{"text":"x"},"on_fail":"retry","retries":1}]}`,
			StatusFailed, false, "", "failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := run(t, newFake(), tc.script)
			if res.Steps[0].Status != tc.status || res.Passed != tc.passed || res.Status != tc.topState {
				t.Fatalf("%+v / %q", res.Steps[0], res.Status)
			}
			if tc.next != "" && res.Steps[1].Status != tc.next {
				t.Fatalf("next = %q", res.Steps[1].Status)
			}
		})
	}
	// retry succeeds once the element appears
	n := 0
	dev := &countingTree{fakeDevice: newFake(), n: &n, appear: 3, elem: device.Element{Role: "TextView", Text: "Later", Bounds: device.Rect{W: 5, H: 5}}}
	res := run(t, dev, `{"steps":[{"action":"assert_visible","predicate":{"text":"Later"},"on_fail":{"strategy":"retry","max_retries":5,"retry_delay_ms":1}}]}`)
	if !res.Passed {
		t.Fatalf("%+v", res.Steps[0])
	}
}

func TestResultFieldsAndFailureScreenshot(t *testing.T) {
	f := newFake()
	f.shot = device.Screenshot{PNG: []byte("PNGDATA")}
	res := run(t, f, hdr+`[{"action":"wait","ms":5},{"action":"assert_visible","predicate":{"text":"Nope"}}]}`)
	ok, bad := res.Steps[0], res.Steps[1]
	if ok.DurationMs < 4 || ok.Screenshot != "" {
		t.Fatalf("%+v", ok)
	}
	if b, _ := base64.StdEncoding.DecodeString(bad.Screenshot); string(b) != "PNGDATA" {
		t.Fatalf("screenshot = %q", bad.Screenshot)
	}
	if bad.Code != CodeAssertionFailed || bad.Debug == nil || res.TotalMs < 4 {
		t.Fatalf("%+v", bad)
	}
	// disabled capture, deterministic mode: no screenshot, no debug
	s, _ := Parse([]byte(hdr + `[{"action":"assert_visible","predicate":{"text":"Nope"}}]}`))
	r2, _ := ExecuteScript(context.Background(), f, s, Options{NoFailureScreenshot: true, Mode: "deterministic"})
	if r2.Steps[0].Screenshot != "" || r2.Steps[0].Debug != nil {
		t.Fatalf("%+v", r2.Steps[0])
	}
	// skipped failures carry no screenshot
	r3 := run(t, f, hdr+`[{"action":"assert_visible","predicate":{"text":"Nope"},"on_fail":"skip"}]}`)
	if r3.Steps[0].Screenshot != "" {
		t.Fatalf("%+v", r3.Steps[0])
	}
	// screenshot step returns the image inline
	r4 := run(t, f, hdr+`[{"action":"screenshot"}]}`)
	if r4.Steps[0].Screenshot == "" {
		t.Fatal("screenshot step returned nothing")
	}
}

func TestStepTimeout(t *testing.T) {
	s := `{"steps":[{"action":"swipe","x1":1,"y1":1,"x2":2,"y2":2,"timeout_ms":1}]}`
	if r := run(t, newFake(), s); !r.Passed {
		t.Fatalf("%+v", r.Steps)
	}
	slow := &slowDevice{fakeDevice: newFake()}
	r := run(t, slow, `{"mode":"deterministic","steps":[{"action":"tap","predicate":{"text":"Login"},"timeout_ms":30}]}`)
	if r.Passed || r.Steps[0].Code != CodeTimeout || !strings.Contains(r.Steps[0].Error, "timed out after 30ms") {
		t.Fatalf("%+v", r.Steps[0])
	}
}

type slowDevice struct{ *fakeDevice }

func (s *slowDevice) UITree(ctx context.Context) (device.Element, error) {
	<-ctx.Done()
	return device.Element{}, ctx.Err()
}

func TestAlertsHandling(t *testing.T) {
	f := newFake()
	alert := device.Element{Role: "android.widget.Button", Text: "Allow", ResourceID: "com.android.permissioncontroller:id/permission_allow_button",
		Bounds: device.Rect{X: 10, Y: 10, W: 20, H: 20}}
	f.tree.Children = append(f.tree.Children, alert)
	f.onSwipe = nil
	// the target appears only after the alert was tapped
	g := &alertDevice{fakeDevice: f, alert: alert}
	res := run(t, g, `{"alerts":"accept","defaults":{"settleMs":0},"steps":[{"action":"tap","predicate":{"text":"Behind"},"timeout_ms":1000}]}`)
	if !res.Passed || !g.dismissed {
		t.Fatalf("%+v dismissed=%v", res.Steps, g.dismissed)
	}
	// in-app dialogs (not system ids) are left alone
	h := newFake()
	h.tree.Children = append(h.tree.Children, device.Element{Role: "Button", Text: "OK", Bounds: device.Rect{W: 5, H: 5}})
	r2 := run(t, h, `{"alerts":"accept","steps":[{"action":"tap","predicate":{"text":"Nope"}}]}`)
	if r2.Passed || len(h.taps) != 0 {
		t.Fatalf("tapped app dialog: %v", h.taps)
	}
}

type alertDevice struct {
	*fakeDevice
	alert     device.Element
	dismissed bool
}

func (a *alertDevice) UITree(context.Context) (device.Element, error) {
	t := a.fakeDevice.tree
	if a.dismissed {
		t.Children = []device.Element{{Role: "Button", Text: "Behind", Bounds: device.Rect{X: 1, Y: 1, W: 2, H: 2}}}
	}
	return t, nil
}
func (a *alertDevice) Tap(ctx context.Context, x, y int) error {
	if x == 20 && y == 20 {
		a.dismissed = true
	}
	return a.fakeDevice.Tap(ctx, x, y)
}

func TestPlatformStepsAndCheckpoints(t *testing.T) {
	f := newFake()
	res := run(t, f, hdr+`[{"action":"tap","x":1,"y":1,"platform":"ios"},{"action":"tap","x":2,"y":2,"platform":"android"},
		{"action":"checkpoint","name":"cp","metadata":{"a":1}}]}`)
	// fake device reports no platform: both platform-restricted steps are skipped
	if !res.Passed || len(f.taps) != 0 || res.Steps[0].Status != StatusSkipped {
		t.Fatalf("%+v", res.Steps)
	}
	if len(res.Checkpoints) != 1 || res.Checkpoints[0].Name != "cp" || res.Checkpoints[0].StepIndex != 2 {
		t.Fatalf("%+v", res.Checkpoints)
	}
}

func TestSwipeScrollGeometry(t *testing.T) {
	f := newFake()
	f.tree.Bounds = device.Rect{W: 1000, H: 2000}
	res := run(t, f, hdr+`[
		{"action":"swipe","direction":"up"},
		{"action":"swipe","direction":"left","distance":"short"},
		{"action":"swipe","from_coords":[1,2],"to_coords":[3,4],"duration_ms":50},
		{"action":"scroll","direction":"down","amount":"full"},
		{"action":"swipe","direction":"down","from":{"predicate":{"text":"Welcome back"}},"distance":"full"}]}`)
	if !res.Passed {
		t.Fatalf("%+v", res.Steps)
	}
	want := [][5]int{
		{500, 1500, 500, 500, 300},
		{625, 1000, 375, 1000, 300},
		{1, 2, 3, 4, 50},
		{500, 200, 500, 1800, 400}, // see below
		{200, 25, 200, 1525, 300},
	}
	// scroll down moves the finger up: from lower to upper
	want[3] = [5]int{500, 1800, 500, 200, 400}
	if len(f.swipes) != len(want) {
		t.Fatalf("swipes = %v", f.swipes)
	}
	for i := range want {
		if f.swipes[i] != want[i] {
			t.Errorf("swipe %d = %v want %v", i, f.swipes[i], want[i])
		}
	}
}

func TestScrollToElement(t *testing.T) {
	fastTimers(t)
	f := newFake()
	f.tree.Bounds = device.Rect{W: 1000, H: 2000}
	f.onSwipe = func(n int) {
		if n == 2 {
			f.tree.Children = append(f.tree.Children, device.Element{Role: "TextView", Text: "Privacy", Bounds: device.Rect{X: 0, Y: 500, W: 100, H: 50}})
		}
	}
	res := run(t, f, hdr+`[{"action":"scroll","direction":"down","to_element":{"predicate":{"text":"Privacy"},"fully_visible":true},"max_scrolls":5}]}`)
	if !res.Passed || len(f.swipes) != 2 || res.Steps[0].Matched == nil {
		t.Fatalf("%+v swipes=%v", res.Steps, f.swipes)
	}
	g := newFake()
	g.tree.Bounds = device.Rect{W: 1000, H: 2000}
	res = run(t, g, hdr+`[{"action":"scroll","to_element":{"predicate":{"text":"Nope"}},"max_scrolls":2}]}`)
	if res.Passed || len(g.swipes) != 2 {
		t.Fatalf("%+v %v", res.Steps, g.swipes)
	}
}

func TestWaitStableAndExtract(t *testing.T) {
	fastTimers(t)
	res := run(t, newFake(), hdr+`[{"action":"wait_for","timeout_ms":500},{"action":"wait_stable","timeout_ms":500},
		{"action":"wait_for","predicate":{"text":"Welcome back"},"stable":true,"timeout_ms":500}]}`)
	if !res.Passed {
		t.Fatalf("%+v", res.Steps)
	}
	// never stable -> timeout
	n := 0
	chg := &changingDevice{fakeDevice: newFake(), n: &n}
	res = run(t, chg, hdr+`[{"action":"wait_for","timeout_ms":40}]}`)
	if res.Passed || !strings.Contains(res.Steps[0].Error, "timed out") {
		t.Fatalf("%+v", res.Steps[0])
	}
	f := newFake()
	f.tree.Children[1].Text = "Order #4711 ready"
	res = run(t, f, hdr+`[
		{"action":"extract","extract_key":"id","predicate":{"text_contains":"Order"},"extract_regex":"#(\\d+)"},
		{"action":"extract","extract_key":"lit","extract_kind":"literal","text":"hi"},
		{"action":"extract","extract_key":"shot","extract_kind":"screenshot"},
		{"action":"type","text":"${id}-${lit}"}]}`)
	if !res.Passed || f.typed[0] != "4711-hi" || res.Storage["id"] != "4711" || len(res.Storage["shot"]) != 64 {
		t.Fatalf("%+v typed=%v storage=%v", res.Steps, f.typed, res.Storage)
	}
	res = run(t, f, hdr+`[{"action":"extract","extract_key":"x","predicate":{"text_contains":"Order"},"extract_regex":"ZZZ"}]}`)
	if res.Passed {
		t.Fatal("regex miss should fail")
	}
}

type changingDevice struct {
	*fakeDevice
	n *int
}

func (c *changingDevice) UITree(context.Context) (device.Element, error) {
	*c.n++
	t := c.fakeDevice.tree
	t.Text = fmt.Sprint(*c.n)
	return t, nil
}

func TestExploreModeRunsOnlyLastObserve(t *testing.T) {
	f := newFake()
	res := run(t, f, `{"defaults":{"settleMs":0},"steps":[{"action":"observe"},{"action":"observe","include":["ui_tree"]}]}`)
	if res.Steps[0].Status != StatusSkipped || res.Steps[1].Status != StatusOK {
		t.Fatalf("%+v", res.Steps)
	}
	if _, ok := res.Steps[1].Result["texts"]; !ok {
		t.Fatalf("%+v", res.Steps[1].Result)
	}
	res = run(t, f, `{"mode":"deterministic","steps":[{"action":"observe"},{"action":"observe"}]}`)
	if res.Steps[0].Status != StatusOK {
		t.Fatalf("%+v", res.Steps)
	}
}
