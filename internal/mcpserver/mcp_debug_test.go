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

func TestCrashAndDebugToolsProxy(t *testing.T) {
	type hit struct{ method, path, query, body string }
	var hits []hit
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hits = append(hits, hit{r.Method, r.URL.Path, r.URL.RawQuery, string(b)})
		switch {
		case strings.HasSuffix(r.URL.Path, "/crashes"):
			_, _ = w.Write([]byte(`[{"name":"A-1.ips"}]`))
		case strings.HasSuffix(r.URL.Path, "/state"):
			_, _ = w.Write([]byte(`{"session":{"id":"s1"},"stop":{"state":"stopped"}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer ts.Close()
	tools := New(ts.URL+"/api/v1", "test").ListTools()
	call := func(name string, args map[string]any) *mcp.CallToolResult {
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
		return res
	}
	call("get_crash_reports", map[string]any{"id": "d1", "app": "com.x", "limit": 3})
	call("get_crash_reports", map[string]any{"id": "d1", "name": "A-1.ips"})
	call("get_crash_reports", map[string]any{"id": "d1", "include_text": true})
	call("debug_attach", map[string]any{"id": "sim-1", "bundle_id": "com.apple.Preferences", "launch": true})
	call("debug_breakpoint", map[string]any{"session": "s1", "action": "set", "file": "a.m", "line": 7})
	call("debug_breakpoint", map[string]any{"session": "s1", "action": "remove", "breakpoint_id": 2})
	call("debug_step", map[string]any{"session": "s1", "action": "over"})
	call("debug_eval", map[string]any{"session": "s1", "expression": "x", "frame": 1})
	call("debug_state", map[string]any{"session": "s1", "backtrace": true})
	call("debug_detach", map[string]any{"session": "s1"})

	want := []struct{ m, p, q string }{
		{"GET", "/api/v1/devices/d1/crashes", "app=com.x&limit=3"},
		{"GET", "/api/v1/devices/d1/crashes/A-1.ips", ""},
		{"GET", "/api/v1/devices/d1/crashes", "limit=10"},
		{"GET", "/api/v1/devices/d1/crashes/A-1.ips", ""},
		{"POST", "/api/v1/debug/sessions", ""},
		{"POST", "/api/v1/debug/sessions/s1/breakpoints", ""},
		{"DELETE", "/api/v1/debug/sessions/s1/breakpoints/2", ""},
		{"POST", "/api/v1/debug/sessions/s1/step-over", ""},
		{"POST", "/api/v1/debug/sessions/s1/eval", ""},
		{"GET", "/api/v1/debug/sessions/s1/state", ""},
		{"GET", "/api/v1/debug/sessions/s1/backtrace", ""},
		{"DELETE", "/api/v1/debug/sessions/s1", ""},
	}
	if len(hits) != len(want) {
		t.Fatalf("hits = %+v", hits)
	}
	for i, w := range want {
		if hits[i].method != w.m || hits[i].path != w.p || hits[i].query != w.q {
			t.Errorf("hit %d = %+v want %+v", i, hits[i], w)
		}
	}
	if !strings.Contains(hits[4].body, `"launch":true`) || !strings.Contains(hits[4].body, `"deviceId":"sim-1"`) {
		t.Errorf("attach body %q", hits[4].body)
	}
	if !strings.Contains(hits[8].body, `"frame":1`) {
		t.Errorf("eval body %q", hits[8].body)
	}
}
