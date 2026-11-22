package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pocketlab/internal/config"
	"pocketlab/internal/lifecycle"
	"pocketlab/internal/registry"
)

func lifeRouter(t *testing.T) (http.Handler, *lifecycle.Recorder, chan struct{}) {
	t.Helper()
	dir := t.TempDir()
	rec := lifecycle.Begin(dir, lifecycle.Info{Version: "9.9", Addr: "127.0.0.1:1", StartedVia: "pocketlabd", Owner: "cli"})
	done := make(chan struct{}, 1)
	h := NewRouterWithDeps(registry.New(), config.Default(), Deps{Lifecycle: rec, Shutdown: func() { done <- struct{}{} }})
	return h, rec, done
}

func TestServerStatusAndHistory(t *testing.T) {
	h, rec, _ := lifeRouter(t)
	defer rec.Stop("test")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/server/status", nil))
	var st lifecycle.Status
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &st) != nil || !st.Running || st.PID == 0 || st.Version != "9.9" || st.StartedVia != "pocketlabd" {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/server/history?limit=5", nil))
	var res struct{ Events []lifecycle.Event }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &res) != nil || len(res.Events) != 1 || res.Events[0].Event != lifecycle.KindStart {
		t.Fatalf("history %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/server/history?limit=abc", nil))
	if w.Code != 400 {
		t.Errorf("bad limit should be 400, got %d", w.Code)
	}
}

func TestShutdownOriginRecorded(t *testing.T) {
	h, rec, done := lifeRouter(t)
	r := httptest.NewRequest("POST", "/api/v1/server/shutdown", strings.NewReader(`{"origin":"ignored-when-header-set","reason":"user clicked Quit"}`))
	r.Host = "127.0.0.1:8700"
	r.Header.Set("X-Pocketlab-Action", "shutdown")
	r.Header.Set(OriginHeader, "web-ui")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("shutdown: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown callback not invoked")
	}
	rec.Stop("fallback must not win")
	ev := rec.History(1)[0]
	if ev.Event != lifecycle.KindStop || ev.Origin != "web-ui" || !strings.Contains(ev.Reason, "shutdown requested via API by web-ui") || !strings.Contains(ev.Reason, "user clicked Quit") {
		t.Errorf("stop event: %+v", ev)
	}
}

func TestShutdownOriginFromBodyAndDefault(t *testing.T) {
	h, rec, _ := lifeRouter(t)
	r := httptest.NewRequest("POST", "/api/v1/server/shutdown", strings.NewReader(`{"origin":"desktop"}`))
	r.Host = "127.0.0.1:8700"
	r.Header.Set("X-Pocketlab-Action", "shutdown")
	h.ServeHTTP(httptest.NewRecorder(), r)
	rec.Stop("x")
	if ev := rec.History(1)[0]; ev.Origin != "desktop" {
		t.Errorf("body origin: %+v", ev)
	}

	h2, rec2, _ := lifeRouter(t)
	r = httptest.NewRequest("POST", "/api/v1/server/shutdown", nil)
	r.Host = "127.0.0.1:8700"
	r.Header.Set("X-Pocketlab-Action", "shutdown")
	h2.ServeHTTP(httptest.NewRecorder(), r)
	rec2.Stop("x")
	if ev := rec2.History(1)[0]; ev.Origin != "unknown" {
		t.Errorf("default origin: %+v", ev)
	}
}

func TestStatusNotMountedWithoutLifecycle(t *testing.T) {
	h := NewRouterWithDeps(registry.New(), config.Default(), Deps{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/server/status", nil))
	if w.Code != 404 && w.Code != 405 {
		t.Errorf("expected 404/405, got %d", w.Code)
	}
}

// Clients built before the PhoneForge -> Pocketlab rename still send the old
// header names; the server must keep accepting them.
func TestShutdownAcceptsLegacyHeaders(t *testing.T) {
	h, rec, done := lifeRouter(t)
	r := httptest.NewRequest("POST", "/api/v1/server/shutdown", nil)
	r.Host = "127.0.0.1:8700"
	r.Header.Set("X-PhoneForge-Action", "shutdown")
	r.Header.Set("X-PhoneForge-Origin", "old-desktop")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("legacy shutdown: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown callback not invoked")
	}
	rec.Stop("fallback must not win")
	if ev := rec.History(1)[0]; ev.Origin != "old-desktop" {
		t.Errorf("origin from legacy header: %+v", ev)
	}
}

func TestShutdownRejectsMissingAction(t *testing.T) {
	h, _, _ := lifeRouter(t)
	r := httptest.NewRequest("POST", "/api/v1/server/shutdown", nil)
	r.Host = "127.0.0.1:8700"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
}

func TestWantRawMJPEGLegacyAccept(t *testing.T) {
	for _, accept := range []string{"application/x-pl-mjpeg", "application/x-pf-mjpeg"} {
		r := httptest.NewRequest("GET", "/x", nil)
		r.Header.Set("Accept", accept)
		if !wantRawMJPEG(r) {
			t.Errorf("Accept %q should select the raw stream", accept)
		}
	}
	if wantRawMJPEG(httptest.NewRequest("GET", "/x", nil)) {
		t.Error("default request must not be raw")
	}
}
