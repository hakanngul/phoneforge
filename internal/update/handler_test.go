package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func do(h http.Handler, method, path, body, remote string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = remote
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const lo = "127.0.0.1:50000"

func TestHandlerStatusShape(t *testing.T) {
	e := newEnv(t, "0.4.2")
	h := e.m.Handler()
	w := do(h, "GET", "/api/v1/update", "", "203.0.113.9:1234") // reads are open
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"current", "disabled", "supported", "state", "available", "skipped", "remindUntil",
		"progress", "error", "lastCheck", "manualUrl", "settings", "rollback"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %s", k, w.Body.String())
		}
	}
	if m["state"] != "idle" || m["available"] != nil || m["current"] != "0.4.0" {
		t.Fatalf("%v", m)
	}
	set := m["settings"].(map[string]any)
	if set["autoCheck"] != true || set["channel"] != "stable" || set["autoDownload"] != false {
		t.Fatalf("settings %v", set)
	}

	// after a check the available object has the contract's fields
	w = do(h, "POST", "/api/v1/update/check", "", lo)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	av := m["available"].(map[string]any)
	for _, k := range []string{"version", "notesMd", "notesUrl", "published", "size"} {
		if _, ok := av[k]; !ok {
			t.Errorf("available missing %q", k)
		}
	}
	if m["state"] != "available" || m["lastCheck"] == nil {
		t.Fatalf("%v", m)
	}
}

func TestHandlerLoopbackOnlyMutations(t *testing.T) {
	e := newEnv(t, "0.4.2")
	h := e.m.Handler()
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/update/check", ""},
		{"POST", "/api/v1/update/download", ""},
		{"POST", "/api/v1/update/install", ""},
		{"POST", "/api/v1/update/skip", `{"version":"0.4.2"}`},
		{"POST", "/api/v1/update/remind", `{"hours":1}`},
		{"POST", "/api/v1/update/rollback", ""},
		{"PUT", "/api/v1/update/settings", `{"autoCheck":false,"channel":"stable","autoDownload":false}`},
	} {
		for _, remote := range []string{"203.0.113.9:1234", "", "[2001:db8::1]:80"} {
			if w := do(h, c.method, c.path, c.body, remote); w.Code != 403 {
				t.Errorf("%s %s from %q: %d", c.method, c.path, remote, w.Code)
			}
		}
		// a loopback peer carrying a foreign Origin (browser CSRF) is refused too
		if w := do(h, c.method, c.path, c.body, lo, "Origin", "https://evil.example"); w.Code != 403 {
			t.Errorf("%s %s with foreign origin: %d", c.method, c.path, w.Code)
		}
	}
	if e.f.reqs.Load() != 0 {
		t.Fatal("rejected requests must not trigger work")
	}
	// loopback with a loopback origin is fine; IPv6 loopback too
	if w := do(h, "POST", "/api/v1/update/check", "", "[::1]:5000", "Origin", "http://127.0.0.1:8700"); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestHandlerRoutesAndErrors(t *testing.T) {
	e := newEnv(t, "0.4.2")
	h := e.m.Handler()
	check := func(w *httptest.ResponseRecorder, code int) {
		t.Helper()
		if w.Code != code {
			t.Fatalf("want %d got %d: %s", code, w.Code, w.Body)
		}
		if code >= 400 {
			var m map[string]string
			if json.Unmarshal(w.Body.Bytes(), &m) != nil || m["error"] == "" {
				t.Fatalf("error body: %s", w.Body)
			}
		}
	}
	check(do(h, "GET", "/api/v1/update/check", "", lo), 405)
	check(do(h, "POST", "/api/v1/update", "", lo), 405)
	check(do(h, "GET", "/api/v1/update/settings", "", lo), 405)
	check(do(h, "POST", "/api/v1/update/settings", "{}", lo), 405)
	check(do(h, "POST", "/api/v1/update/nope", "", lo), 404)
	check(do(h, "POST", "/api/v1/update/skip", "{bad", lo), 400)
	check(do(h, "POST", "/api/v1/update/skip", `{"version":"x"}`, lo), 400)
	check(do(h, "PUT", "/api/v1/update/settings", `{"autoCheck":true,"channel":"nightly"}`, lo), 400)
	check(do(h, "POST", "/api/v1/update/install", "", lo), 409) // nothing to install yet
	check(do(h, "POST", "/api/v1/update/download", "", lo), 409)
	check(do(h, "POST", "/api/v1/update/rollback", "", lo), 500) // no backup (internal error text)

	check(do(h, "POST", "/api/v1/update/skip", `{"version":"0.4.2"}`, lo), 200)
	w := do(h, "POST", "/api/v1/update/remind", "", lo) // default 24h
	check(w, 200)
	var st Status
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if st.RemindUntil == nil {
		t.Fatal("default remind should set remindUntil")
	}
	check(do(h, "POST", "/api/v1/update/remind", `{"hours":-1}`, lo), 400)
	w = do(h, "PUT", "/api/v1/update/settings", `{"autoCheck":false,"channel":"beta","autoDownload":true}`, lo)
	check(w, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if st.Settings.AutoCheck || st.Settings.Channel != "beta" || !st.Settings.AutoDownload {
		t.Fatalf("%+v", st.Settings)
	}

	// mounted behind http.StripPrefix (path "/check", or "" for the root)
	mux := http.NewServeMux()
	mux.Handle("/api/v1/update/", http.StripPrefix("/api/v1/update", h))
	mux.Handle("/api/v1/update", h)
	check(do(mux, "GET", "/api/v1/update", "", lo), 200)
	check(do(mux, "GET", "/api/v1/update/", "", lo), 200)
	check(do(mux, "POST", "/api/v1/update/check", "", lo), 200)
}

func TestHandlerInstallAsync(t *testing.T) {
	e := newEnv(t, "0.4.2")
	h := e.m.Handler()
	if w := do(h, "POST", "/api/v1/update/check", "", lo); w.Code != 200 {
		t.Fatal(w.Body)
	}
	e.busy = "recording in progress"
	w := do(h, "POST", "/api/v1/update/install", "", lo)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "busy: recording in progress") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	e.busy = ""
	w = do(h, "POST", "/api/v1/update/install", "", lo)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var st Status
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if st.State != StateInstalling {
		t.Fatalf("state %q", st.State)
	}
	e.m.Wait()
	if countOf(e.snapshot(), "quit") != 1 {
		t.Fatalf("calls %v", e.snapshot())
	}
}

func TestHandlerDownloadAsync(t *testing.T) {
	e := newEnv(t, "0.4.2")
	h := e.m.Handler()
	_, _ = e.m.CheckNow(context.Background())
	w := do(h, "POST", "/api/v1/update/download", "", lo)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	e.m.Wait()
	if st := e.m.Status(); st.State != StateReady || st.Progress != nil {
		t.Fatalf("%+v", st)
	}
}
