package uia2

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"phoneforge/internal/device"
)

const sampleXML = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<hierarchy index="0" class="hierarchy" rotation="0" width="1080" height="2400">
  <android.widget.FrameLayout index="0" package="x" class="android.widget.FrameLayout" text="" resource-id="" clickable="false" enabled="true" focused="false" bounds="[0,0][1080,2400]">
    <android.widget.Button index="0" class="android.widget.Button" text="OK" resource-id="x:id/ok" content-desc="Confirm" clickable="true" enabled="true" focused="true" bounds="[100,200][300,400]"/>
  </android.widget.FrameLayout>
</hierarchy>`

type fakeServer struct {
	mu      sync.Mutex
	actions []map[string]any
	srv     *httptest.Server
}

func (f *fakeServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.actions)
}

func newFake(t *testing.T) *fakeServer {
	f := &fakeServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"value":{"ready":true}}`))
	})
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"value":{"sessionId":"sid1","capabilities":{}}}`))
	})
	mux.HandleFunc("DELETE /session/sid1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"value":null}`))
	})
	mux.HandleFunc("GET /session/sid1/source", func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(map[string]any{"value": sampleXML})
		w.Write(b)
	})
	mux.HandleFunc("GET /session/sid1/screenshot", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"value":"` + base64.StdEncoding.EncodeToString([]byte("PNGDATA")) + `"}`))
	})
	mux.HandleFunc("POST /session/sid1/actions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.actions = append(f.actions, body)
		f.mu.Unlock()
		w.Write([]byte(`{"value":null}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

type fakeProc struct {
	once sync.Once
	done chan struct{}
}

func (p *fakeProc) Kill() error           { p.once.Do(func() { close(p.done) }); return nil }
func (p *fakeProc) Done() <-chan struct{} { return p.done }

func testAssets(t *testing.T) *Assets {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "appium-uiautomator2-server-v10.6.6.apk"), []byte("PK\x03\x04"), 0o644)
	os.WriteFile(filepath.Join(d, "appium-uiautomator2-server-debug-androidTest.apk"), []byte("PK\x03\x04"), 0o644)
	return &Assets{Dir: d}
}

func startedBridge(t *testing.T) (*Bridge, *fakeServer, *[]string) {
	f := newFake(t)
	var mu sync.Mutex
	var cmds []string
	b := New("adb", "SER", testAssets(t))
	b.baseURLOverride = f.srv.URL
	b.runner = Runner{
		Run: func(ctx context.Context, args ...string) ([]byte, error) {
			mu.Lock()
			cmds = append(cmds, strings.Join(args, " "))
			mu.Unlock()
			if len(args) > 2 && args[0] == "shell" && args[1] == "pm" {
				return []byte("package:io.appium.uiautomator2.server\npackage:io.appium.uiautomator2.server.test\n"), nil
			}
			if len(args) > 1 && args[1] == "dumpsys" {
				return []byte("    versionName=10.6.6\n"), nil
			}
			return nil, nil
		},
		Spawn: func(args ...string) (Proc, error) {
			mu.Lock()
			cmds = append(cmds, "SPAWN "+strings.Join(args, " "))
			mu.Unlock()
			return &fakeProc{done: make(chan struct{})}, nil
		},
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Stop(context.Background()) })
	return b, f, &cmds
}

func pmRunner(pm string, cmds *[]string) Runner {
	return Runner{
		Run: func(ctx context.Context, args ...string) ([]byte, error) {
			*cmds = append(*cmds, strings.Join(args, " "))
			if len(args) > 2 && args[0] == "shell" && args[1] == "pm" {
				return []byte(pm), nil
			}
			return nil, nil
		},
		Spawn: func(args ...string) (Proc, error) { return &fakeProc{done: make(chan struct{})}, nil },
	}
}

func TestStartMissingAssets(t *testing.T) {
	var cmds []string
	b := New("adb", "S", &Assets{Dir: t.TempDir()})
	b.runner = pmRunner("", &cmds)
	err := b.Start(context.Background())
	if !errors.Is(err, ErrAssetsMissing) || !strings.Contains(err.Error(), "download them first") {
		t.Fatalf("got %v", err)
	}
	if b.Status().LastError == "" {
		t.Fatal("LastError not set")
	}
}

func TestStartInstalledWithoutAssets(t *testing.T) {
	f := newFake(t)
	var cmds []string
	b := New("adb", "S", &Assets{Dir: t.TempDir()}) // no APKs locally
	b.baseURLOverride = f.srv.URL
	b.runner = pmRunner("package:io.appium.uiautomator2.server\npackage:io.appium.uiautomator2.server.test\n", &cmds)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer b.Stop(context.Background())
	if !b.Status().Running {
		t.Fatal("not running")
	}
	for _, c := range cmds {
		if strings.HasPrefix(c, "install") || strings.Contains(c, "dumpsys") {
			t.Fatalf("unexpected %q", c)
		}
	}
	if ok, err := b.Installed(context.Background()); err != nil || !ok {
		t.Fatalf("installed %v %v", ok, err)
	}
}

func TestStartStopAndSource(t *testing.T) {
	b, _, cmds := startedBridge(t)
	st := b.Status()
	if !st.Running || st.SessionID != "sid1" {
		t.Fatalf("state %+v", st)
	}
	for _, c := range *cmds {
		if strings.HasPrefix(c, "install") {
			t.Fatalf("unexpected install: %s", c)
		}
	}
	root, err := b.Source(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if root.Role != "hierarchy" || len(root.Children) != 1 || len(root.Children[0].Children) != 1 {
		t.Fatalf("tree %+v", root)
	}
	btn := root.Children[0].Children[0]
	if btn.Role != "android.widget.Button" || btn.Text != "OK" || btn.Label != "Confirm" ||
		btn.ResourceID != "x:id/ok" || !btn.Clickable || !btn.Enabled || !btn.Focused ||
		btn.Bounds != (device.Rect{X: 100, Y: 200, W: 200, H: 200}) {
		t.Fatalf("button %+v", btn)
	}
	png, err := b.Screenshot(context.Background())
	if err != nil || string(png) != "PNGDATA" {
		t.Fatalf("screenshot %q %v", png, err)
	}
	if err := b.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.Status().Running {
		t.Fatal("still running")
	}
}

func lastActions(f *fakeServer) []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.actions[len(f.actions)-1]["actions"].([]any)
}

func TestPinchTwoSequences(t *testing.T) {
	b, f, _ := startedBridge(t)
	if err := b.Pinch(context.Background(), 500, 1000, 2, 300); err != nil {
		t.Fatal(err)
	}
	seqs := lastActions(f)
	if len(seqs) != 2 {
		t.Fatalf("want 2 sequences, got %d", len(seqs))
	}
	ids := map[string]bool{}
	for _, s := range seqs {
		m := s.(map[string]any)
		if m["type"] != "pointer" || m["parameters"].(map[string]any)["pointerType"] != "touch" {
			t.Fatalf("bad seq %v", m)
		}
		ids[m["id"].(string)] = true
	}
	if len(ids) != 2 {
		t.Fatal("pointer ids not distinct")
	}
	if err := b.Tap(context.Background(), 1, 2); err != nil {
		t.Fatal(err)
	}
	if len(lastActions(f)) != 1 {
		t.Fatal("tap should be one sequence")
	}
}

type baseDev struct {
	device.Device
	taps int
}

func (d *baseDev) Tap(ctx context.Context, x, y int) error { d.taps++; return nil }

func TestWrapRouting(t *testing.T) {
	b, f, _ := startedBridge(t)
	base := &baseDev{}
	w := Wrap(base, b)
	if err := w.Tap(context.Background(), 5, 6); err != nil {
		t.Fatal(err)
	}
	if base.taps != 0 || f.count() != 1 {
		t.Fatalf("expected bridge routing: taps=%d actions=%d", base.taps, f.count())
	}
	b.Stop(context.Background())
	if err := w.Tap(context.Background(), 5, 6); err != nil {
		t.Fatal(err)
	}
	if base.taps != 1 {
		t.Fatal("expected base routing when stopped")
	}
	if err := w.(device.GestureExtras).Pinch(context.Background(), 1, 1, 1, 1); err != device.ErrUnsupported {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
}

func TestWrapFallbackOnError(t *testing.T) {
	b, f, _ := startedBridge(t)
	f.srv.Close() // transport failure
	base := &baseDev{}
	w := Wrap(base, b)
	if err := w.Tap(context.Background(), 1, 1); err != nil || base.taps != 1 {
		t.Fatalf("fallback failed: %v taps=%d", err, base.taps)
	}
	if b.Status().LastError == "" {
		t.Fatal("LastError not marked")
	}
}

func TestAssetsStatus(t *testing.T) {
	d := t.TempDir()
	a := &Assets{Dir: d}
	if _, _, ok := a.Status(); ok {
		t.Fatal("empty dir should not be ok")
	}
	os.WriteFile(filepath.Join(d, "appium-uiautomator2-server-v1.2.3.apk"), nil, 0o644)
	if _, _, ok := a.Status(); ok {
		t.Fatal("test apk missing should not be ok")
	}
	os.WriteFile(filepath.Join(d, "appium-uiautomator2-server-debug-androidTest.apk"), nil, 0o644)
	s, tp, ok := a.Status()
	if !ok || !strings.HasSuffix(s, "v1.2.3.apk") || !strings.HasSuffix(tp, "androidTest.apk") {
		t.Fatalf("%q %q %v", s, tp, ok)
	}
}

func TestAssetsDownload(t *testing.T) {
	apk := append([]byte("PK\x03\x04"), []byte("data")...)
	bad := false
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/rel", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v9", "assets": []map[string]string{
			{"name": "appium-uiautomator2-server-v9.apk", "browser_download_url": base + "/a"},
			{"name": "appium-uiautomator2-server-debug-androidTest.apk", "browser_download_url": base + "/b"},
		}})
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) { w.Write(apk) })
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		if bad {
			w.Write([]byte("<html>"))
			return
		}
		w.Write(apk)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL
	a := &Assets{Dir: filepath.Join(t.TempDir(), "x"), apiURL: srv.URL + "/rel"}
	bad = true
	if err := a.Download(context.Background()); err == nil || !strings.Contains(err.Error(), "bad magic") {
		t.Fatalf("expected magic error, got %v", err)
	}
	bad = false
	if err := a.Download(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := a.Status(); !ok {
		t.Fatal("assets not found after download")
	}
}
