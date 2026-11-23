package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpdateAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := updateAuth("secret", ok)
	do := func(remote, key string) int {
		r := httptest.NewRequest("GET", "/api/v1/update", nil)
		r.RemoteAddr = remote
		if key != "" {
			r.Header.Set("X-API-Key", key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if c := do("127.0.0.1:5555", ""); c != 200 {
		t.Fatalf("loopback = %d", c)
	}
	if c := do("10.0.0.2:5555", ""); c != 401 {
		t.Fatalf("remote no token = %d", c)
	}
	if c := do("10.0.0.2:5555", "secret"); c != 200 {
		t.Fatalf("remote token = %d", c)
	}
}

func TestUpdateHandlerMountedOnlyWhenSet(t *testing.T) {
	for _, set := range []bool{true, false} {
		srv, err := Start(t.Context(), Options{Addr: "127.0.0.1:0", StateDir: t.TempDir(), ADBPath: "/nonexistent-adb",
			UpdateHandler: map[bool]http.Handler{true: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }), false: nil}[set]})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.Get("http://" + srv.Addr() + "/api/v1/update")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := 404
		if set {
			want = 200
		}
		if resp.StatusCode != want {
			t.Errorf("set=%v: status %d, want %d", set, resp.StatusCode, want)
		}
		_ = srv.Shutdown(t.Context())
	}
}
