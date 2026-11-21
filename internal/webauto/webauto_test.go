package webauto

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

const unixSample = `Num       RefCount Protocol Flags    Type St Inode Path
0000000000000000: 00000002 00000000 00010000 0001 01 12345 /dev/socket/logd
0000000000000000: 00000002 00000000 00010000 0001 01 22222 @chrome_devtools_remote
0000000000000000: 00000002 00000000 00010000 0001 01 22223 @webview_devtools_remote_4242
0000000000000000: 00000002 00000000 00010000 0001 01 22224 @chrome_devtools_remote
0000000000000000: 00000002 00000000 00010000 0001 01 22225 @other_socket
0000000000000000: 00000002 00000000 00010000 0001 01 22226
`

func TestParseUnixSockets(t *testing.T) {
	got := ParseUnixSockets([]byte(unixSample))
	if len(got) != 2 || got[0] != "chrome_devtools_remote" || got[1] != "webview_devtools_remote_4242" {
		t.Fatalf("got %v", got)
	}
}

func TestParseTargets(t *testing.T) {
	ts, err := ParseTargets([]byte(`[{"id":"A","type":"page","title":"T","url":"https://x","webSocketDebuggerUrl":"ws://localhost:9222/devtools/page/A"},{"id":"B","type":"service_worker","title":"","url":"","webSocketDebuggerUrl":""}]`))
	if err != nil || len(ts) != 2 || ts[0].ID != "A" || ts[0].Type != "page" {
		t.Fatalf("%v %v", ts, err)
	}
	d, ok := DefaultTarget(ts)
	if !ok || d.ID != "A" {
		t.Fatal("default target")
	}
	if _, err := ParseTargets([]byte("nope")); err == nil {
		t.Fatal("want error")
	}
	u, _ := rewriteWS("ws://localhost:9222/devtools/page/A", 5555)
	if u != "ws://127.0.0.1:5555/devtools/page/A" {
		t.Fatal(u)
	}
}

func TestCDPCodec(t *testing.T) {
	b, err := EncodeRequest(7, "Page.navigate", map[string]string{"url": "x"})
	if err != nil || string(b) != `{"id":7,"method":"Page.navigate","params":{"url":"x"}}` {
		t.Fatalf("%s %v", b, err)
	}
	r, _ := DecodeResponse([]byte(`{"id":7,"result":{"a":1}}`))
	if r.ID != 7 || string(r.Result) != `{"a":1}` {
		t.Fatal(r)
	}
	r, _ = DecodeResponse([]byte(`{"id":8,"error":{"code":-32000,"message":"bad"}}`))
	if r.Error == nil || r.Error.Error() == "" {
		t.Fatal("error not decoded")
	}
	r, _ = DecodeResponse([]byte(`{"method":"Page.loadEventFired","params":{}}`))
	if r.ID != 0 || r.Method != "Page.loadEventFired" {
		t.Fatal(r)
	}
}

// fakeCDP is an httptest server serving /json/list and a CDP websocket.
type fakeCDP struct {
	mu      sync.Mutex
	methods []string
	srv     *httptest.Server
}

func newFakeCDP(t *testing.T) *fakeCDP {
	f := &fakeCDP{}
	up := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"P1","type":"page","title":"t","url":"u","webSocketDebuggerUrl":"ws://localhost:1/devtools/page/P1"}]`))
	})
	mux.HandleFunc("/devtools/page/P1", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"method":"Page.frameNavigated","params":{}}`))
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			var req struct {
				ID     int64
				Method string
				Params map[string]any
			}
			_ = json.Unmarshal(data, &req)
			f.mu.Lock()
			f.methods = append(f.methods, req.Method)
			f.mu.Unlock()
			var res string
			switch req.Method {
			case "Runtime.evaluate":
				expr, _ := req.Params["expression"].(string)
				switch {
				case strings.Contains(expr, "getBoundingClientRect"):
					res = `{"result":{"type":"object","value":{"x":10,"y":20,"w":5,"h":5}}}`
				case strings.Contains(expr, "outerHTML"):
					res = `{"result":{"type":"string","value":"<html></html>"}}`
				case strings.Contains(expr, "throw"):
					res = `{"result":{"type":"object"},"exceptionDetails":{"text":"Uncaught","exception":{"description":"Error: boom"}}}`
				case strings.HasPrefix(expr, "!!document"):
					res = `{"result":{"type":"boolean","value":true}}`
				case strings.Contains(expr, "e.focus()"):
					res = `{"result":{"type":"boolean","value":true}}`
				default:
					res = `{"result":{"type":"number","value":42}}`
				}
			case "Page.captureScreenshot":
				res = `{"data":"` + base64.StdEncoding.EncodeToString([]byte("PNGDATA")) + `"}`
			case "Page.navigate":
				res = `{"frameId":"F"}`
			default:
				res = `{}`
			}
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"id":`+itoa(int(req.ID))+`,"result":`+res+`}`))
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCDP) port() int {
	n := 0
	for _, ch := range f.srv.URL[strings.LastIndex(f.srv.URL, ":")+1:] {
		n = n*10 + int(ch-'0')
	}
	return n
}

func TestPageAgainstFakeServer(t *testing.T) {
	f := newFakeCDP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+strings.TrimPrefix(f.srv.URL, "http")+"/devtools/page/P1")
	if err != nil {
		t.Fatal(err)
	}
	p := NewPage(Target{ID: "P1"}, c)
	defer p.Close()

	if err := p.Navigate(ctx, "https://example.com"); err != nil {
		t.Fatal(err)
	}
	v, err := p.Evaluate(ctx, "1+41")
	if err != nil || string(v) != "42" {
		t.Fatalf("%s %v", v, err)
	}
	if _, err := p.Evaluate(ctx, "throw new Error('boom')"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want exception, got %v", err)
	}
	if err := p.Click(ctx, "#go"); err != nil {
		t.Fatal(err)
	}
	if err := p.Type(ctx, "#in", "hi"); err != nil {
		t.Fatal(err)
	}
	if h, err := p.HTML(ctx); err != nil || h != "<html></html>" {
		t.Fatalf("%q %v", h, err)
	}
	if png, err := p.Screenshot(ctx); err != nil || string(png) != "PNGDATA" {
		t.Fatalf("%q %v", png, err)
	}
	f.mu.Lock()
	joined := strings.Join(f.methods, ",")
	f.mu.Unlock()
	for _, m := range []string{"Page.navigate", "Input.dispatchMouseEvent", "Input.insertText", "Page.captureScreenshot"} {
		if !strings.Contains(joined, m) {
			t.Errorf("missing %s in %s", m, joined)
		}
	}
	_ = p.Close()
	if _, err := p.Evaluate(ctx, "1"); err == nil {
		t.Fatal("want error after close")
	}
}

func TestSessionAndRoutes(t *testing.T) {
	f := newFakeCDP(t)
	var calls []string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) >= 3 && args[2] == "shell" {
			return []byte(unixSample), nil
		}
		return nil, nil
	}
	s := NewSession("SER", run)
	// Pre-seed forwards to the fake server so no real adb forward is needed.
	s.forwards["chrome_devtools_remote"] = f.port()
	s.forwards["webview_devtools_remote_4242"] = f.port()

	ts, err := s.Targets(context.Background())
	if err != nil || len(ts) != 2 || ts[0].Socket != "chrome_devtools_remote" {
		t.Fatalf("%v %v", ts, err)
	}

	m := NewManagerWithRunner(run)
	m.ss["SER"] = s
	root := chi.NewRouter()
	root.Mount("/api/v1/web", NewRoutes(m, func(id string) (string, error) { return "SER", nil }))

	do := func(method, path, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		root.ServeHTTP(rr, httptest.NewRequest(method, "/api/v1/web"+path, strings.NewReader(body)))
		return rr
	}
	if rr := do("GET", "/devices/d1/targets", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"id":"P1"`) {
		t.Fatal(rr.Code, rr.Body.String())
	}
	rr := do("POST", "/devices/d1/eval", `{"script":"1+41"}`)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"result":42`) {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if rr := do("POST", "/devices/d1/click", `{"selector":"#go"}`); rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if rr := do("POST", "/devices/d1/wait", `{"selector":"#go","timeoutMs":1000}`); rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if rr := do("GET", "/devices/d1/screenshot", ""); rr.Code != 200 || rr.Body.String() != "PNGDATA" {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if rr := do("POST", "/devices/d1/navigate", `{}`); rr.Code == 200 {
		t.Fatal("missing url should fail")
	}
	m.CloseAll(context.Background())
	found := false
	for _, c := range calls {
		if strings.Contains(c, "forward --remove") {
			found = true
		}
	}
	if !found {
		t.Fatalf("forwards not removed: %v", calls)
	}
}
