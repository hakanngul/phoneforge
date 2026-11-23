package wda

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"pocketlab/internal/device"
)

type req struct {
	Method, Path, RawQuery string
	Body                   string
}

type fakeWDA struct {
	mu          sync.Mutex
	reqs        []req
	sessions    int
	invalidNext bool // next session-scoped call returns invalid session id
	failActions bool
	srv         *httptest.Server
}

const sourceXML = `<?xml version="1.0" encoding="UTF-8"?>
<AppiumAUT>
  <XCUIElementTypeApplication type="XCUIElementTypeApplication" name="Demo" label="Demo" enabled="true" visible="true" accessible="false" x="0" y="0" width="390" height="844" index="0">
    <XCUIElementTypeWindow type="XCUIElementTypeWindow" enabled="true" visible="true" accessible="false" x="0" y="0" width="390" height="844" index="0">
      <XCUIElementTypeStaticText type="XCUIElementTypeStaticText" value="Welcome" name="Welcome" label="Welcome" enabled="true" visible="true" accessible="true" x="20" y="100" width="200" height="30" index="0"/>
      <XCUIElementTypeTextField type="XCUIElementTypeTextField" value="hi &amp; bye" name="email" label="Email" enabled="true" visible="true" accessible="true" x="20.5" y="150" width="350" height="44" index="1" hasFocus="true"/>
      <XCUIElementTypeButton type="XCUIElementTypeButton" name="loginBtn" label="Log in" enabled="false" visible="true" accessible="true" x="20" y="220" width="350" height="50" index="2"/>
      <XCUIElementTypeOther type="XCUIElementTypeOther" enabled="true" visible="true" accessible="false" x="0" y="300" width="390" height="100" index="3"/>
    </XCUIElementTypeWindow>
  </XCUIElementTypeApplication>
</AppiumAUT>`

func newFake(t *testing.T) (*fakeWDA, *Client) {
	f := &fakeWDA{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f, New(f.srv.URL)
}

func pngB64(w, h int) string {
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func (f *fakeWDA) handle(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, req{r.Method, r.URL.Path, r.URL.RawQuery, string(b)})
	invalid := f.invalidNext && strings.HasPrefix(r.URL.Path, "/session/") && r.URL.Path != "/session"
	if invalid {
		f.invalidNext = false
	}
	f.mu.Unlock()
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"value": v}) }
	switch {
	case invalid:
		w.WriteHeader(404)
		reply(map[string]any{"error": "invalid session id", "message": "Session does not exist"})
	case r.URL.Path == "/status":
		reply(map[string]any{"ready": true, "state": "success", "os": map[string]any{"version": "17.4"}})
	case r.URL.Path == "/session" && r.Method == "POST":
		f.mu.Lock()
		f.sessions++
		n := f.sessions
		f.mu.Unlock()
		reply(map[string]any{"sessionId": fmt.Sprintf("S%d", n), "capabilities": map[string]any{}})
	case strings.HasSuffix(r.URL.Path, "/window/size"):
		reply(map[string]any{"width": 390, "height": 844})
	case r.URL.Path == "/screenshot":
		reply(pngB64(1170, 2532))
	case strings.HasSuffix(r.URL.Path, "/source"):
		reply(sourceXML)
	case strings.HasSuffix(r.URL.Path, "/actions") && f.failActions:
		w.WriteHeader(500)
		reply(map[string]any{"error": "unknown error", "message": "boom"})
	case r.URL.Path == "/wda/activeAppInfo":
		reply(map[string]any{"bundleId": "com.demo", "name": "Demo", "pid": 42})
	case r.URL.Path == "/wda/getPasteboard":
		reply(base64.StdEncoding.EncodeToString([]byte("clip")))
	default:
		reply(nil)
	}
}

func (f *fakeWDA) find(suffix string) []req {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []req
	for _, r := range f.reqs {
		if strings.HasSuffix(r.Path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

func TestStatusAndSessionReuse(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	st, err := c.Status(ctx)
	if err != nil || !st.Ready || st.OS != "17.4" {
		t.Fatalf("status: %+v %v", st, err)
	}
	if _, _, err := c.WindowSize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Tap(ctx, 1, 2); err != nil {
		t.Fatal(err)
	}
	if f.sessions != 1 {
		t.Fatalf("sessions=%d want 1", f.sessions)
	}
}

func TestSessionRecreate(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	if err := c.LaunchApp(ctx, "com.a"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.invalidNext = true
	f.mu.Unlock()
	if err := c.LaunchApp(ctx, "com.b"); err != nil {
		t.Fatal(err)
	}
	if f.sessions != 2 {
		t.Fatalf("sessions=%d want 2", f.sessions)
	}
	l := f.find("/wda/apps/launch")
	if len(l) != 3 || !strings.HasPrefix(l[2].Path, "/session/S2/") {
		t.Fatalf("launch reqs: %+v", l)
	}
}

func TestScreenshotScale(t *testing.T) {
	_, c := newFake(t)
	s, err := c.Screenshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Width != 390 || s.Height != 844 || len(s.PNG) == 0 {
		t.Fatalf("shot %+v", s)
	}
	if c.Scale() != 3 {
		t.Fatalf("scale=%v", c.Scale())
	}
}

func TestSourceParse(t *testing.T) {
	f, c := newFake(t)
	root, err := c.Source(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if q := f.find("/source")[0].RawQuery; !strings.Contains(q, "format=xml") || !strings.Contains(q, "excluded_attributes=visible") {
		t.Fatalf("query %q", q)
	}
	if root.Role != "XCUIElementTypeApplication" || root.Bounds.W != 390 || root.Bounds.H != 844 {
		t.Fatalf("root %+v", root)
	}
	win := root.Children[0]
	if len(win.Children) != 4 {
		t.Fatalf("window children %d", len(win.Children))
	}
	txt, tf, btn, other := win.Children[0], win.Children[1], win.Children[2], win.Children[3]
	if txt.Text != "Welcome" || txt.Label != "Welcome" || txt.Clickable {
		t.Fatalf("static %+v", txt)
	}
	if tf.Text != "hi & bye" || tf.ResourceID != "email" || tf.Label != "Email" || !tf.Focused || !tf.Clickable || tf.Bounds.X != 21 {
		t.Fatalf("field %+v", tf)
	}
	if btn.Text != "Log in" || btn.ResourceID != "loginBtn" || btn.Enabled || !btn.Clickable {
		t.Fatalf("button %+v", btn)
	}
	if other.Clickable || !other.Enabled {
		t.Fatalf("other %+v", other)
	}
}

func TestSourceJSON(t *testing.T) {
	el, err := parseSourceJSON([]byte(`{"type":"Application","label":"A","rect":{"x":0,"y":0,"width":10,"height":20},"children":[{"type":"Button","label":"OK","name":"ok","isEnabled":true,"rect":{"x":1,"y":2,"width":3,"height":4}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b := el.Children[0]
	if el.Role != "XCUIElementTypeApplication" || b.ResourceID != "ok" || !b.Clickable || b.Bounds != (device.Rect{X: 1, Y: 2, W: 3, H: 4}) {
		t.Fatalf("%+v %+v", el, b)
	}
}

func lastActions(t *testing.T, f *fakeWDA) []map[string]any {
	t.Helper()
	rs := f.find("/actions")
	var body struct {
		Actions []map[string]any `json:"actions"`
	}
	if err := json.Unmarshal([]byte(rs[len(rs)-1].Body), &body); err != nil {
		t.Fatal(err)
	}
	return body.Actions
}

func types(f map[string]any) []string {
	var out []string
	for _, a := range f["actions"].([]any) {
		out = append(out, a.(map[string]any)["type"].(string))
	}
	return out
}

func TestActionsShape(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	if err := c.Tap(ctx, 10, 20); err != nil {
		t.Fatal(err)
	}
	a := lastActions(t, f)
	if len(a) != 1 || a[0]["type"] != "pointer" || a[0]["parameters"].(map[string]any)["pointerType"] != "touch" {
		t.Fatalf("%v", a)
	}
	if got := strings.Join(types(a[0]), ","); got != "pointerMove,pointerDown,pause,pointerUp" {
		t.Fatal(got)
	}
	mv := a[0]["actions"].([]any)[0].(map[string]any)
	if mv["x"] != float64(10) || mv["y"] != float64(20) {
		t.Fatalf("%v", mv)
	}
	if err := c.Swipe(ctx, 1, 2, 3, 4, 250); err != nil {
		t.Fatal(err)
	}
	a = lastActions(t, f)
	end := a[0]["actions"].([]any)[2].(map[string]any)
	if end["duration"] != float64(250) || end["x"] != float64(3) {
		t.Fatalf("%v", end)
	}
	if err := c.LongPress(ctx, 5, 5, 900); err != nil {
		t.Fatal(err)
	}
	if p := a; p == nil {
		t.Fatal()
	}
	a = lastActions(t, f)
	if a[0]["actions"].([]any)[2].(map[string]any)["duration"] != float64(900) {
		t.Fatalf("%v", a)
	}
	if err := c.Pinch(ctx, 100, 300, 0.5, 400); err != nil {
		t.Fatal(err)
	}
	if a = lastActions(t, f); len(a) != 2 {
		t.Fatalf("pinch fingers %d", len(a))
	}
	if err := c.DoubleTap(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	if a = lastActions(t, f); len(a[0]["actions"].([]any)) != 8 {
		t.Fatalf("%v", a)
	}
	if err := c.Drag(ctx, 1, 1, 9, 9, 300); err != nil {
		t.Fatal(err)
	}
}

func TestTapFallback(t *testing.T) {
	f, c := newFake(t)
	f.failActions = true
	if err := c.Tap(context.Background(), 7, 8); err != nil {
		t.Fatal(err)
	}
	r := f.find("/wda/tap")
	if len(r) != 1 || !strings.Contains(r[0].Body, `"x":7`) {
		t.Fatalf("%+v", r)
	}
}

func TestKeysAndKeys(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	_ = c.TypeText(ctx, "héy")
	var b struct{ Value []string }
	_ = json.Unmarshal([]byte(f.find("/wda/keys")[0].Body), &b)
	if strings.Join(b.Value, "|") != "h|é|y" {
		t.Fatalf("%v", b.Value)
	}
	_ = c.PressKey(ctx, device.KeyEnter)
	_ = c.PressKey(ctx, device.KeyDelete)
	ks := f.find("/wda/keys")
	if !strings.Contains(ks[1].Body, `"\n"`) || !strings.Contains(ks[2].Body, `"\b"`) {
		t.Fatalf("%+v", ks)
	}
	if err := c.PressKey(ctx, device.KeyHome); err != nil {
		t.Fatal("home", err)
	}
	if pb := f.find("/wda/pressButton"); len(pb) != 1 || !strings.Contains(pb[0].Body, `"home"`) || len(f.find("/wda/homescreen")) != 0 {
		t.Fatalf("home must press the Home button, got %+v", pb)
	}
	for _, k := range []device.Key{"volumeup", "volume_up", "VolumeUp", "volume-up"} {
		if err := c.PressKey(ctx, k); err != nil {
			t.Fatal("volume", k, err)
		}
	}
	if pb := f.find("/wda/pressButton"); len(pb) != 5 || !strings.Contains(pb[4].Body, "volumeUp") {
		t.Fatalf("volume aliases: %+v", pb)
	}
	if err := c.PressKey(ctx, "Volume_Down"); err != nil || !strings.Contains(f.find("/wda/pressButton")[5].Body, "volumeDown") {
		t.Fatal("volume down", err)
	}
	if err := c.PressKey(ctx, device.KeyBack); err != nil {
		t.Fatal(err)
	}
	a := lastActions(t, f)
	mv := a[0]["actions"].([]any)[0].(map[string]any)
	if mv["x"] != float64(1) || mv["y"] != float64(422) {
		t.Fatalf("edge swipe start %v", mv)
	}
	if err := c.PressKey(ctx, "bogus"); err != device.ErrUnsupported {
		t.Fatal(err)
	}
}

func TestPressKeyAppSwitchGesture(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	w, h, err := c.WindowSize(ctx)
	if err != nil || float64(h)/float64(w) < 2.0 {
		t.Skipf("fake window %dx%d is a home-button shape", w, h)
	}
	for i, k := range []device.Key{"app_switch", "recents", "app_switcher", "appswitch", "recent_apps"} {
		if err := c.PressKey(ctx, k); err != nil {
			t.Fatal(k, err)
		}
		rs := f.find("/wda/pressAndDragWithVelocity")
		if len(rs) != i+1 {
			t.Fatalf("%s: want %d calls, got %d", k, i+1, len(rs))
		}
		var b map[string]float64
		_ = json.Unmarshal([]byte(rs[i].Body), &b)
		if b["fromX"] != float64(w/2) || b["fromY"] != float64(h-1) || b["toY"] != float64(h*53/100) ||
			b["pressDuration"] != 0.1 || b["holdDuration"] != 1.0 || b["velocity"] != 400 {
			t.Fatalf("%s: bad body %s", k, rs[i].Body)
		}
	}
}

// appSwitchServer is a minimal WDA that records POST paths; size is the
// window size reported and drag404 makes pressAndDragWithVelocity 404.
func appSwitchServer(t *testing.T, wd, ht int, drag404 bool) (*Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		if r.Method == http.MethodPost {
			paths = append(paths, r.URL.Path)
		}
		mu.Unlock()
		reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"value": v}) }
		switch {
		case r.URL.Path == "/status":
			reply(map[string]any{"ready": true})
		case r.URL.Path == "/session":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessionId": "S1", "value": map[string]any{"capabilities": map[string]any{}}})
		case strings.HasSuffix(r.URL.Path, "/window/size"):
			reply(map[string]any{"width": wd, "height": ht})
		case drag404 && strings.HasSuffix(r.URL.Path, "/wda/pressAndDragWithVelocity"):
			w.WriteHeader(404)
			reply(map[string]any{"error": "unknown command", "message": "nope"})
		default:
			reply(nil)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL), &paths
}

func TestAppSwitchHomeButtonDevice(t *testing.T) {
	c, paths := appSwitchServer(t, 375, 667, false)
	if err := c.PressKey(context.Background(), "app_switch"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range *paths {
		if strings.HasSuffix(p, "/wda/pressButton") {
			n++
		}
		if strings.Contains(p, "pressAndDrag") {
			t.Fatal("home-button device must not use the swipe gesture")
		}
	}
	if n != 2 {
		t.Fatalf("want 2 home presses, got %d: %v", n, *paths)
	}
}

func TestAppSwitchFallsBackToActions(t *testing.T) {
	c, paths := appSwitchServer(t, 393, 852, true)
	if err := c.PressKey(context.Background(), "recents"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*paths, " "); !strings.Contains(got, "pressAndDragWithVelocity") || !strings.HasSuffix(got, "/actions") {
		t.Fatalf("paths: %v", *paths)
	}
}

func TestAppsAndExtras(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	if err := c.LaunchApp(ctx, "com.x"); err != nil || !strings.Contains(f.find("/wda/apps/launch")[0].Body, `"bundleId":"com.x"`) {
		t.Fatal(err)
	}
	if err := c.StopApp(ctx, "com.x"); err != nil || len(f.find("/wda/apps/terminate")) != 1 {
		t.Fatal(err)
	}
	a, err := c.ActiveApp(ctx)
	if err != nil || a.BundleID != "com.demo" || a.PID != 42 {
		t.Fatalf("%+v %v", a, err)
	}
	if err := c.OpenURL(ctx, "https://x.y"); err != nil || !strings.Contains(f.find("/url")[0].Body, "https://x.y") {
		t.Fatal(err)
	}
	if err := c.SetClipboard(ctx, "hello"); err != nil || !strings.Contains(f.find("/wda/setPasteboard")[0].Body, base64.StdEncoding.EncodeToString([]byte("hello"))) {
		t.Fatal(err)
	}
	if s, err := c.GetClipboard(ctx); err != nil || s != "clip" {
		t.Fatal(s, err)
	}
	if err := c.DismissKeyboard(ctx); err != nil || len(f.find("/wda/keyboard/dismiss")) != 1 {
		t.Fatal(err)
	}
	if err := c.SetMJPEGSettings(ctx, 15, 50, 60); err != nil {
		t.Fatal(err)
	}
	body := f.find("/appium/settings")[0].Body
	for _, want := range []string{"mjpegServerFramerate", "mjpegScalingFactor", "mjpegServerScreenshotQuality"} {
		if !strings.Contains(body, want) {
			t.Fatalf("%s missing in %s", want, body)
		}
	}
}

func TestMJPEG(t *testing.T) {
	frames := [][]byte{[]byte("\xff\xd8one\xff\xd9"), []byte("\xff\xd8two-longer\xff\xd9")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=BoundaryString")
		for _, fr := range frames {
			fmt.Fprintf(w, "--BoundaryString\r\nContent-type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(fr))
			w.Write(fr)
			w.Write([]byte("\r\n"))
		}
		// Part without Content-Length.
		fmt.Fprint(w, "--BoundaryString\r\nContent-type: image/jpeg\r\n\r\nnolen\r\n--BoundaryString\r\nContent-type: image/jpeg\r\nContent-Length: 3\r\n\r\nabc\r\n")
	}))
	defer srv.Close()
	var got [][]byte
	err := MJPEGFrames(context.Background(), srv.URL, func(j []byte) error {
		got = append(got, append([]byte(nil), j...))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{frames[0], frames[1], []byte("nolen"), []byte("abc")}
	if len(got) != len(want) {
		t.Fatalf("got %d frames: %q", len(got), got)
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("frame %d = %q", i, got[i])
		}
	}
	// fn error stops the stream.
	stop := fmt.Errorf("stop")
	if err := MJPEGFrames(context.Background(), srv.URL, func([]byte) error { return stop }); err != stop {
		t.Fatalf("err %v", err)
	}
}

type stubDev struct {
	calls []string
}

func (s *stubDev) Info() device.Info { return device.Info{ID: "ios1"} }
func (s *stubDev) Screenshot(context.Context) (device.Screenshot, error) {
	s.calls = append(s.calls, "screenshot")
	return device.Screenshot{}, device.ErrUnsupported
}
func (s *stubDev) UITree(context.Context) (device.Element, error) {
	s.calls = append(s.calls, "uitree")
	return device.Element{}, device.ErrUnsupported
}
func (s *stubDev) Tap(context.Context, int, int) error {
	s.calls = append(s.calls, "tap")
	return device.ErrUnsupported
}
func (s *stubDev) LongPress(context.Context, int, int, int) error { return device.ErrUnsupported }
func (s *stubDev) Swipe(context.Context, int, int, int, int, int) error {
	return device.ErrUnsupported
}
func (s *stubDev) TypeText(context.Context, string) error     { return device.ErrUnsupported }
func (s *stubDev) PressKey(context.Context, device.Key) error { return device.ErrUnsupported }
func (s *stubDev) ListApps(context.Context) ([]device.AppInfo, error) {
	s.calls = append(s.calls, "listapps")
	return []device.AppInfo{{BundleID: "com.inner"}}, nil
}
func (s *stubDev) LaunchApp(context.Context, string) error { return device.ErrUnsupported }
func (s *stubDev) StopApp(context.Context, string) error   { return device.ErrUnsupported }

func TestWrapRouting(t *testing.T) {
	f, c := newFake(t)
	inner := &stubDev{}
	on := true
	d := Wrap(inner, c, func() bool { return on })
	ctx := context.Background()

	if s, err := d.Screenshot(ctx); err != nil || s.Width != 390 {
		t.Fatalf("%+v %v", s, err)
	}
	if e, err := d.UITree(ctx); err != nil || e.Role != "XCUIElementTypeApplication" {
		t.Fatalf("%+v %v", e, err)
	}
	if err := d.Tap(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := d.(device.GestureExtras).DoubleTap(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := d.(device.URLOpener).OpenURL(ctx, "x://y"); err != nil {
		t.Fatal(err)
	}
	if err := d.(device.KeyboardDismisser).DismissKeyboard(ctx); err != nil {
		t.Fatal(err)
	}
	if g, err := d.(device.Clipboard).GetClipboard(ctx); err != nil || g != "clip" {
		t.Fatal(g, err)
	}
	if apps, err := d.ListApps(ctx); err != nil || apps[0].BundleID != "com.inner" {
		t.Fatal(err)
	}
	if len(inner.calls) != 1 || inner.calls[0] != "listapps" {
		t.Fatalf("inner calls %v", inner.calls)
	}

	// Inactive: everything goes to inner.
	on = false
	if err := d.Tap(ctx, 1, 1); err != device.ErrUnsupported {
		t.Fatalf("err %v", err)
	}
	if _, err := d.UITree(ctx); err != device.ErrUnsupported {
		t.Fatalf("err %v", err)
	}
	if err := d.(device.GestureExtras).Pinch(ctx, 1, 1, 2, 100); err != device.ErrUnsupported {
		t.Fatalf("err %v", err)
	}

	// Active but WDA failing and inner unsupported: WDA error surfaces.
	on = true
	f.failActions = true
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"value":{"error":"unknown error","message":"down"}}`))
	})
	err := d.Tap(ctx, 1, 1)
	if err == nil || err == device.ErrUnsupported || !strings.Contains(err.Error(), "down") {
		t.Fatalf("err %v", err)
	}
}

func TestImageToJPEGScalesDown(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 400, 800))
	for y := 0; y < 800; y++ {
		for x := 0; x < 400; x++ {
			src.Set(x, y, color.RGBA{200, uint8(x), uint8(y), 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, src); err != nil {
		t.Fatal(err)
	}
	out, err := ImageToJPEG(pngBuf.Bytes(), 200, 50)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || format != "jpeg" || cfg.Width != 200 || cfg.Height != 400 {
		t.Fatalf("got %v %v %dx%d", err, format, cfg.Width, cfg.Height)
	}
	same, err := ImageToJPEG(pngBuf.Bytes(), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _, _ := image.DecodeConfig(bytes.NewReader(same)); cfg.Width != 400 {
		t.Fatalf("maxWidth 0 must keep size, got %d", cfg.Width)
	}
	if _, err := ImageToJPEG([]byte("nope"), 100, 50); err == nil {
		t.Fatal("garbage must error")
	}
}

func TestAppSwitchLandscape(t *testing.T) {
	c, paths := appSwitchServer(t, 852, 393, false) // landscape Face ID iPhone
	if err := c.PressKey(context.Background(), "app_switch"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*paths, " "); !strings.Contains(got, "pressAndDragWithVelocity") || strings.Contains(got, "pressButton") {
		t.Fatalf("landscape Face ID must use the gesture: %v", *paths)
	}
	c, paths = appSwitchServer(t, 667, 375, false) // landscape iPhone SE
	if err := c.PressKey(context.Background(), "app_switch"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*paths, " "); strings.Contains(got, "pressAndDrag") || strings.Count(got, "pressButton") != 2 {
		t.Fatalf("landscape SE must double-press home: %v", *paths)
	}
}
