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
	"phoneforge/internal/uia2"
)

type serialDev struct {
	stubDev
	info device.Info
}

func (d serialDev) Info() device.Info { return d.info }

type serialDisc []device.Device

func (s serialDisc) Discover(context.Context) ([]device.Device, error) { return s, nil }

func TestBridgeRoutes(t *testing.T) {
	reg := registry.New()
	reg.AddDiscoverer(serialDisc{
		serialDev{info: device.Info{ID: "android-X", Serial: "X", Name: "A", Platform: device.PlatformAndroid}},
		serialDev{info: device.Info{ID: "ios-1", Serial: "1", Name: "I", Platform: device.PlatformIOS}},
	})
	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	h := NewRouterWithDeps(reg, config.Config{Version: "t"}, Deps{Bridges: uia2.NewManager("/nonexistent-adb", dir)})
	get := func(path string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	code, m := get("/api/v1/devices/android-X/bridge")
	if code != 200 || m["supported"] != true || m["running"] != false || m["installed"] != false || m["since"] != nil {
		t.Fatalf("android: %d %v", code, m)
	}
	code, m = get("/api/v1/devices/ios-1/bridge")
	if code != 200 || m["supported"] != false {
		t.Fatalf("ios: %d %v", code, m)
	}
	code, m = get("/api/v1/bridge/assets")
	if code != 200 || m["ok"] != false || m["dir"] != dir {
		t.Fatalf("assets: %d %v", code, m)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/devices/ios-1/bridge/start", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("ios start: %d", rec.Code)
	}
}
