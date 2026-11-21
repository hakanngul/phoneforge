package remotenode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"phoneforge/internal/device"
)

func fakeNode(t *testing.T, gotKey *string, gotTap *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "9.9", "deviceCount": 1})
	})
	mux.HandleFunc("/api/v1/devices", func(w http.ResponseWriter, r *http.Request) {
		*gotKey = r.Header.Get("X-API-Key")
		_ = json.NewEncoder(w).Encode([]device.Info{{ID: "abc", Name: "Pixel", Platform: device.PlatformAndroid, State: "connected"}})
	})
	mux.HandleFunc("/api/v1/devices/abc/screenshot", func(w http.ResponseWriter, _ *http.Request) {
		var buf bytes.Buffer
		_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 7, 5)))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(buf.Bytes())
	})
	mux.HandleFunc("/api/v1/devices/abc/tap", func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r.Body)
		*gotTap = strings.TrimSpace(b.String())
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverAndProxy(t *testing.T) {
	var key, tap string
	srv := fakeNode(t, &key, &tap)
	st, err := New(filepath.Join(t.TempDir(), "nodes.json"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.Add("lab", srv.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	// An unreachable node must not break discovery.
	if _, err := st.Add("dead", "http://127.0.0.1:1", ""); err != nil {
		t.Fatal(err)
	}
	devs, err := NewDiscoverer(st).Discover(context.Background())
	if err != nil || len(devs) != 1 {
		t.Fatalf("devs=%d err=%v", len(devs), err)
	}
	info := devs[0].Info()
	if info.ID != "remote-"+n.ID+"-abc" || info.Name != "Pixel @lab" || info.Platform != device.PlatformAndroid || info.State != "connected" {
		t.Fatalf("bad info %+v", info)
	}
	if key != "secret" {
		t.Fatalf("api key not sent: %q", key)
	}
	shot, err := devs[0].Screenshot(context.Background())
	if err != nil || shot.Width != 7 || shot.Height != 5 || len(shot.PNG) == 0 {
		t.Fatalf("shot %+v err=%v", shot, err)
	}
	if err := devs[0].Tap(context.Background(), 3, 4); err != nil || tap != `{"x":3,"y":4}` {
		t.Fatalf("tap %q err=%v", tap, err)
	}
}

func TestStorePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "nodes.json")
	st, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := st.Add("a", "http://100.1.2.3:8700/", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if a.Addr != "http://100.1.2.3:8700" {
		t.Fatalf("addr %q", a.Addr)
	}
	if _, err := st.Add("dup", "http://100.1.2.3:8700", ""); err == nil {
		t.Fatal("expected dedupe error")
	}
	if _, err := st.Add("bad", "ftp://x", ""); err == nil {
		t.Fatal("expected invalid addr error")
	}
	b, _ := st.Add("b", "http://100.1.2.4:8700", "")
	st2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st2.List()) != 2 {
		t.Fatalf("reload: %+v", st2.List())
	}
	if got, ok := st2.Get(a.ID); !ok || got.Token != "tok" {
		t.Fatal("token not persisted")
	}
	if err := st2.Remove(b.ID); err != nil {
		t.Fatal(err)
	}
	if err := st2.Remove(b.ID); err == nil {
		t.Fatal("expected not found")
	}
	st3, _ := New(path)
	if len(st3.List()) != 1 {
		t.Fatalf("after remove: %+v", st3.List())
	}
}

func TestRoutes(t *testing.T) {
	var key, tap string
	srv := fakeNode(t, &key, &tap)
	st, _ := New(filepath.Join(t.TempDir(), "n.json"))
	h := st.Routes()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"lab","addr":"`+srv.URL+`","token":"s3"}`)))
	if rec.Code != 201 || strings.Contains(rec.Body.String(), "s3") {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	var nv nodeView
	_ = json.Unmarshal(rec.Body.Bytes(), &nv)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), `"token":"set"`) {
		t.Fatalf("list: %s", rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/"+nv.ID+"/test", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"deviceCount":1`) {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("DELETE", "/"+nv.ID, nil))
	if rec.Code != 200 {
		t.Fatalf("delete: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("DELETE", "/"+nv.ID, nil))
	if rec.Code != 404 {
		t.Fatalf("delete2: %d", rec.Code)
	}
}

func TestRemoteOptionalInterfaces(t *testing.T) {
	var gotPath, gotBody, gotUpload string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/devices/abc/stream.h264", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte{0, 0, 0, 1, 0x67})
	})
	mux.HandleFunc("/api/v1/devices/abc/logs", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("data: hello\n\ndata: world\n\n"))
	})
	mux.HandleFunc("/api/v1/devices/abc/clipboard", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"text":"clip"}`))
	})
	mux.HandleFunc("/api/v1/devices/abc/install-app", func(w http.ResponseWriter, r *http.Request) {
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b, _ := io.ReadAll(f)
		gotUpload = string(b)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/v1/devices/abc/apps/", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/v1/devices/abc/drag", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/v1/devices/abc/recording", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("mp4:" + r.URL.Query().Get("seconds")))
	})
	mux.HandleFunc("/api/v1/devices/abc/open-url", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"code":"UNSUPPORTED"}`, http.StatusNotImplemented)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	d := newRemoteDevice(Node{ID: "n", Name: "lab", Addr: srv.URL}, device.Info{ID: "abc", Name: "P"})
	ctx := context.Background()

	var vid bytes.Buffer
	if err := d.StreamH264(ctx, &vid); err != nil || !bytes.Equal(vid.Bytes(), []byte{0, 0, 0, 1, 0x67}) {
		t.Fatalf("stream %v %v", vid.Bytes(), err)
	}
	var logs bytes.Buffer
	if err := d.StreamLogs(ctx, &logs); err != nil || logs.String() != "hello\nworld\n" {
		t.Fatalf("logs %q %v", logs.String(), err)
	}
	if txt, err := d.GetClipboard(ctx); err != nil || txt != "clip" {
		t.Fatalf("clip %q %v", txt, err)
	}
	apk := filepath.Join(t.TempDir(), "a.apk")
	_ = os.WriteFile(apk, []byte("APK"), 0o600)
	if err := d.InstallApp(ctx, apk); err != nil || gotUpload != "APK" {
		t.Fatalf("install %q %v", gotUpload, err)
	}
	if err := d.UninstallApp(ctx, "com.x"); err != nil || gotPath != "DELETE /api/v1/devices/abc/apps/com.x" {
		t.Fatalf("uninstall %q %v", gotPath, err)
	}
	if err := d.Drag(ctx, 1, 2, 3, 4, 5); err != nil || gotBody != `{"durationMs":5,"x1":1,"x2":3,"y1":2,"y2":4}` {
		t.Fatalf("drag %q %v", gotBody, err)
	}
	if b, err := d.RecordScreen(ctx, 4); err != nil || string(b) != "mp4:4" {
		t.Fatalf("record %q %v", b, err)
	}
	if err := d.OpenURL(ctx, "x"); !errors.Is(err, device.ErrUnsupported) {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
}
