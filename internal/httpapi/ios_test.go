package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"phoneforge/internal/config"
	"phoneforge/internal/device"
	"phoneforge/internal/iosbridge"
	"phoneforge/internal/registry"
)

type fakeInstaller struct{ ok bool }

func (f fakeInstaller) Installed(context.Context, string, string) (bool, error) { return f.ok, nil }

func TestIOSRoutes(t *testing.T) {
	reg := registry.New()
	reg.AddDiscoverer(serialDisc{
		serialDev{info: device.Info{ID: "android-X", Serial: "X", Name: "A", Platform: device.PlatformAndroid}},
		serialDev{info: device.Info{ID: "ios-1", Serial: "UDID1", Name: "I", Platform: device.PlatformIOS}},
	})
	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	mgr := iosbridge.NewManagerWithDeps(t.TempDir(), iosbridge.Deps{Installer: fakeInstaller{ok: true}})
	ctl := NewIOSControl(mgr)
	var gotUDID string
	ctl.Install = func(udid, app string) error { gotUDID = udid; return iosbridge.ErrRunnerNotBuilt }
	h := NewRouterWithDeps(reg, config.Config{Version: "t"}, Deps{IOS: ctl})

	do := func(method, path string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	code, m := do("GET", "/api/v1/devices/ios-1/iosbridge/status")
	if code != 200 || m["state"] != "stopped" || m["installed"] != true {
		t.Fatalf("status: %d %v", code, m)
	}
	if code, _ = do("GET", "/api/v1/devices/android-X/iosbridge/status"); code != 404 {
		t.Fatalf("android status: %d", code)
	}
	code, m = do("GET", "/api/v1/devices/ios-1/bridge")
	if code != 200 || m["supported"] != true || m["running"] != false || m["platform"] != "ios" {
		t.Fatalf("generic bridge: %d %v", code, m)
	}
	code, m = do("POST", "/api/v1/devices/ios-1/iosbridge/install")
	if code != http.StatusConflict || m["code"] != "IOSBRIDGE_RUNNER_NOT_BUILT" || gotUDID != "UDID1" {
		t.Fatalf("install: %d %v udid=%q", code, m, gotUDID)
	}
	if code, _ = do("POST", "/api/v1/devices/android-X/iosbridge/install"); code != 404 {
		t.Fatalf("android install: %d", code)
	}
	if code, m = do("GET", "/api/v1/devices/ios-1/mjpeg"); code != http.StatusConflict || m["code"] != "IOSBRIDGE_NOT_RUNNING" {
		t.Fatalf("mjpeg: %d %v", code, m)
	}
	if code, _ = do("GET", "/api/v1/devices/android-X/mjpeg"); code != http.StatusNotImplemented {
		t.Fatalf("android mjpeg: %d", code)
	}
}

func TestIOSControlWrap(t *testing.T) {
	ctl := NewIOSControl(iosbridge.NewManagerWithDeps(t.TempDir(), iosbridge.Deps{Installer: fakeInstaller{}}))
	and := serialDev{info: device.Info{ID: "android-X", Serial: "X", Platform: device.PlatformAndroid}}
	if ctl.Wrap(and) != device.Device(and) {
		t.Fatal("android device must not be wrapped")
	}
	ios := serialDev{info: device.Info{ID: "ios-1", Serial: "U", Platform: device.PlatformIOS}}
	if w := ctl.Wrap(ios); w == device.Device(ios) || w.Info().ID != "ios-1" {
		t.Fatal("ios device must be wrapped")
	}
	if ctl.Client("U") != ctl.Client("U") {
		t.Fatal("client must be cached per udid")
	}
	if ctl.Active("U")() {
		t.Fatal("stopped bridge must not be active")
	}
}
