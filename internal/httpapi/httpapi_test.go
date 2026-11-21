package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"phoneforge/internal/config"
	"phoneforge/internal/device"
	"phoneforge/internal/registry"
)

type stubDev struct{}

func (stubDev) Info() device.Info {
	return device.Info{ID: "d1", Name: "Stub", Platform: device.PlatformAndroid, State: "connected"}
}
func (stubDev) Screenshot(context.Context) (device.Screenshot, error) {
	return device.Screenshot{PNG: []byte("png")}, nil
}
func (stubDev) UITree(context.Context) (device.Element, error) {
	return device.Element{Role: "root"}, nil
}
func (stubDev) Tap(context.Context, int, int) error                  { return nil }
func (stubDev) LongPress(context.Context, int, int, int) error       { return nil }
func (stubDev) Swipe(context.Context, int, int, int, int, int) error { return nil }
func (stubDev) TypeText(context.Context, string) error               { return nil }
func (stubDev) PressKey(context.Context, device.Key) error           { return nil }
func (stubDev) ListApps(context.Context) ([]device.AppInfo, error) {
	return nil, nil
}
func (stubDev) LaunchApp(context.Context, string) error { return nil }
func (stubDev) StopApp(context.Context, string) error   { return nil }

type stubDisc struct{}

func (stubDisc) Discover(context.Context) ([]device.Device, error) {
	return []device.Device{stubDev{}}, nil
}

func newTestRouter(t *testing.T, token string) http.Handler {
	t.Helper()
	reg := registry.New()
	reg.AddDiscoverer(stubDisc{})
	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewRouter(reg, config.Config{Version: "test", APIToken: token})
}

func TestHealth(t *testing.T) {
	h := newTestRouter(t, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/health", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["service"] != "phoneforge" || m["deviceCount"].(float64) != 1 {
		t.Fatalf("bad body %v", m)
	}
}

func TestDevices(t *testing.T) {
	h := newTestRouter(t, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/devices", nil))
	var list []device.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "d1" {
		t.Fatalf("bad list %v", list)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/devices/nope", nil))
	if rec.Code != 404 {
		t.Fatalf("want 404 got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/devices/d1", nil))
	if rec.Code != 200 {
		t.Fatalf("want 200 got %d", rec.Code)
	}
}

func TestCORS(t *testing.T) {
	h := newTestRouter(t, "")
	for origin, want := range map[string]string{
		"http://localhost:8700": "http://localhost:8700",
		"http://evil.example":   "",
	} {
		req := httptest.NewRequest("OPTIONS", "/api/v1/devices", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 204 {
			t.Fatalf("preflight %d", rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != want {
			t.Fatalf("origin %s: got %q want %q", origin, got, want)
		}
		if rec.Header().Get("Access-Control-Allow-Private-Network") != "" {
			t.Fatal("PNA header must not be set")
		}
	}
}

func TestAuth(t *testing.T) {
	h := newTestRouter(t, "secret")
	do := func(remote, key string) int {
		req := httptest.NewRequest("GET", "/api/v1/devices", nil)
		req.RemoteAddr = remote
		if key != "" {
			req.Header.Set("X-API-Key", key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if do("10.0.0.5:1234", "") != 401 {
		t.Fatal("remote without key should be 401")
	}
	if do("10.0.0.5:1234", "secret") != 200 {
		t.Fatal("remote with key should pass")
	}
	if do("127.0.0.1:1234", "") != 200 {
		t.Fatal("loopback should pass")
	}
}
