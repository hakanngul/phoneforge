package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"pocketlab/internal/config"
	"pocketlab/internal/device"
	"pocketlab/internal/registry"
)

// fullDev implements every optional interface and records calls.
type fullDev struct {
	stubDev
	calls     []string
	clip      string
	installed []byte
	instPath  string
}

func (f *fullDev) Info() device.Info { return device.Info{ID: "full", Name: "Full"} }
func (f *fullDev) StreamH264(ctx context.Context, w io.Writer) error {
	_, err := w.Write([]byte{0, 0, 0, 1, 0x67})
	return err
}
func (f *fullDev) DoubleTap(_ context.Context, x, y int) error {
	f.calls = append(f.calls, "dt")
	return nil
}
func (f *fullDev) Drag(_ context.Context, x1, y1, x2, y2, ms int) error {
	f.calls = append(f.calls, "drag")
	return nil
}
func (f *fullDev) Pinch(_ context.Context, x, y int, s float64, ms int) error {
	f.calls = append(f.calls, "pinch")
	return nil
}
func (f *fullDev) OpenURL(_ context.Context, u string) error {
	f.calls = append(f.calls, "url:"+u)
	return nil
}
func (f *fullDev) DismissKeyboard(context.Context) error { f.calls = append(f.calls, "kb"); return nil }
func (f *fullDev) SetClipboard(_ context.Context, t string) error {
	f.clip = t
	return nil
}
func (f *fullDev) GetClipboard(context.Context) (string, error) { return f.clip, nil }
func (f *fullDev) InstallApp(_ context.Context, p string) error {
	f.instPath = p
	f.installed, _ = os.ReadFile(p)
	return nil
}
func (f *fullDev) UninstallApp(_ context.Context, id string) error {
	f.calls = append(f.calls, "uninstall:"+id)
	return nil
}
func (f *fullDev) SetPermission(_ context.Context, id, p string, g bool) error {
	f.calls = append(f.calls, "perm:"+id+":"+p)
	return nil
}
func (f *fullDev) StreamLogs(_ context.Context, w io.Writer) error {
	_, err := w.Write([]byte("line one\nline two\n"))
	return err
}
func (f *fullDev) RecordScreen(_ context.Context, s int) ([]byte, error) {
	return []byte("mp4data"), nil
}

type fullDisc struct{ d device.Device }

func (f fullDisc) Discover(context.Context) ([]device.Device, error) {
	return []device.Device{f.d}, nil
}

func routerFor(t *testing.T, d device.Device) http.Handler {
	t.Helper()
	reg := registry.New()
	reg.AddDiscoverer(fullDisc{d})
	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewRouter(reg, config.Config{Version: "test"})
}

func do(h http.Handler, method, path, ctype, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestExtrasSupported(t *testing.T) {
	f := &fullDev{}
	h := routerFor(t, f)
	base := "/api/v1/devices/full"

	rec := do(h, "GET", base+"/stream.h264", "", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "video/h264" || rec.Header().Get("Cache-Control") != "no-cache" ||
		!bytes.Equal(rec.Body.Bytes(), []byte{0, 0, 0, 1, 0x67}) {
		t.Fatalf("stream: %d %v %v", rec.Code, rec.Header(), rec.Body.Bytes())
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/double-tap", `{"x":1,"y":2}`},
		{"POST", "/drag", `{"x1":1,"y1":2,"x2":3,"y2":4,"durationMs":200}`},
		{"POST", "/pinch", `{"x":1,"y":2,"scale":0.5,"durationMs":200}`},
		{"POST", "/open-url", `{"url":"https://example.com"}`},
		{"POST", "/dismiss-keyboard", ``},
		{"POST", "/clipboard", `{"text":"hello"}`},
		{"POST", "/permissions", `{"bundleId":"com.x","permission":"CAMERA","grant":true}`},
		{"DELETE", "/apps/com.x", ``},
	} {
		if rec := do(h, c.method, base+c.path, "application/json", c.body); rec.Code != 200 {
			t.Fatalf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	if got := strings.Join(f.calls, ","); got != "dt,drag,pinch,url:https://example.com,kb,perm:com.x:CAMERA,uninstall:com.x" {
		t.Fatalf("calls = %s", got)
	}
	if rec := do(h, "POST", base+"/pinch", "application/json", `{"x":1,"y":2,"scale":0}`); rec.Code != 400 {
		t.Fatalf("pinch scale 0: %d", rec.Code)
	}
	rec = do(h, "GET", base+"/clipboard", "", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"text":"hello"`) {
		t.Fatalf("clipboard: %d %s", rec.Code, rec.Body)
	}

	rec = do(h, "GET", base+"/logs", "", "")
	if rec.Header().Get("Content-Type") != "text/event-stream" || rec.Body.String() != "data: line one\n\ndata: line two\n\n" {
		t.Fatalf("logs: %q %v", rec.Body, rec.Header())
	}
	rec = do(h, "GET", base+"/recording?seconds=3", "", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "video/mp4" ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") || rec.Body.String() != "mp4data" {
		t.Fatalf("recording: %d %v", rec.Code, rec.Header())
	}
}

func TestInstallApp(t *testing.T) {
	f := &fullDev{}
	h := routerFor(t, f)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "app.apk")
	_, _ = fw.Write([]byte("APKBYTES"))
	_ = mw.Close()
	rec := do(h, "POST", "/api/v1/devices/full/install-app", mw.FormDataContentType(), buf.String())
	if rec.Code != 200 || string(f.installed) != "APKBYTES" || !strings.HasSuffix(f.instPath, ".apk") {
		t.Fatalf("install: %d %s installed=%q path=%q", rec.Code, rec.Body, f.installed, f.instPath)
	}
	if _, err := os.Stat(f.instPath); !os.IsNotExist(err) {
		t.Fatal("temp file not removed")
	}
	// missing field
	rec = do(h, "POST", "/api/v1/devices/full/install-app", mw.FormDataContentType(), "--"+mw.Boundary()+"--\r\n")
	if rec.Code != 400 {
		t.Fatalf("missing file: %d", rec.Code)
	}
	// JSON path variant
	tmp := t.TempDir() + "/x.apk"
	_ = os.WriteFile(tmp, []byte("LOCAL"), 0o600)
	body, _ := json.Marshal(map[string]string{"path": tmp})
	rec = do(h, "POST", "/api/v1/devices/full/install-app", "application/json", string(body))
	if rec.Code != 200 || string(f.installed) != "LOCAL" {
		t.Fatalf("json install: %d %s", rec.Code, rec.Body)
	}
}

func TestExtrasUnsupported(t *testing.T) {
	h := routerFor(t, &stubDev{})
	base := "/api/v1/devices/d1"
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/stream.h264", ""},
		{"POST", "/double-tap", `{"x":1,"y":2}`},
		{"POST", "/drag", `{}`},
		{"POST", "/pinch", `{"scale":1}`},
		{"POST", "/open-url", `{"url":"x"}`},
		{"POST", "/dismiss-keyboard", ""},
		{"GET", "/clipboard", ""},
		{"POST", "/clipboard", `{"text":"x"}`},
		{"POST", "/install-app", `{"path":"/x.apk"}`},
		{"DELETE", "/apps/com.x", ""},
		{"POST", "/permissions", `{"bundleId":"a","permission":"b"}`},
		{"GET", "/logs", ""},
		{"GET", "/recording", ""},
	} {
		rec := do(h, c.method, base+c.path, "application/json", c.body)
		if rec.Code != 501 || !strings.Contains(rec.Body.String(), `"code":"UNSUPPORTED"`) {
			t.Fatalf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}

func TestFeatureDepsMounted(t *testing.T) {
	mk := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(name)) })
	}
	h := NewRouterWithDeps(registry.New(), config.Default(), Deps{AI: mk("ai"), OCR: mk("ocr"), Web: mk("web")})
	for _, name := range []string{"ai", "ocr", "web"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/"+name+"/x", nil))
		if rec.Body.String() != name {
			t.Errorf("/%s not mounted: %d %q", name, rec.Code, rec.Body.String())
		}
	}
}
