package dsl

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"pocketlab/internal/device"
)

type tap struct{ x, y int }

type fakeDevice struct {
	tree    device.Element
	taps    []tap
	typed   []string
	keys    []device.Key
	launch  []string
	swipes  [][5]int
	shot    device.Screenshot
	onSwipe func(n int)
}

func newFake() *fakeDevice {
	return &fakeDevice{tree: device.Element{
		Role:   "FrameLayout",
		Bounds: device.Rect{W: 1080, H: 1920},
		Children: []device.Element{
			{Role: "Button", Text: "Login", ResourceID: "com.x:id/login", Bounds: device.Rect{X: 100, Y: 200, W: 200, H: 100}},
			{Role: "TextView", Text: "Welcome back", Bounds: device.Rect{X: 0, Y: 0, W: 400, H: 50}},
			{Role: "Button", Text: "Login", Bounds: device.Rect{X: 100, Y: 600, W: 200, H: 100}},
		},
	}}
}

func (f *fakeDevice) Info() device.Info { return device.Info{ID: "fake"} }
func (f *fakeDevice) Screenshot(context.Context) (device.Screenshot, error) {
	return f.shot, nil
}
func (f *fakeDevice) UITree(context.Context) (device.Element, error) { return f.tree, nil }
func (f *fakeDevice) Tap(_ context.Context, x, y int) error {
	f.taps = append(f.taps, tap{x, y})
	return nil
}
func (f *fakeDevice) LongPress(_ context.Context, x, y, _ int) error {
	f.taps = append(f.taps, tap{x, y})
	return nil
}
func (f *fakeDevice) Swipe(_ context.Context, x1, y1, x2, y2, ms int) error {
	f.swipes = append(f.swipes, [5]int{x1, y1, x2, y2, ms})
	if f.onSwipe != nil {
		f.onSwipe(len(f.swipes))
	}
	return nil
}
func (f *fakeDevice) TypeText(_ context.Context, s string) error {
	f.typed = append(f.typed, s)
	return nil
}
func (f *fakeDevice) PressKey(_ context.Context, k device.Key) error {
	f.keys = append(f.keys, k)
	return nil
}
func (f *fakeDevice) ListApps(context.Context) ([]device.AppInfo, error) { return nil, nil }
func (f *fakeDevice) LaunchApp(_ context.Context, id string) error {
	f.launch = append(f.launch, id)
	return nil
}
func (f *fakeDevice) StopApp(context.Context, string) error { return nil }

const hdr = `{"version":"0.2","defaults":{"settleMs":0},"steps":`

func TestTapSelectorHitsCenter(t *testing.T) {
	f := newFake()
	res, err := Execute(context.Background(), f, []byte(hdr+`[
		{"action":"launch_app","bundleId":"com.x"},
		{"action":"tap","selector":{"text":"Login"}},
		{"action":"assert_visible","selector":{"textContains":"Welcome"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Fatalf("expected pass: %+v", res)
	}
	if len(f.taps) != 1 || f.taps[0] != (tap{200, 250}) {
		t.Fatalf("taps = %+v", f.taps)
	}
	if res.Steps[1].Matched == nil || res.Steps[1].Matched.X != 100 {
		t.Fatalf("matched = %+v", res.Steps[1].Matched)
	}
}

func TestIndexAndExplicitXY(t *testing.T) {
	f := newFake()
	_, err := Execute(context.Background(), f, []byte(hdr+`[
		{"action":"tap","selector":{"text":"Login","index":1}},
		{"action":"tap","x":5,"y":6,"selector":{"text":"Nope"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.taps) != 2 || f.taps[0] != (tap{200, 650}) || f.taps[1] != (tap{5, 6}) {
		t.Fatalf("taps = %+v", f.taps)
	}
}

func TestAssertFailureAbortsRest(t *testing.T) {
	f := newFake()
	res, err := Execute(context.Background(), f, []byte(hdr+`[
		{"action":"assert_visible","selector":{"text":"Nope"}},
		{"action":"tap","selector":{"text":"Login"}},
		{"action":"press_key","key":"back"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed {
		t.Fatal("expected failure")
	}
	want := []string{StatusFailed, StatusSkipped, StatusSkipped}
	for i, w := range want {
		if res.Steps[i].Status != w {
			t.Fatalf("step %d status %q want %q", i, res.Steps[i].Status, w)
		}
	}
	if len(f.taps) != 0 || len(f.keys) != 0 {
		t.Fatal("skipped steps executed")
	}
	if !strings.Contains(res.Steps[0].Error, "element not found") {
		t.Fatalf("error = %q", res.Steps[0].Error)
	}
}

func TestOnFailSkipAndNotVisible(t *testing.T) {
	f := newFake()
	res, _ := Execute(context.Background(), f, []byte(hdr+`[
		{"action":"assert_visible","selector":{"text":"Nope"},"onFail":"skip"},
		{"action":"assert_not_visible","selector":{"text":"Error"}},
		{"action":"assert_not_visible","selector":{"text":"Login"}}]}`))
	if res.Steps[0].Status != StatusSkipped || res.Steps[1].Status != StatusOK || res.Steps[2].Status != StatusFailed {
		t.Fatalf("%+v", res.Steps)
	}
	if res.Passed {
		t.Fatal("expected failure")
	}
}

func TestRetry(t *testing.T) {
	f := newFake()
	res, _ := Execute(context.Background(), f, []byte(`{"defaults":{"settleMs":0,"retries":2},"steps":[
		{"action":"assert_visible","selector":{"text":"Nope"},"onFail":"retry","retries":1}]}`))
	if res.Passed || res.Steps[0].Status != StatusFailed {
		t.Fatalf("%+v", res)
	}
}

func TestMalformed(t *testing.T) {
	for _, s := range []string{`{`, `{"version":"9","steps":[]}`, `{"steps":[{"action":"zap"}]}`} {
		if _, err := Execute(context.Background(), newFake(), []byte(s)); err == nil {
			t.Fatalf("expected error for %s", s)
		}
	}
}

func TestContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := Execute(ctx, newFake(), []byte(`{"steps":[{"action":"wait","ms":10000},{"action":"screenshot"}]}`))
	if err != nil || res.Passed || res.Steps[1].Status != StatusSkipped {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestMatchOrder(t *testing.T) {
	root := newFake().tree
	el, ok := Match(root, Selector{Role: "Button"})
	if !ok || el.ResourceID != "com.x:id/login" {
		t.Fatalf("%+v", el)
	}
	if _, ok := Match(root, Selector{Role: "Button", Index: 2}); ok {
		t.Fatal("index out of range matched")
	}
	if _, ok := Match(root, Selector{}); ok {
		t.Fatal("empty selector matched")
	}
}

// extraDevice adds every optional capability to fakeDevice.
type extraDevice struct {
	*fakeDevice
	calls []string
}

func (e *extraDevice) rec(s string) error { e.calls = append(e.calls, s); return nil }
func (e *extraDevice) DoubleTap(_ context.Context, x, y int) error {
	return e.rec(fmt.Sprintf("dt %d,%d", x, y))
}
func (e *extraDevice) Drag(_ context.Context, x1, y1, x2, y2, ms int) error {
	return e.rec(fmt.Sprintf("drag %d,%d>%d,%d %d", x1, y1, x2, y2, ms))
}
func (e *extraDevice) Pinch(context.Context, int, int, float64, int) error { return nil }
func (e *extraDevice) OpenURL(_ context.Context, u string) error           { return e.rec("url " + u) }
func (e *extraDevice) DismissKeyboard(context.Context) error               { return e.rec("kb") }
func (e *extraDevice) SetClipboard(_ context.Context, t string) error      { return e.rec("clip " + t) }
func (e *extraDevice) GetClipboard(context.Context) (string, error)        { return "", nil }
func (e *extraDevice) InstallApp(_ context.Context, p string) error        { return e.rec("install " + p) }
func (e *extraDevice) UninstallApp(_ context.Context, id string) error {
	return e.rec("uninstall " + id)
}
func (e *extraDevice) SetPermission(_ context.Context, id, p string, g bool) error {
	return e.rec(fmt.Sprintf("perm %s %s %v", id, p, g))
}

func TestNewActions(t *testing.T) {
	e := &extraDevice{fakeDevice: newFake()}
	res, err := Execute(context.Background(), e, []byte(hdr+`[
		{"action":"double_tap","selector":{"text":"Welcome back"}},
		{"action":"double_tap","x":7,"y":8},
		{"action":"drag","x1":1,"y1":2,"x2":3,"y2":4,"durationMs":250},
		{"action":"drag","selector":{"textContains":"Welcome"},"toSelector":{"text":"Login","index":1}},
		{"action":"open_url","url":"https://example.com"},
		{"action":"dismiss_keyboard"},
		{"action":"set_clipboard","text":"hi"},
		{"action":"install_app","path":"/tmp/a.apk"},
		{"action":"uninstall_app","bundleId":"com.x"},
		{"action":"grant_permission","bundleId":"com.x","permission":"CAMERA"},
		{"action":"revoke_permission","bundleId":"com.x","permission":"CAMERA"}]}`))
	if err != nil || !res.Passed {
		t.Fatalf("%v %+v", err, res)
	}
	want := "dt 200,25|dt 7,8|drag 1,2>3,4 250|drag 200,25>200,650 500|url https://example.com|kb|clip hi|install /tmp/a.apk|uninstall com.x|perm com.x CAMERA true|perm com.x CAMERA false"
	if got := strings.Join(e.calls, "|"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestUnsupportedCapability(t *testing.T) {
	for _, step := range []string{
		`{"action":"double_tap","x":1,"y":2}`, `{"action":"drag","x1":1}`, `{"action":"open_url","url":"u"}`,
		`{"action":"dismiss_keyboard"}`, `{"action":"set_clipboard","text":"x"}`, `{"action":"install_app","path":"p"}`,
		`{"action":"uninstall_app","bundleId":"b"}`, `{"action":"grant_permission","bundleId":"b","permission":"p"}`,
	} {
		res, err := Execute(context.Background(), newFake(), []byte(hdr+`[`+step+`]}`))
		if err != nil || res.Passed || !strings.Contains(res.Steps[0].Error, "operation not supported") {
			t.Fatalf("%s: %v %+v", step, err, res)
		}
	}
}

func TestNewFieldValidation(t *testing.T) {
	if _, err := Parse([]byte(`{"steps":[{"action":"wait_for","selector":{"text":"x"},"timeoutMs":5,"bogus":1}]}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
	res, _ := Execute(context.Background(), &extraDevice{fakeDevice: newFake()}, []byte(hdr+`[{"action":"open_url"}]}`))
	if res.Passed {
		t.Fatal("open_url without url should fail")
	}
}

func fastTimers(t *testing.T) {
	t.Helper()
	p, s := waitPollInterval, scrollSettle
	waitPollInterval, scrollSettle = 5*time.Millisecond, 0
	t.Cleanup(func() { waitPollInterval, scrollSettle = p, s })
}

func TestWaitFor(t *testing.T) {
	fastTimers(t)
	f := newFake()
	var n int
	// element appears after a few polls
	dev := &polling{fakeDevice: f, appearAfter: 3, n: &n}
	res, _ := Execute(context.Background(), dev, []byte(hdr+`[{"action":"wait_for","selector":{"text":"Later"},"timeoutMs":2000}]}`))
	if !res.Passed || n < 3 || res.Steps[0].Matched == nil {
		t.Fatalf("%+v polls=%d", res, n)
	}
	res, _ = Execute(context.Background(), newFake(), []byte(hdr+`[{"action":"wait_for","selector":{"text":"Never"},"timeoutMs":30}]}`))
	if res.Passed || !strings.Contains(res.Steps[0].Error, "timed out") {
		t.Fatalf("%+v", res)
	}
}

type polling struct {
	*fakeDevice
	appearAfter int
	n           *int
}

func (p *polling) UITree(context.Context) (device.Element, error) {
	*p.n++
	tree := p.fakeDevice.tree
	if *p.n >= p.appearAfter {
		tree.Children = append(append([]device.Element{}, tree.Children...),
			device.Element{Role: "TextView", Text: "Later", Bounds: device.Rect{X: 10, Y: 10, W: 10, H: 10}})
	}
	return tree, nil
}

func TestScrollUntilVisible(t *testing.T) {
	fastTimers(t)
	f := newFake()
	f.shot = device.Screenshot{Width: 1000, Height: 2000}
	f.onSwipe = func(n int) {
		if n == 3 {
			f.tree.Children = append(f.tree.Children, device.Element{Role: "TextView", Text: "Bottom", Bounds: device.Rect{X: 0, Y: 1000, W: 10, H: 10}})
		}
	}
	res, _ := Execute(context.Background(), f, []byte(hdr+`[{"action":"scroll_until_visible","selector":{"text":"Bottom"}}]}`))
	if !res.Passed || len(f.swipes) != 3 {
		t.Fatalf("%+v swipes=%v", res, f.swipes)
	}
	if f.swipes[0] != [5]int{500, 1400, 500, 600, 400} {
		t.Fatalf("down swipe = %v", f.swipes[0])
	}

	f2 := newFake()
	f2.shot = device.Screenshot{Width: 1000, Height: 2000}
	res, _ = Execute(context.Background(), f2, []byte(hdr+`[{"action":"scroll_until_visible","selector":{"text":"Nope"},"direction":"up","maxSwipes":2}]}`))
	if res.Passed || len(f2.swipes) != 2 || f2.swipes[0] != [5]int{500, 600, 500, 1400, 400} {
		t.Fatalf("%+v swipes=%v", res, f2.swipes)
	}
	res, _ = Execute(context.Background(), newFake(), []byte(hdr+`[{"action":"scroll_until_visible","selector":{"text":"x"},"direction":"left"}]}`))
	if res.Passed {
		t.Fatal("bad direction accepted")
	}
	// already visible: no swipes
	f3 := newFake()
	res, _ = Execute(context.Background(), f3, []byte(hdr+`[{"action":"scroll_until_visible","selector":{"text":"Welcome back"}}]}`))
	if !res.Passed || len(f3.swipes) != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestNavigateAppSwitchAliases(t *testing.T) {
	for _, tgt := range []string{"app_switch", "recents", "recent_apps", "App-Switcher"} {
		f := newFake()
		res, err := Execute(context.Background(), f, []byte(hdr+`[{"action":"navigate","target":"`+tgt+`"}]}`))
		if err != nil || !res.Passed {
			t.Fatalf("%s: %v %+v", tgt, err, res)
		}
		if len(f.keys) != 1 || f.keys[0] != device.KeyAppSwitch {
			t.Fatalf("%s: keys %v", tgt, f.keys)
		}
	}
}
