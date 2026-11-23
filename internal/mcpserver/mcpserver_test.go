package mcpserver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestNewRegistersTools(t *testing.T) {
	s := New("http://127.0.0.1:8700/api/v1", "test")
	if s == nil {
		t.Fatal("nil server")
	}
	if n := len(s.ListTools()); n != 55 {
		t.Fatalf("want 55 tools, got %d", n)
	}
	if Handler(s) == nil {
		t.Fatal("nil handler")
	}
}

func TestOCRAndWebToolsProxy(t *testing.T) {
	type hit struct{ method, path, body string }
	var hits []hit
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hits = append(hits, hit{r.Method, r.URL.Path, string(b)})
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	s := New(ts.URL+"/api/v1", "test")
	tools := s.ListTools()
	call := func(name string, args map[string]any) {
		t.Helper()
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("tool %s missing", name)
		}
		var req mcp.CallToolRequest
		req.Params.Name = name
		req.Params.Arguments = args
		res, err := tool.Handler(context.Background(), req)
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
	}
	call("ocr_screen", map[string]any{"id": "d1"})
	call("ocr_tap", map[string]any{"id": "d1", "text": "Sign in", "index": 1})
	call("web_targets", map[string]any{"id": "d1"})
	call("web_navigate", map[string]any{"id": "d1", "url": "https://example.com"})
	call("web_eval", map[string]any{"id": "d1", "script": "1+1", "targetId": "T9"})
	call("web_click", map[string]any{"id": "d1", "selector": "#go"})
	call("web_type", map[string]any{"id": "d1", "selector": "input", "text": "hi"})
	want := []hit{
		{"POST", "/api/v1/ocr/devices/d1", `{"words":false}`},
		{"POST", "/api/v1/ocr/devices/d1/tap", `{"text":"Sign in","index":1,"exact":false,"regex":false}`},
		{"GET", "/api/v1/web/devices/d1/targets", ``},
		{"POST", "/api/v1/web/devices/d1/navigate", `{"url":"https://example.com"}`},
		{"POST", "/api/v1/web/devices/d1/eval", `{"script":"1+1","targetId":"T9"}`},
		{"POST", "/api/v1/web/devices/d1/click", `{"selector":"#go"}`},
		{"POST", "/api/v1/web/devices/d1/type", `{"selector":"input","text":"hi"}`},
	}
	if len(hits) != len(want) {
		t.Fatalf("hits = %+v", hits)
	}
	for i, w := range want {
		if hits[i] != w {
			t.Errorf("hit %d = %+v want %+v", i, hits[i], w)
		}
	}
}

func TestUITreeCompactAndJSON(t *testing.T) {
	const tree = `{"role":"XCUIElementTypeApplication","bounds":{"x":0,"y":0,"w":393,"h":852},"enabled":true,"children":[` +
		`{"role":"XCUIElementTypeButton","label":"Ayarlar","resourceId":"com.apple.settings","bounds":{"x":16,"y":311,"w":370,"h":53},"clickable":true,"enabled":true}]}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(tree))
	}))
	defer ts.Close()
	s := New(ts.URL+"/api/v1", "test")
	h := s.ListTools()["ui_tree"].Handler
	call := func(args map[string]any) string {
		t.Helper()
		args["id"] = "d1"
		var req mcp.CallToolRequest
		req.Params.Arguments = args
		res, err := h(context.Background(), req)
		if err != nil || res.IsError {
			t.Fatalf("err=%v res=%+v", err, res)
		}
		return res.Content[0].(mcp.TextContent).Text
	}
	out := call(map[string]any{})
	if !strings.Contains(out, `[1] Button "Ayarlar" id=com.apple.settings @(201,337) 370x53 clickable`) || !strings.Contains(out, "device d1") {
		t.Fatalf("compact:\n%s", out)
	}
	if out := call(map[string]any{"filter": "nomatch"}); !strings.Contains(out, "0 nodes") {
		t.Fatalf("filter:\n%s", out)
	}
	if out := call(map[string]any{"format": "json"}); out != tree {
		t.Fatalf("json should be unchanged below the limit: %s", out)
	}
}

func TestSimulatorSlimTools(t *testing.T) {
	type hit struct{ Method, Path, Body string }
	var hits []hit
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hits = append(hits, hit{r.Method, r.URL.Path, string(b)})
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	s := New(ts.URL+"/api/v1", "test")
	tools := s.ListTools()
	call := func(name string, args map[string]any) {
		t.Helper()
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("tool %s missing", name)
		}
		var req mcp.CallToolRequest
		req.Params.Name = name
		req.Params.Arguments = args
		res, err := tool.Handler(context.Background(), req)
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
	}
	call("boot_simulator", map[string]any{"udid": "U"})
	call("boot_simulator", map[string]any{"udid": "U", "slim": true, "profile": "aggressive"})
	call("slim_simulator", map[string]any{"udid": "U", "groups": "siri, photos", "dry_run": true})
	call("unslim_simulator", map[string]any{"udid": "U"})
	call("simulator_memory", map[string]any{"udid": "U"})
	want := []hit{
		{"POST", "/api/v1/simulators/U/boot", ""},
		{"POST", "/api/v1/simulators/U/boot", ""},
		{"POST", "/api/v1/simulators/U/slim", `{"profile":"aggressive"}`},
		{"POST", "/api/v1/simulators/U/slim", `{"dryRun":true,"groups":["siri","photos"]}`},
		{"POST", "/api/v1/simulators/U/unslim", ""},
		{"GET", "/api/v1/simulators/U/memory", ""},
	}
	if len(hits) != len(want) {
		t.Fatalf("hits: %+v", hits)
	}
	for i := range want {
		if hits[i] != want[i] {
			t.Errorf("hit %d: got %+v want %+v", i, hits[i], want[i])
		}
	}
}

func TestSimulatorLifecycleToolsProxy(t *testing.T) {
	type hit struct{ method, path, body string }
	var hits []hit
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hits = append(hits, hit{r.Method, r.URL.RequestURI(), string(b)})
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	s := New(ts.URL+"/api/v1", "test")
	tools := s.ListTools()
	for _, name := range []string{"erase_simulator", "delete_simulator"} {
		if d := tools[name].Tool.Description; !strings.Contains(d, "DESTRUCTIVE") {
			t.Errorf("%s description must state it is destructive: %q", name, d)
		}
	}
	call := func(name string, args map[string]any) {
		t.Helper()
		var req mcp.CallToolRequest
		req.Params.Name = name
		req.Params.Arguments = args
		if res, err := tools[name].Handler(context.Background(), req); err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", name, err, res)
		}
	}
	call("delete_simulator", map[string]any{"udid": "U1", "shutdown": true})
	call("erase_simulator", map[string]any{"udid": "U1", "keep_slim": true})
	call("create_simulator", map[string]any{"device_type": "iPhone-17", "slim": true})
	call("clone_simulator", map[string]any{"udid": "U1", "count": 2, "slim": false})
	if hits[0].method != "DELETE" || hits[0].path != "/api/v1/simulators/U1?shutdown=1" {
		t.Errorf("delete: %+v", hits[0])
	}
	if hits[1].path != "/api/v1/simulators/U1/erase" || !strings.Contains(hits[1].body, `"keepSlim":true`) {
		t.Errorf("erase: %+v", hits[1])
	}
	if hits[2].path != "/api/v1/simulators" || !strings.Contains(hits[2].body, `"deviceType":"iPhone-17"`) || !strings.Contains(hits[2].body, `"slim":true`) {
		t.Errorf("create: %+v", hits[2])
	}
	if !strings.Contains(hits[3].body, `"slim":false`) || !strings.Contains(hits[3].body, `"count":2`) {
		t.Errorf("clone: %+v", hits[3])
	}
}
