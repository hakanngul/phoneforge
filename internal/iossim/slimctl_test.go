package iossim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const slimListJSON = `{"devices":{"com.apple.CoreSimulator.SimRuntime.iOS-27-2":[
 {"udid":"AAAA-1","name":"Test","state":"Booted","isAvailable":true,"deviceTypeIdentifier":"com.apple.CoreSimulator.SimDeviceType.iPhone-17"}],
 "com.apple.CoreSimulator.SimRuntime.watchOS-27-2":[
 {"udid":"WWWW","name":"Watch","state":"Booted","isAvailable":true}]}}`

type slimFake struct {
	mu    sync.Mutex
	calls []string
}

func (f *slimFake) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	f.mu.Unlock()
	switch {
	case args[0] == "list":
		return []byte(slimListJSON), nil
	case args[0] == "spawn" && len(args) > 3 && args[3] == "manageruid":
		return []byte("501\n"), nil
	case args[0] == "spawn" && len(args) > 3 && args[3] == "list":
		return []byte("PID\tStatus\tLabel\n123\t0\tcom.apple.assistantd\n-\t0\tcom.apple.chronod\n"), nil
	}
	return nil, nil
}

func (f *slimFake) has(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == s {
			return true
		}
	}
	return false
}

func newSlimServer(t *testing.T) (*httptest.Server, *slimFake) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("slimming is macOS only")
	}
	f := &slimFake{}
	c := New(t.TempDir())
	c.Run = f.run
	c.Host = func(_ context.Context, name string, args ...string) ([]byte, error) { return nil, nil }
	c.SetSlimPath(filepath.Join(t.TempDir(), "iossim-slim.json"))
	r := http.NewServeMux()
	r.Handle("/simulators/", http.StripPrefix("/simulators", c.Routes()))
	return httptest.NewServer(r), f
}

func post(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestSlimRoutes(t *testing.T) {
	ts, f := newSlimServer(t)
	defer ts.Close()

	// dry run changes nothing
	code, m := post(t, ts.URL+"/simulators/AAAA-1/slim", `{"dryRun":true}`)
	if code != 200 || m["dryRun"] != true || m["profile"] != "minimal-safe" {
		t.Fatalf("dry: %d %v", code, m)
	}
	if f.has("spawn AAAA-1 launchctl disable user/501/com.apple.assistantd") {
		t.Fatal("dry run disabled a label")
	}

	code, m = post(t, ts.URL+"/simulators/AAAA-1/slim", `{}`)
	if code != 200 {
		t.Fatalf("slim: %d %v", code, m)
	}
	if !f.has("spawn AAAA-1 launchctl disable user/501/com.apple.assistantd") || !f.has("spawn AAAA-1 launchctl bootout user/501/com.apple.chronod") {
		t.Fatalf("calls: %v", f.calls)
	}
	if n := len(m["applied"].([]any)); n != 2 {
		t.Fatalf("applied: %v", m["applied"])
	}
	if len(m["missing"].([]any)) == 0 {
		t.Fatal("expected missing labels (fake sim only has two)")
	}

	resp, err := http.Get(ts.URL + "/simulators/AAAA-1/slim")
	if err != nil {
		t.Fatal(err)
	}
	var st SlimStatus
	_ = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if !st.Slimmed || st.Profile != "minimal-safe" || st.Disabled != 2 {
		t.Fatalf("status: %+v", st)
	}

	code, m = post(t, ts.URL+"/simulators/AAAA-1/unslim", ``)
	if code != 200 || len(m["enabled"].([]any)) != 2 || m["rebooted"] != true {
		t.Fatalf("unslim: %d %v", code, m)
	}
	if !f.has("spawn AAAA-1 launchctl enable user/501/com.apple.assistantd") || !f.has("shutdown AAAA-1") {
		t.Fatalf("calls: %v", f.calls)
	}
	code, m = post(t, ts.URL+"/simulators/AAAA-1/unslim", ``)
	if code != http.StatusConflict || m["code"] != "SIMULATOR_NOT_SLIMMED" {
		t.Fatalf("second unslim: %d %v", code, m)
	}
}

func TestSlimRouteErrors(t *testing.T) {
	ts, _ := newSlimServer(t)
	defer ts.Close()
	if code, m := post(t, ts.URL+"/simulators/WWWW/slim", `{}`); code != http.StatusBadRequest || m["code"] != "SIMULATOR_NOT_IOS" {
		t.Fatalf("watch: %d %v", code, m)
	}
	if code, m := post(t, ts.URL+"/simulators/NOPE/slim", `{}`); code != http.StatusNotFound {
		t.Fatalf("missing: %d %v", code, m)
	}
	if code, m := post(t, ts.URL+"/simulators/AAAA-1/slim", `{"groups":["bogus"]}`); code != http.StatusBadRequest || m["code"] != "INVALID_REQUEST" {
		t.Fatalf("bad group: %d %v", code, m)
	}
	// no booted launchd_sim for the fake udid
	resp, err := http.Get(ts.URL + "/simulators/AAAA-1/memory")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("memory: %d", resp.StatusCode)
	}
}
