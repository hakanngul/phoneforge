package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"phoneforge/internal/registry"

	"phoneforge/internal/config"
)

// syncRec is a ResponseRecorder safe to read while the handler writes.
type syncRec struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func newSyncRec() *syncRec { return &syncRec{rec: httptest.NewRecorder()} }

func (s *syncRec) Header() http.Header { return s.rec.Header() }
func (s *syncRec) WriteHeader(c int)   { s.mu.Lock(); s.rec.WriteHeader(c); s.mu.Unlock() }
func (s *syncRec) Flush()              {}
func (s *syncRec) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.Write(b)
}
func (s *syncRec) body() string { s.mu.Lock(); defer s.mu.Unlock(); return s.rec.Body.String() }

func TestCORSAllowsPutPatchDelete(t *testing.T) {
	h := NewRouter(registry.New(), config.Default())
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/devices", nil)
	req.Header.Set("Origin", "http://127.0.0.1:8700")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status %d", rec.Code)
	}
	allow := rec.Header().Get("Access-Control-Allow-Methods")
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if !strings.Contains(allow, m) {
			t.Errorf("Allow-Methods %q lacks %s", allow, m)
		}
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://127.0.0.1:8700" {
		t.Error("allowed origin not echoed")
	}
	// Unknown origins get no CORS headers.
	req2 := httptest.NewRequest(http.MethodOptions, "/api/v1/devices", nil)
	req2.Header.Set("Origin", "http://evil.example")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("foreign origin must not be allowed")
	}
}

func TestServeMJPEGFirstFrameBeforeStream(t *testing.T) {
	rec := newSyncRec()
	req := httptest.NewRequest(http.MethodGet, "/mjpeg", nil)
	gate := make(chan struct{})
	first := func(ctx context.Context) ([]byte, error) { return []byte("FIRSTFRAME"), nil }
	stream := func(ctx context.Context, fn func([]byte) error) error {
		// The stream only produces after the screenshot frame went out.
		deadline := time.After(2 * time.Second)
		for !strings.Contains(rec.body(), "FIRSTFRAME") {
			select {
			case <-deadline:
				t.Error("first frame was not emitted before the stream produced one")
				return nil
			case <-time.After(5 * time.Millisecond):
			}
		}
		close(gate)
		return fn([]byte("STREAMFRAME"))
	}
	serveMJPEG(rec, req, rec, first, stream)
	<-gate
	body := rec.body()
	if !strings.HasPrefix(body, "--pfframe\r\n") {
		t.Fatalf("missing leading boundary: %q", body)
	}
	i, j := strings.Index(body, "FIRSTFRAME"), strings.Index(body, "STREAMFRAME")
	if i < 0 || j < i {
		t.Fatalf("frame order wrong: %q", body)
	}
	// Each part is terminated by the next boundary immediately.
	if !strings.Contains(body, "FIRSTFRAME\r\n--pfframe\r\n") || !strings.HasSuffix(body, "STREAMFRAME\r\n--pfframe\r\n") {
		t.Fatalf("parts not closed with boundary: %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "multipart/x-mixed-replace; boundary=pfframe") {
		t.Fatalf("content type %q", ct)
	}
}

func TestServeMJPEGStreamWinsDropsLateScreenshot(t *testing.T) {
	rec := newSyncRec()
	req := httptest.NewRequest(http.MethodGet, "/mjpeg", nil)
	release := make(chan struct{})
	first := func(ctx context.Context) ([]byte, error) {
		<-release
		return []byte("LATESHOT"), nil
	}
	stream := func(ctx context.Context, fn func([]byte) error) error {
		err := fn([]byte("S1"))
		close(release)
		time.Sleep(30 * time.Millisecond)
		return err
	}
	serveMJPEG(rec, req, rec, first, stream)
	if bytes.Contains([]byte(rec.body()), []byte("LATESHOT")) {
		t.Fatal("a screenshot arriving after the stream started must be discarded")
	}
}

func TestServeMJPEGStreamErrorBeforeAnyFrame(t *testing.T) {
	rec := newSyncRec()
	req := httptest.NewRequest(http.MethodGet, "/mjpeg", nil)
	first := func(ctx context.Context) ([]byte, error) { return nil, context.DeadlineExceeded }
	stream := func(ctx context.Context, fn func([]byte) error) error { return context.Canceled }
	serveMJPEG(rec, req, rec, first, stream)
	if rec.rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.rec.Code)
	}
}

func TestServeMJPEGRawFormatHeaders(t *testing.T) {
	for _, tc := range []struct{ name, url, accept string }{
		{"query", "/mjpeg?format=raw", ""},
		{"accept", "/mjpeg", "application/x-pf-mjpeg"},
	} {
		rec := newSyncRec()
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		if tc.accept != "" {
			req.Header.Set("Accept", tc.accept)
		}
		serveMJPEG(rec, req, rec, nil, func(ctx context.Context, fn func([]byte) error) error { return fn([]byte("FRAME")) })
		if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
			t.Fatalf("%s: content type %q", tc.name, ct)
		}
		if b := rec.Header().Get("X-Boundary"); b != "pfframe" {
			t.Fatalf("%s: X-Boundary %q", tc.name, b)
		}
		body := rec.body()
		if !strings.HasPrefix(body, "--pfframe\r\n") || !strings.Contains(body, "Content-Length: 5\r\n\r\nFRAME\r\n--pfframe\r\n") {
			t.Fatalf("%s: framing changed: %q", tc.name, body)
		}
	}
}
