package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pocketlab/internal/config"
	"pocketlab/internal/device"
	"pocketlab/internal/iosbridge"
	"pocketlab/internal/registry"
)

type wdaFakeRun struct {
	xcodeOK bool
	build   func(w io.Writer) error
}

func (f wdaFakeRun) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	switch name {
	case "xcode-select":
		if f.xcodeOK {
			return []byte("/Applications/Xcode.app/Contents/Developer\n"), nil
		}
		return []byte("/Library/Developer/CommandLineTools\n"), nil
	case "xcodebuild":
		return []byte("Xcode 27.2\n"), nil
	}
	return nil, errors.New("none")
}

func (f wdaFakeRun) Stream(ctx context.Context, dir string, w io.Writer, name string, args ...string) error {
	if f.build != nil {
		return f.build(w)
	}
	return nil
}

func wdaTestBuilder(t *testing.T, run wdaFakeRun) *iosbridge.Builder {
	b := iosbridge.NewBuilder(filepath.Join(t.TempDir(), "settings.json"))
	b.Run = run
	b.Home = t.TempDir()
	b.GOOS = "darwin"
	b.UserHome = t.TempDir()
	b.Env = func(string) string { return "" }
	b.Prepare = func(ctx context.Context, h string, note func(string)) error {
		return os.MkdirAll(iosbridge.SrcDir(h), 0o755)
	}
	return b
}

func TestWDABuildRoutes(t *testing.T) {
	var b *iosbridge.Builder
	b = wdaTestBuilder(t, wdaFakeRun{xcodeOK: true, build: func(w io.Writer) error {
		io.WriteString(w, "CompileC one\nLd two\n")
		return os.MkdirAll(b.RunnerApp(), 0o755)
	}})
	h := NewRouterWithDeps(registry.New(), config.Config{Version: "t"}, Deps{WDABuild: WDABuildRoutes(b)})
	do := func(method, path, body string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		h.ServeHTTP(rec, httptest.NewRequest(method, "/api/v1/ios/wda"+path, rd))
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}

	code, m := do("GET", "/", "")
	env := m["env"].(map[string]any)
	if code != 200 || env["supported"] != true || env["runnerBuilt"] != false || env["teamIssue"] == nil {
		t.Fatalf("info: %d %v", code, m)
	}
	if bs := m["build"].(map[string]any); bs["state"] != "idle" {
		t.Fatalf("build state %v", bs["state"])
	}

	// no team -> structured 409
	code, m = do("POST", "/build", `{}`)
	if code != http.StatusConflict || m["code"] != iosbridge.CodeTeamNotFound || m["hint"] == "" || m["error"] == "" {
		t.Fatalf("no team: %d %v", code, m)
	}
	// invalid team -> 400 TEAM_INVALID, nothing saved
	code, m = do("POST", "/build", `{"teamId":"bad"}`)
	if code != http.StatusBadRequest || m["code"] != iosbridge.CodeTeamInvalid {
		t.Fatalf("invalid team: %d %v", code, m)
	}
	if code, m = do("PUT", "/team", `{"teamId":"bad"}`); code != http.StatusBadRequest || m["code"] != iosbridge.CodeTeamInvalid {
		t.Fatalf("put invalid: %d %v", code, m)
	}
	if code, m = do("PUT", "/team", `{"teamId":"abcde12345"}`); code != 200 || m["teamId"] != "ABCDE12345" {
		t.Fatalf("put: %d %v", code, m)
	}
	// start with the saved team
	code, m = do("POST", "/build", `{"mode":"device"}`)
	if code != http.StatusAccepted || m["state"] != "running" {
		t.Fatalf("start: %d %v", code, m)
	}
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	b.Wait(ctx)
	code, m = do("GET", "/build", "")
	if code != 200 || m["state"] != "succeeded" || m["team"] != "ABCDE12345" || m["runnerApp"] == "" {
		t.Fatalf("status: %d %v", code, m)
	}
	lines := m["lines"].([]any)
	if len(lines) == 0 || int(m["next"].(float64)) != len(lines) {
		t.Fatalf("lines: %v next=%v", lines, m["next"])
	}
	if code, m = do("GET", "/build?since="+jsonNum(m["next"]), ""); code != 200 || len(m["lines"].([]any)) != 0 {
		t.Fatalf("since: %d %v", code, m)
	}
	if code, _ = do("GET", "/build?since=x", ""); code != 400 {
		t.Fatalf("bad since: %d", code)
	}
	if code, m = do("POST", "/build/cancel", ""); code != http.StatusConflict {
		t.Fatalf("cancel idle: %d %v", code, m)
	}
	if code, m = do("POST", "/build", `{"mode":"bogus"}`); code != http.StatusBadRequest {
		t.Fatalf("bad mode: %d %v", code, m)
	}

	// SSE of a finished build: replays log lines + final status, then ends
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/ios/wda/build/stream", nil))
	body := rec.Body.String()
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(body, "event: log") || !strings.Contains(body, "CompileC one") || !strings.Contains(body, `"state":"succeeded"`) {
		t.Fatalf("sse: %q", body)
	}
}

func jsonNum(v any) string {
	b, _ := json.Marshal(v)
	return string(bytes.TrimSpace(b))
}

func TestWDABuildXcodeMissingAndBusy(t *testing.T) {
	b := wdaTestBuilder(t, wdaFakeRun{xcodeOK: false})
	h := NewRouterWithDeps(registry.New(), config.Config{Version: "t"}, Deps{WDABuild: WDABuildRoutes(b)})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/ios/wda/build", strings.NewReader(`{"mode":"sim"}`)))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if rec.Code != http.StatusConflict || (m["code"] != iosbridge.CodeXcodeMissing && m["code"] != iosbridge.CodeXcodeNotSelected) || !strings.Contains(m["hint"].(string), "xcode-select") {
		t.Fatalf("%d %v", rec.Code, m)
	}

	// busy -> 409 BUILD_BUSY
	release := make(chan struct{})
	b2 := wdaTestBuilder(t, wdaFakeRun{xcodeOK: true, build: func(w io.Writer) error { <-release; return nil }})
	h2 := NewRouterWithDeps(registry.New(), config.Config{Version: "t"}, Deps{WDABuild: WDABuildRoutes(b2)})
	post := func() (int, map[string]any) {
		rec := httptest.NewRecorder()
		h2.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/ios/wda/build", strings.NewReader(`{"mode":"sim"}`)))
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	if code, _ := post(); code != http.StatusAccepted {
		t.Fatalf("first: %d", code)
	}
	if code, m := post(); code != http.StatusConflict || m["code"] != iosbridge.CodeBuildBusy {
		t.Fatalf("second: %d %v", code, m)
	}
	close(release)
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	b2.Wait(ctx)
}

func TestIOSReadinessAndInstallIssue(t *testing.T) {
	reg := registry.New()
	reg.AddDiscoverer(serialDisc{serialDev{info: device.Info{ID: "ios-1", Serial: "UDID1", Name: "I", Platform: device.PlatformIOS}}})
	if err := reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctl := NewIOSControl(iosbridge.NewManagerWithDeps(t.TempDir(), iosbridge.Deps{Installer: fakeInstaller{ok: true}}))
	f := false
	ctl.Readiness = func(udid string) iosbridge.Readiness {
		return iosbridge.Readiness{Reachable: true, DeveloperMode: &f, Issue: &iosbridge.Issue{Code: iosbridge.CodeDeveloperModeOff, Message: "off"}}
	}
	ctl.Install = func(udid, app string) error { return errors.New("install failed: boom") }
	h := NewRouterWithDeps(reg, config.Config{Version: "t"}, Deps{IOS: ctl})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/devices/ios-1/iosbridge/readiness", nil))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if rec.Code != 200 || m["developerMode"] != false || m["issue"].(map[string]any)["code"] != iosbridge.CodeDeveloperModeOff {
		t.Fatalf("readiness: %d %v", rec.Code, m)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/devices/ios-1/iosbridge/install", nil))
	m = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if rec.Code != http.StatusConflict || m["code"] != iosbridge.CodeDeveloperNotTrust || m["hint"] == "" {
		t.Fatalf("install issue: %d %v", rec.Code, m)
	}
}
