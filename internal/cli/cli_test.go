package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"pocketlab/internal/lifecycle"
)

const (
	andID = "android-AAA"
	iosID = "ios-BBB"
	simID = "sim-CCC"
)

const devicesJSON = `[
 {"id":"android-AAA","serial":"AAA","name":"SM-G780G","platform":"android","model":"SM-G780G","osVersion":"13","state":"connected"},
 {"id":"ios-BBB","serial":"BBB","name":"iPhone 15","platform":"ios","model":"iPhone15,4","osVersion":"26","state":"connected"},
 {"id":"sim-CCC","serial":"CCC","name":"iPhone 17","platform":"ios","model":"iPhone-17","osVersion":"27","state":"connected","simulator":true}
]`

const oneDeviceJSON = `[{"id":"android-AAA","serial":"AAA","name":"SM-G780G","platform":"android","model":"SM-G780G","osVersion":"13","state":"connected"}]`

const treeJSON = `{"role":"android.widget.FrameLayout","bounds":{"x":0,"y":0,"w":1000,"h":2000},"enabled":true,"children":[
 {"role":"android.widget.TextView","text":"Settings label","bounds":{"x":0,"y":0,"w":100,"h":100},"enabled":true},
 {"role":"android.widget.Button","text":"Settings","resourceId":"com.app:id/btn_settings","bounds":{"x":200,"y":300,"w":200,"h":100},"clickable":true,"enabled":true},
 {"role":"android.widget.Button","text":"Ayarlar","resourceId":"com.app:id/btn_ayar","bounds":{"x":0,"y":1000,"w":400,"h":200},"clickable":true,"enabled":true}
]}`

type rec struct {
	Method, Path, Query, Body, CType string
}

type fakeServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []rec
	h    map[string]http.HandlerFunc // "METHOD /path" (without /api/v1)
}

func newFake(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{h: map[string]http.HandlerFunc{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		p := strings.TrimPrefix(r.URL.Path, "/api/v1")
		f.mu.Lock()
		f.reqs = append(f.reqs, rec{r.Method, p, r.URL.RawQuery, string(b), r.Header.Get("Content-Type")})
		f.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(b))
		if h, ok := f.h[r.Method+" "+p]; ok {
			h(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":"no route","code":"NOT_FOUND"}`)
	}))
	t.Cleanup(f.Close)
	f.json("GET /devices", devicesJSON)
	return f
}

func (f *fakeServer) json(key, body string) {
	f.h[key] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}
}

func (f *fakeServer) last(method, path string) (rec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.reqs) - 1; i >= 0; i-- {
		if f.reqs[i].Method == method && f.reqs[i].Path == path {
			return f.reqs[i], true
		}
	}
	return rec{}, false
}

func (f *fakeServer) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

// run executes the CLI against the fake server.
func run(t *testing.T, f *fakeServer, stdin string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var so, se bytes.Buffer
	env := Env{Stdin: strings.NewReader(stdin), Stdout: &so, Stderr: &se, Getenv: func(string) string { return "" }}
	url := "http://127.0.0.1:1"
	if f != nil {
		url = f.URL
	}
	code = Main(context.Background(), append([]string{"--server", url}, args...), env)
	return code, so.String(), se.String()
}

func TestGlobalFlagsAnywhereAndEnv(t *testing.T) {
	f := newFake(t)
	// --server after the command, device from the environment.
	var so, se bytes.Buffer
	env := Env{Stdout: &so, Stderr: &se, Getenv: func(k string) string {
		return map[string]string{"POCKETLAB_URL": f.URL, "POCKETLAB_DEVICE": "sm-g"}[k]
	}}
	f.json("GET /devices/android-AAA", `{"id":"android-AAA","name":"SM-G780G","platform":"android","state":"connected"}`)
	if code := Main(context.Background(), []string{"device", "info"}, env); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, se.String())
	}
	if !strings.Contains(so.String(), "SM-G780G") {
		t.Errorf("out=%q", so.String())
	}
	// flag overrides env, placed after the command
	so.Reset()
	f.json("GET /devices/ios-BBB", `{"id":"ios-BBB","name":"iPhone 15","platform":"ios","state":"connected"}`)
	if code := Main(context.Background(), []string{"device", "info", "-d", "ios-BBB"}, env); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, se.String())
	}
	if !strings.Contains(so.String(), "iPhone 15") {
		t.Errorf("out=%q", so.String())
	}
}

func TestUsageErrors(t *testing.T) {
	f := newFake(t)
	cases := [][]string{
		{},
		{"nosuchcmd"},
		{"tap", "10"},
		{"tap", "a", "b"},
		{"-d", "android-AAA", "swipe", "sideways"},
		{"swipe", "1", "2", "3"},
		{"key"},
		{"wait"},
		{"tap", "--text", "x", "--id", "y"},
		{"--bogus", "devices"},
		{"devices", "--bogus"},
	}
	for _, args := range cases {
		code, _, _ := run(t, f, "", args...)
		if code != ExitUsage {
			t.Errorf("%v: code=%d want %d", args, code, ExitUsage)
		}
	}
}

func TestHelp(t *testing.T) {
	code, out, _ := run(t, nil, "", "help")
	if code != 0 || !strings.Contains(out, "Exit codes") {
		t.Errorf("help: %d %q", code, out)
	}
	for _, c := range commands {
		code, out, errOut := run(t, nil, "", c.name, "--help")
		if code != 0 {
			t.Errorf("%s --help: code %d (%s)", c.name, code, errOut)
		}
		if c.name != "help" && !strings.Contains(out, "Usage:") {
			t.Errorf("%s --help lacks Usage: %q", c.name, out)
		}
		code, out, _ = run(t, nil, "", "help", c.name)
		if code != 0 || out == "" {
			t.Errorf("help %s: %d", c.name, code)
		}
	}
	if code, _, _ := run(t, nil, "", "help", "zzz"); code != ExitUsage {
		t.Errorf("help zzz code=%d", code)
	}
	_, out, _ = run(t, nil, "", "tap", "--help")
	if !strings.Contains(out, "iOS points, Android pixels") {
		t.Errorf("tap help must state coordinate units: %q", out)
	}
}

func TestInterspersedFlagsAndDashDash(t *testing.T) {
	f := newFake(t)
	f.json("POST /devices/android-AAA/long-press", `{"ok":true}`)
	if code, _, e := run(t, f, "", "-d", "android-AAA", "longpress", "10", "--ms", "1200", "20"); code != 0 {
		t.Fatalf("code=%d %s", code, e)
	}
	r, _ := f.last("POST", "/devices/android-AAA/long-press")
	if r.Body != `{"ms":1200,"x":10,"y":20}` {
		t.Errorf("body=%s", r.Body)
	}
	f.json("POST /devices/android-AAA/type", `{"ok":true}`)
	if code, _, e := run(t, f, "", "-d", "android-AAA", "type", "--", "-hello world"); code != 0 {
		t.Fatalf("code=%d %s", code, e)
	}
	r, _ = f.last("POST", "/devices/android-AAA/type")
	if r.Body != `{"text":"-hello world"}` {
		t.Errorf("body=%s", r.Body)
	}
}

func TestUnreachableExit4(t *testing.T) {
	code, _, errOut := run(t, nil, "", "devices")
	if code != ExitUnreachable {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(errOut, "pocketlabd") {
		t.Errorf("missing hint: %q", errOut)
	}
}

func TestDeviceExitCodes(t *testing.T) {
	f := newFake(t)
	if code, _, _ := run(t, f, "", "-d", "nope", "device", "info"); code != ExitDevice {
		t.Errorf("not found code=%d", code)
	}
	if code, _, e := run(t, f, "", "device", "info"); code != ExitDevice || !strings.Contains(e, "--device") {
		t.Errorf("ambiguous code=%d %s", code, e)
	}
	if code, _, _ := run(t, f, "", "-d", "iphone", "device", "info"); code != ExitDevice {
		t.Errorf("iphone matches phone+sim, want ambiguous; code=%d", code)
	}
	// server-reported DEVICE_NOT_FOUND maps to 3 as well
	f.h["GET /devices/android-AAA"] = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":"device not found","code":"DEVICE_NOT_FOUND"}`)
	}
	if code, _, _ := run(t, f, "", "-d", "android-AAA", "device", "info"); code != ExitDevice {
		t.Errorf("api DEVICE_NOT_FOUND code=%d", code)
	}
	// other API errors are exit 1
	f.h["GET /devices/ios-BBB"] = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":"boom","code":"DEVICE_ERROR"}`)
	}
	code, _, e := run(t, f, "", "-d", "ios-BBB", "device", "info")
	if code != ExitError || !strings.Contains(e, "boom") {
		t.Errorf("code=%d %s", code, e)
	}
}

func TestSingleDeviceAutoSelect(t *testing.T) {
	f := newFake(t)
	f.json("GET /devices", oneDeviceJSON)
	f.json("GET /devices/android-AAA", `{"id":"android-AAA","name":"SM-G780G","platform":"android","state":"connected"}`)
	if code, out, e := run(t, f, "", "device", "info"); code != 0 || !strings.Contains(out, "android-AAA") {
		t.Fatalf("code=%d out=%q err=%q", code, out, e)
	}
	f.json("GET /devices", `[]`)
	if code, _, _ := run(t, f, "", "device", "info"); code != ExitDevice {
		t.Errorf("no devices code=%d", code)
	}
}

func TestDevicesOutput(t *testing.T) {
	f := newFake(t)
	code, out, _ := run(t, f, "", "devices")
	if code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"ID", "android-AAA", "ios simulator", "iPhone 15"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	code, out, _ = run(t, f, "", "--json", "devices")
	var devs []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &devs) != nil || len(devs) != 3 {
		t.Errorf("json out: %d %q", code, out)
	}
}

func TestTapCoordsAndText(t *testing.T) {
	f := newFake(t)
	f.json("POST /devices/android-AAA/tap", `{"ok":true}`)
	f.json("GET /devices/android-AAA/ui-tree", treeJSON)
	if code, _, e := run(t, f, "", "-d", "android-AAA", "tap", "5", "6"); code != 0 {
		t.Fatalf("%d %s", code, e)
	}
	if r, _ := f.last("POST", "/devices/android-AAA/tap"); r.Body != `{"x":5,"y":6}` {
		t.Errorf("body=%s", r.Body)
	}
	// exact text match beats substring ("Settings label" appears first in the tree)
	code, out, e := run(t, f, "", "-d", "android-AAA", "tap", "--text", "settings")
	if code != 0 {
		t.Fatalf("%d %s", code, e)
	}
	if r, _ := f.last("POST", "/devices/android-AAA/tap"); r.Body != `{"x":300,"y":350}` {
		t.Errorf("body=%s out=%s", r.Body, out)
	}
	if code, _, _ := run(t, f, "", "-d", "android-AAA", "tap", "--id", "btn_ayar"); code != 0 {
		t.Fatal(code)
	}
	if r, _ := f.last("POST", "/devices/android-AAA/tap"); r.Body != `{"x":200,"y":1100}` {
		t.Errorf("body=%s", r.Body)
	}
	// no match -> error with --ocr hint, exit 1
	code, _, e = run(t, f, "", "-d", "android-AAA", "tap", "--text", "Nope")
	if code != ExitError || !strings.Contains(e, "--ocr") {
		t.Errorf("code=%d %s", code, e)
	}
	// --ocr fallback hits the OCR tap endpoint
	f.json("POST /ocr/devices/android-AAA/tap", `{"x":11,"y":22,"matches":1}`)
	code, out, e = run(t, f, "", "-d", "android-AAA", "tap", "--text", "Nope", "--ocr")
	if code != 0 || !strings.Contains(out, "(11, 22)") {
		t.Errorf("code=%d out=%q err=%q", code, out, e)
	}
	if r, _ := f.last("POST", "/ocr/devices/android-AAA/tap"); !strings.Contains(r.Body, `"text":"Nope"`) {
		t.Errorf("ocr body=%s", r.Body)
	}
}

func TestSwipeDirectionAndExplicit(t *testing.T) {
	f := newFake(t)
	f.json("POST /devices/android-AAA/swipe", `{"ok":true}`)
	f.json("GET /devices/android-AAA/ui-tree", treeJSON) // 1000 x 2000
	if code, _, e := run(t, f, "", "-d", "android-AAA", "swipe", "left"); code != 0 {
		t.Fatalf("%d %s", code, e)
	}
	r, _ := f.last("POST", "/devices/android-AAA/swipe")
	if r.Body != `{"durationMs":300,"x1":800,"x2":200,"y1":1000,"y2":1000}` {
		t.Errorf("body=%s", r.Body)
	}
	if code, _, _ := run(t, f, "", "-d", "android-AAA", "swipe", "1", "2", "3", "4", "--ms", "50"); code != 0 {
		t.Fatal(code)
	}
	r, _ = f.last("POST", "/devices/android-AAA/swipe")
	if r.Body != `{"durationMs":50,"x1":1,"x2":3,"y1":2,"y2":4}` {
		t.Errorf("body=%s", r.Body)
	}
}

func TestDirectionSwipeGeometry(t *testing.T) {
	x1, y1, x2, y2, ok := directionSwipe("up", 100, 200)
	if !ok || x1 != x2 || !(y2 < y1) {
		t.Errorf("up: %d %d %d %d", x1, y1, x2, y2)
	}
	x1, y1, x2, y2, _ = directionSwipe("right", 100, 200)
	if y1 != y2 || !(x2 > x1) {
		t.Errorf("right: %d %d %d %d", x1, y1, x2, y2)
	}
	if _, _, _, _, ok := directionSwipe("diagonal", 1, 1); ok {
		t.Error("diagonal should be invalid")
	}
}

func TestScreenshotFileAndStdout(t *testing.T) {
	f := newFake(t)
	png := []byte("\x89PNG\r\n\x1a\nfake")
	f.h["GET /devices/android-AAA/screenshot"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("X-Screen-Width", "1080")
		w.Header().Set("X-Screen-Height", "2400")
		w.Write(png)
	}
	path := filepath.Join(t.TempDir(), "s.png")
	code, out, e := run(t, f, "", "-d", "android-AAA", "screenshot", "-o", path)
	if code != 0 || !strings.Contains(out, "1080x2400") {
		t.Fatalf("%d %q %q", code, out, e)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, png) {
		t.Errorf("file contents wrong")
	}
	code, out, _ = run(t, f, "", "-d", "android-AAA", "screenshot", "-o", "-")
	if code != 0 || out != string(png) {
		t.Errorf("stdout png: %d %q", code, out)
	}
	// default name lands in cwd
	dir := t.TempDir()
	old, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer os.Chdir(old)
	if code, _, _ := run(t, f, "", "-d", "android-AAA", "screenshot"); code != 0 {
		t.Fatal(code)
	}
	m, _ := filepath.Glob(filepath.Join(dir, "screenshot-*.png"))
	if len(m) != 1 {
		t.Errorf("default screenshot file: %v", m)
	}
}

func TestTreeCompactAndJSON(t *testing.T) {
	f := newFake(t)
	f.json("GET /devices/android-AAA/ui-tree", treeJSON)
	code, out, _ := run(t, f, "", "-d", "android-AAA", "tree", "--filter", "ayar")
	if code != 0 || !strings.Contains(out, `Button "Ayarlar"`) || !strings.Contains(out, "@(200,1100)") || strings.Contains(out, `"Settings"`) {
		t.Errorf("compact tree: %d\n%s", code, out)
	}
	code, out, _ = run(t, f, "", "-d", "android-AAA", "--json", "tree")
	var v map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || v["role"] == nil {
		t.Errorf("json tree: %q", out)
	}
}

func TestAppsLaunchStopUninstallOpenKey(t *testing.T) {
	f := newFake(t)
	f.json("GET /devices/android-AAA/apps", `[{"bundleId":"com.b","name":"Bee","version":"1.0","build":"7"},{"bundleId":"com.a"}]`)
	code, out, _ := run(t, f, "", "-d", "android-AAA", "apps")
	if code != 0 || strings.Index(out, "com.a") > strings.Index(out, "com.b") || !strings.Contains(out, "1.0 (7)") {
		t.Errorf("apps:\n%s", out)
	}
	code, out, _ = run(t, f, "", "-d", "android-AAA", "apps", "--filter", "BEE")
	if strings.Contains(out, "com.a ") || !strings.Contains(out, "com.b") {
		t.Errorf("filter:\n%s", out)
	}
	for _, c := range []struct{ cmd, path string }{{"launch", "launch-app"}, {"stop", "stop-app"}} {
		f.json("POST /devices/android-AAA/"+c.path, `{"ok":true}`)
		if code, _, e := run(t, f, "", "-d", "android-AAA", c.cmd, "com.x"); code != 0 {
			t.Fatalf("%s: %d %s", c.cmd, code, e)
		}
		if r, _ := f.last("POST", "/devices/android-AAA/"+c.path); r.Body != `{"bundleId":"com.x"}` {
			t.Errorf("%s body=%s", c.cmd, r.Body)
		}
	}
	f.json("DELETE /devices/android-AAA/apps/com.x", `{"ok":true}`)
	if code, _, e := run(t, f, "", "-d", "android-AAA", "uninstall", "com.x"); code != 0 {
		t.Fatalf("%d %s", code, e)
	}
	f.json("POST /devices/android-AAA/open-url", `{"ok":true}`)
	if code, _, _ := run(t, f, "", "-d", "android-AAA", "open", "https://example.com"); code != 0 {
		t.Fatal(code)
	}
	if r, _ := f.last("POST", "/devices/android-AAA/open-url"); r.Body != `{"url":"https://example.com"}` {
		t.Errorf("open body=%s", r.Body)
	}
	f.json("POST /devices/android-AAA/press-key", `{"ok":true}`)
	if code, _, _ := run(t, f, "", "-d", "android-AAA", "key", "HOME"); code != 0 {
		t.Fatal(code)
	}
	if r, _ := f.last("POST", "/devices/android-AAA/press-key"); r.Body != `{"key":"home"}` {
		t.Errorf("key body=%s", r.Body)
	}
}

func TestInstallUploadsMultipart(t *testing.T) {
	f := newFake(t)
	var gotName, gotData string
	f.h["POST /devices/android-AAA/install-app"] = func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"`+err.Error()+`","code":"BAD_REQUEST"}`)
			return
		}
		file, hdr, _ := r.FormFile("file")
		b, _ := io.ReadAll(file)
		gotName, gotData = hdr.Filename, string(b)
		fmt.Fprint(w, `{"ok":true}`)
	}
	apk := filepath.Join(t.TempDir(), "app.apk")
	_ = os.WriteFile(apk, []byte("APKDATA"), 0o644)
	if code, _, e := run(t, f, "", "-d", "android-AAA", "install", apk); code != 0 {
		t.Fatalf("%d %s", code, e)
	}
	if gotName != "app.apk" || gotData != "APKDATA" {
		t.Errorf("upload name=%q data=%q", gotName, gotData)
	}
	// .app directory is installed by path
	f.json("POST /devices/android-AAA/install-app", `{"ok":true}`)
	appDir := filepath.Join(t.TempDir(), "My.app")
	_ = os.Mkdir(appDir, 0o755)
	if code, _, e := run(t, f, "", "-d", "android-AAA", "install", appDir); code != 0 {
		t.Fatalf("%d %s", code, e)
	}
	r, _ := f.last("POST", "/devices/android-AAA/install-app")
	if !strings.Contains(r.CType, "application/json") || !strings.Contains(r.Body, "My.app") {
		t.Errorf("dir install: %s %s", r.CType, r.Body)
	}
	if code, _, _ := run(t, f, "", "-d", "android-AAA", "install", "/nonexistent.apk"); code != ExitError {
		t.Errorf("missing file code=%d", code)
	}
}

func TestOCR(t *testing.T) {
	f := newFake(t)
	f.json("POST /ocr/devices/android-AAA", `{"engine":"vision","width":1,"height":1,"results":[{"text":"Hello","bounds":{"x":1,"y":2,"w":3,"h":4},"confidence":0.9}]}`)
	f.json("POST /ocr/devices/android-AAA/find", `{"matches":[]}`)
	code, out, _ := run(t, f, "", "-d", "android-AAA", "ocr")
	if code != 0 || !strings.Contains(out, `"Hello" @(1,2) 3x4 conf=0.90`) {
		t.Errorf("ocr: %d %q", code, out)
	}
	code, out, _ = run(t, f, "", "-d", "android-AAA", "ocr", "--find", "zzz")
	if code != ExitError || !strings.Contains(out, "no on-screen text matched") {
		t.Errorf("find none: %d %q", code, out)
	}
}

func TestWait(t *testing.T) {
	f := newFake(t)
	calls := 0
	f.h["GET /devices/android-AAA/ui-tree"] = func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls < 3 {
			fmt.Fprint(w, `{"role":"android.widget.FrameLayout","bounds":{"x":0,"y":0,"w":10,"h":10},"enabled":true}`)
			return
		}
		fmt.Fprint(w, treeJSON)
	}
	code, out, e := run(t, f, "", "-d", "android-AAA", "wait", "--text", "Ayarlar", "--interval", "10ms", "--timeout", "5s")
	if code != 0 || !strings.Contains(out, "found") || calls < 3 {
		t.Fatalf("%d %q %q calls=%d", code, out, e, calls)
	}
	code, _, e = run(t, f, "", "-d", "android-AAA", "wait", "--text", "Never", "--interval", "10ms", "--timeout", "150ms")
	if code != ExitError || !strings.Contains(e, "did not appear") {
		t.Errorf("timeout: %d %q", code, e)
	}
	code, out, _ = run(t, f, "", "-d", "android-AAA", "wait", "--text", "Never", "--gone", "--interval", "10ms", "--timeout", "1s")
	if code != 0 || !strings.Contains(out, "gone") {
		t.Errorf("gone: %d %q", code, out)
	}
}

func TestDSLAndRun(t *testing.T) {
	f := newFake(t)
	f.json("POST /devices/android-AAA/dsl/execute", `{"passed":false,"status":"failed","totalMs":12,"steps":[{"index":1,"action":"tap","status":"ok","durationMs":3},{"index":2,"action":"assert","status":"failed","error":"no match","code":"NO_MATCH","durationMs":9}]}`)
	code, out, e := run(t, f, `{"actions":[{"action":"tap"}]}`, "-d", "android-AAA", "dsl", "-")
	if code != ExitError || !strings.Contains(out, "FAIL  2 assert") || !strings.Contains(out, "NO_MATCH") {
		t.Errorf("dsl: %d\n%s\n%s", code, out, e)
	}
	if r, _ := f.last("POST", "/devices/android-AAA/dsl/execute"); !strings.Contains(r.Body, `"actions"`) {
		t.Errorf("dsl body: %s", r.Body)
	}
	if code, _, _ := run(t, f, "not json", "-d", "android-AAA", "dsl", "-"); code != ExitUsage {
		t.Errorf("bad json code=%d", code)
	}

	// run: creates project, saves script, runs
	f.json("GET /tests/projects", `[]`)
	f.json("POST /tests/projects", `{"id":"cli","name":"cli"}`)
	f.json("PUT /tests/projects/cli/scripts/smoke.mob", `{"name":"smoke.mob"}`)
	f.json("POST /tests/projects/cli/scripts/smoke.mob/run", `{"id":"r1","passed":true,"result":{"passed":true,"totalMs":5,"steps":[{"index":1,"action":"tap","status":"ok","durationMs":2}]}}`)
	script := filepath.Join(t.TempDir(), "smoke.mob")
	_ = os.WriteFile(script, []byte("tap 1 2\n"), 0o644)
	code, out, e = run(t, f, "", "-d", "android-AAA", "run", script)
	if code != 0 || !strings.Contains(out, "passed") {
		t.Fatalf("run: %d %q %q", code, out, e)
	}
	if r, _ := f.last("PUT", "/tests/projects/cli/scripts/smoke.mob"); r.Body != "tap 1 2\n" {
		t.Errorf("script body=%q", r.Body)
	}
	if r, _ := f.last("POST", "/tests/projects/cli/scripts/smoke.mob/run"); r.Body != `{"deviceId":"android-AAA"}` {
		t.Errorf("run body=%s", r.Body)
	}
}

func TestCrashes(t *testing.T) {
	f := newFake(t)
	f.json("GET /devices/android-AAA/crashes", `[{"name":"c1","platform":"android","source":"logcat","bundleId":"com.app","timestamp":"2026-11-21T10:00:00Z","exceptionType":"java.lang.Boom","kind":"crash","size":10}]`)
	code, out, _ := run(t, f, "", "-d", "android-AAA", "crashes", "--app", "com.app", "--limit", "5")
	if code != 0 || !strings.Contains(out, "java.lang.Boom") || !strings.Contains(out, "c1") {
		t.Errorf("crashes: %d\n%s", code, out)
	}
	r, _ := f.last("GET", "/devices/android-AAA/crashes")
	if r.Query != "app=com.app&limit=5" {
		t.Errorf("query=%s", r.Query)
	}
	f.h["GET /devices/android-AAA/crashes/c1"] = func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "STACK TRACE\n") }
	code, out, _ = run(t, f, "", "-d", "android-AAA", "crash", "c1")
	if code != 0 || out != "STACK TRACE\n" {
		t.Errorf("crash: %d %q", code, out)
	}
}

func TestBridgeAndServerAndSims(t *testing.T) {
	f := newFake(t)
	f.json("GET /devices/ios-BBB/bridge", `{"platform":"ios","supported":true,"running":true,"state":"running","wdaUrl":"http://x"}`)
	f.json("POST /devices/ios-BBB/bridge/start", `{"platform":"ios","supported":true,"running":true,"state":"running"}`)
	f.json("POST /devices/ios-BBB/bridge/stop", `{"platform":"ios","supported":true,"running":false,"state":"stopped"}`)
	code, out, _ := run(t, f, "", "-d", "ios-BBB", "bridge", "status")
	if code != 0 || !strings.Contains(out, "running:   true") || !strings.Contains(out, "http://x") {
		t.Errorf("bridge status: %q", out)
	}
	if code, _, _ := run(t, f, "", "-d", "ios-BBB", "bridge", "start"); code != 0 {
		t.Error("start")
	}
	if code, _, _ := run(t, f, "", "-d", "ios-BBB", "bridge", "stop"); code != 0 {
		t.Error("stop")
	}
	if f.count("POST", "/devices/ios-BBB/bridge/start") != 1 || f.count("POST", "/devices/ios-BBB/bridge/stop") != 1 {
		t.Error("bridge start/stop not called")
	}
	if code, _, _ := run(t, f, "", "-d", "ios-BBB", "bridge", "restart"); code != ExitUsage {
		t.Error("bridge restart should be usage error")
	}

	f.json("GET /health", `{"status":"ok","version":"9.9","deviceCount":3}`)
	code, out, _ = run(t, f, "", "server", "status")
	if code != 0 || !strings.Contains(out, "9.9") {
		t.Errorf("server: %q", out)
	}

	f.json("GET /simulators", `{"simulators":[{"udid":"UDID-1","name":"GP Dev1","state":"Shutdown","runtime":"iOS 27","wda":{"state":"stopped"}},{"udid":"UDID-2","name":"GP Dev2","state":"Booted","runtime":"iOS 27","wda":{"state":"stopped"}}]}`)
	f.json("POST /simulators/UDID-1/boot", `{"udid":"UDID-1","name":"GP Dev1","state":"Booted"}`)
	f.json("POST /simulators/UDID-2/shutdown", `{"udid":"UDID-2","name":"GP Dev2","state":"Shutdown"}`)
	code, out, _ = run(t, f, "", "sims")
	if code != 0 || !strings.Contains(out, "GP Dev2") || !strings.Contains(out, "Booted") {
		t.Errorf("sims: %q", out)
	}
	if code, out, _ := run(t, f, "", "sim", "boot", "gp dev1"); code != 0 || !strings.Contains(out, "Booted") {
		t.Errorf("sim boot: %d %q", code, out)
	}
	if code, _, _ := run(t, f, "", "sim", "shutdown", "UDID-2"); code != 0 {
		t.Error("sim shutdown")
	}
	if code, _, _ := run(t, f, "", "sim", "boot", "gp"); code != ExitDevice {
		t.Errorf("ambiguous sim code=%d", code)
	}
	if code, _, _ := run(t, f, "", "sim", "boot", "nothing"); code != ExitDevice {
		t.Errorf("unknown sim code=%d", code)
	}
}

func TestRecordWritesFile(t *testing.T) {
	f := newFake(t)
	f.h["GET /devices/android-AAA/recording"] = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("MP4DATA")) }
	path := filepath.Join(t.TempDir(), "r.mp4")
	if code, _, e := run(t, f, "", "-d", "android-AAA", "record", "--seconds", "3", "-o", path); code != 0 {
		t.Fatalf("%d %s", code, e)
	}
	if b, _ := os.ReadFile(path); string(b) != "MP4DATA" {
		t.Errorf("file=%q", b)
	}
	if r, _ := f.last("GET", "/devices/android-AAA/recording"); r.Query != "seconds=3" {
		t.Errorf("query=%s", r.Query)
	}
}

func TestLogsGrep(t *testing.T) {
	f := newFake(t)
	f.h["GET /devices/android-AAA/logs"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first line\n\ndata: Error here\n\ndata: third\n\n")
	}
	code, out, _ := run(t, f, "", "-d", "android-AAA", "logs", "--grep", "error")
	if code != 0 || out != "Error here\n" {
		t.Errorf("logs: %d %q", code, out)
	}
}

func TestAgentStreamsAndRestoresModel(t *testing.T) {
	f := newFake(t)
	settings := `{"automation":{"agent":"claude","model":""},"maxSteps":50,"version":3}`
	var puts []string
	f.json("GET /ai/settings", settings)
	f.h["PUT /ai/settings"] = func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		puts = append(puts, string(b))
		fmt.Fprint(w, `{}`)
	}
	f.h["POST /ai/runs"] = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		fmt.Fprint(w, `{"id":"run1","builtin":false}`)
	}
	ev := func(seq int, typ, data string) string {
		b, _ := json.Marshal(map[string]any{"seq": seq, "type": typ, "data": data})
		return fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", seq, typ, b)
	}
	asst := `{"type":"assistant","message":{"content":[{"type":"text","text":"Opening settings"},{"type":"tool_use","name":"mcp__pocketlab__tap","input":{"deviceId":"d","x":10,"y":20}}]}}`
	res := `{"type":"result","result":"All good","is_error":false}`
	f.h["GET /ai/runs/run1/events"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ev(0, "start", "claude"), ev(1, "stdout", `{"type":"system","subtype":"init"}`), ev(2, "stdout", asst), ev(3, "stdout", res), ev(4, "done", "ok"))
	}
	code, out, e := run(t, f, "", "-d", "android-AAA", "agent", "open settings", "--model", "haiku")
	if code != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out, e)
	}
	for _, want := range []string{"Opening settings", `▸ tap(x=10 y=20)`, "✓ done: All good"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	r, _ := f.last("POST", "/ai/runs")
	var body map[string]any
	_ = json.Unmarshal([]byte(r.Body), &body)
	if body["type"] != "automation" || body["deviceId"] != "android-AAA" || body["prompt"] != "open settings" {
		t.Errorf("run body=%s", r.Body)
	}
	if len(puts) != 2 || !strings.Contains(puts[0], `"model":"haiku"`) || !strings.Contains(puts[1], `"model":""`) {
		t.Errorf("settings puts=%v", puts)
	}

	// failed run -> exit 1
	f.h["GET /ai/runs/run1/events"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ev(0, "error", "boom"), ev(1, "done", "failed"))
	}
	if code, _, _ := run(t, f, "", "-d", "android-AAA", "agent", "x"); code != ExitError {
		t.Errorf("failed run code=%d", code)
	}
}

func TestAgentBuiltinFallback(t *testing.T) {
	f := newFake(t)
	f.h["POST /ai/runs"] = func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"builtin":true,"id":""}`) }
	f.h["POST /devices/android-AAA/agent/run"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"thought\",\"text\":\"thinking\"}\n\n",
			"data: {\"type\":\"action\",\"tool\":\"tap\",\"args\":{\"x\":1}}\n\n",
			"data: {\"type\":\"done\",\"text\":\"fin\"}\n\n")
	}
	code, out, e := run(t, f, "", "-d", "android-AAA", "agent", "do it")
	if code != 0 || !strings.Contains(out, "thinking") || !strings.Contains(out, "▸ tap(x=1)") || !strings.Contains(out, "✓ done: fin") {
		t.Errorf("builtin: %d\n%s\n%s", code, out, e)
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := run(t, nil, "", "version")
	if code != 0 || !strings.HasPrefix(out, "pocketlab ") {
		t.Errorf("%d %q", code, out)
	}
}

func TestIOSBuildWDA(t *testing.T) {
	f := newFake(t)
	f.h["POST /ios/wda/build"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		fmt.Fprint(w, `{"state":"running","mode":"device","lines":[]}`)
	}
	f.h["GET /ios/wda/build/stream"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: log\ndata: \"CompileC one\"\n\nevent: status\ndata: {\"state\":\"succeeded\"}\n\n")
	}
	f.json("GET /ios/wda/build", `{"state":"succeeded","mode":"device","team":"ABCDE12345","runnerApp":"/x/Runner.app","lines":[]}`)
	f.json("GET /devices", devicesJSON)
	f.json("POST /devices/ios-BBB/iosbridge/install", `{"state":"stopped"}`)
	code, out, errOut := run(t, f, "", "ios", "build-wda", "--team", "abcde12345", "--install", "ios-BBB")
	if code != 0 || !strings.Contains(out, "/x/Runner.app") || !strings.Contains(errOut, "CompileC one") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if r, _ := f.last("POST", "/ios/wda/build"); !strings.Contains(string(r.Body), `"teamId":"abcde12345"`) {
		t.Errorf("body: %s", r.Body)
	}
	if f.count("POST", "/devices/ios-BBB/iosbridge/install") != 1 {
		t.Error("runner not installed")
	}

	// structured server error is shown with its hint
	f2 := newFake(t)
	f2.h["POST /ios/wda/build"] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		fmt.Fprint(w, `{"error":"No Apple development team was found","code":"TEAM_NOT_FOUND","hint":"enter your Team ID"}`)
	}
	code, _, errOut = run(t, f2, "", "ios", "build-wda")
	if code == 0 || !strings.Contains(errOut, "TEAM_NOT_FOUND") || !strings.Contains(errOut, "enter your Team ID") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if code, _, _ := run(t, f2, "", "ios", "build-wda", "--sim", "--install", "x"); code != ExitUsage {
		t.Errorf("sim+install should be a usage error: %d", code)
	}
	if code, _, _ := run(t, f2, "", "ios", "nope"); code != ExitUsage {
		t.Errorf("bad subcommand: %d", code)
	}
}

func TestServerStatusHistoryStop(t *testing.T) {
	f := newFake(t)
	f.json("GET /server/status", `{"running":true,"pid":4321,"version":"9.9","addr":"127.0.0.1:8700","startedAt":"2026-01-02T03:04:05Z","uptimeSec":3700,"startedVia":"pocketlabd","owner":"cli","eventsFile":"/x/server-events.jsonl"}`)
	code, out, _ := run(t, f, "", "server", "status")
	if code != 0 || !strings.Contains(out, "4321") || !strings.Contains(out, "pocketlabd") || !strings.Contains(out, "1h1m") {
		t.Errorf("status: %d %q", code, out)
	}

	f.json("GET /server/history", `{"events":[{"time":"2026-01-02T05:00:00Z","event":"unexpected_exit","pid":4321,"reason":"previous run ended unexpectedly"},{"time":"2026-01-02T03:04:05Z","event":"start","pid":4321,"startedVia":"pocketlabd","reason":"server started"}]}`)
	code, out, _ = run(t, f, "", "server", "history", "--limit", "5")
	if code != 0 || !strings.Contains(out, "unexpected_exit") || strings.Index(out, "start") > strings.Index(out, "unexpected_exit") || !strings.Contains(out, "!") {
		t.Errorf("history (oldest first, unexpected marked): %d %q", code, out)
	}
	if r, _ := f.last("GET", "/server/history"); r.Query != "limit=5" {
		t.Errorf("limit query: %q", r.Query)
	}

	var origin string
	f.h["POST /server/shutdown"] = func(w http.ResponseWriter, r *http.Request) {
		origin = r.Header.Get("X-Pocketlab-Origin")
		w.WriteHeader(202)
	}
	if code, _, _ := run(t, f, "", "server", "stop"); code != 0 || origin != "cli" {
		t.Errorf("stop origin: code=%d origin=%q", code, origin)
	}
}

func TestServerHistoryOffline(t *testing.T) {
	dir := t.TempDir()
	r := lifecycle.Begin(dir, lifecycle.Info{StartedVia: "pocketlabd"})
	r.SetStopReason("received signal terminated", "signal:SIGTERM", "SIGTERM")
	r.Stop("x")
	var so, se bytes.Buffer
	env := Env{Stdout: &so, Stderr: &se, Getenv: func(k string) string { return map[string]string{"POCKETLAB_HOME": dir}[k] }}
	code := Main(context.Background(), []string{"--server", "http://127.0.0.1:1", "server", "history"}, env)
	if code != 0 || !strings.Contains(so.String(), "received signal terminated") || !strings.Contains(so.String(), "server is not running") {
		t.Errorf("offline history: %d %q %q", code, so.String(), se.String())
	}
	so.Reset()
	code = Main(context.Background(), []string{"--server", "http://127.0.0.1:1", "server", "status"}, env)
	if code != ExitUnreachable || !strings.Contains(so.String(), "Last recorded event") {
		t.Errorf("offline status: %d %q", code, so.String())
	}
}

func TestSimSlimUnslimMem(t *testing.T) {
	f := newFake(t)
	f.json("GET /simulators", `{"simulators":[{"udid":"UDID-2","name":"GP Dev2","state":"Booted","runtime":"iOS 27","platform":"iOS","wda":{"state":"stopped"}}]}`)
	f.json("POST /simulators/UDID-2/slim", `{"udid":"UDID-2","profile":"custom","groups":["siri","photos"],"applied":["a","b"],"missing":["c"],"failed":[],"protected":[],"dryRun":true}`)
	f.json("POST /simulators/UDID-2/unslim", `{"udid":"UDID-2","enabled":["a","b"],"failed":[],"rebooted":true}`)
	f.json("GET /simulators/UDID-2/memory", `{"udid":"UDID-2","rssMB":20000,"footprintMB":5000,"procCount":120}`)

	code, out, _ := run(t, f, "", "sim", "slim", "gp dev2", "--groups", "siri, photos", "--dry-run")
	if code != 0 || !strings.Contains(out, "would disable 2 labels, 1 missing") {
		t.Fatalf("slim: %d %q", code, out)
	}
	r, ok := f.last("POST", "/simulators/UDID-2/slim")
	if !ok || strings.TrimSpace(r.Body) != `{"dryRun":true,"groups":["siri","photos"]}` {
		t.Fatalf("slim body: %q", r.Body)
	}
	if code, out, _ := run(t, f, "", "--json", "sim", "slim", "UDID-2", "--profile", "aggressive"); code != 0 || !strings.Contains(out, `"applied"`) {
		t.Fatalf("slim json: %d %q", code, out)
	}
	if r, _ := f.last("POST", "/simulators/UDID-2/slim"); !strings.Contains(r.Body, `"profile":"aggressive"`) {
		t.Fatalf("profile body: %q", r.Body)
	}
	if code, out, _ := run(t, f, "", "sim", "unslim", "UDID-2"); code != 0 || !strings.Contains(out, "2 labels re-enabled") {
		t.Fatalf("unslim: %d %q", code, out)
	}
	if code, out, _ := run(t, f, "", "sim", "mem", "UDID-2"); code != 0 || !strings.Contains(out, "footprint 5000 MB") {
		t.Fatalf("mem: %d %q", code, out)
	}
	if code, _, _ := run(t, f, "", "sim", "boot", "UDID-2", "--profile", "aggressive"); code != ExitUsage {
		t.Errorf("--profile on boot should be a usage error, got %d", code)
	}
}
