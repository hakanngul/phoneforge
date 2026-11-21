package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"phoneforge/internal/events"
)

type capture struct {
	body []byte
	sig  string
	ct   string
}

func receiver(t *testing.T) (*httptest.Server, chan capture) {
	t.Helper()
	ch := make(chan capture, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		ch <- capture{buf.Bytes(), r.Header.Get("X-PhoneForge-Signature"), r.Header.Get("Content-Type")}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "wh.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDeliverSignedAndUnsigned(t *testing.T) {
	srv, ch := receiver(t)
	s := newStore(t)
	m := NewManager(s)
	signed, err := s.Add("a", srv.URL, nil, "topsecret")
	if err != nil {
		t.Fatal(err)
	}
	st, err := m.Test(context.Background(), signed.ID)
	if err != nil || !st.OK || st.Code != 204 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	c := <-ch
	if c.ct != "application/json" {
		t.Fatalf("content-type %q", c.ct)
	}
	var p Payload
	if err := json.Unmarshal(c.body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Source != "phoneforge" || p.ID == "" || p.Message == "" || p.Level != "info" {
		t.Fatalf("payload %+v", p)
	}
	if c.sig != Sign(c.body, "topsecret") || len(c.sig) != 64 {
		t.Fatalf("bad signature %q", c.sig)
	}
	if got, ok := m.Status(signed.ID); !ok || !got.OK {
		t.Fatalf("status not recorded: %+v", got)
	}

	plain, _ := s.Add("b", srv.URL, nil, "")
	if _, err := m.Test(context.Background(), plain.ID); err != nil {
		t.Fatal(err)
	}
	if c := <-ch; c.sig != "" {
		t.Fatalf("unexpected signature %q", c.sig)
	}
}

func TestStartDeliversBusEvents(t *testing.T) {
	srv, ch := receiver(t)
	s := newStore(t)
	if _, err := s.Add("e", srv.URL, []string{"error"}, ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	NewManager(s).Start(ctx)
	events.Info("ignored info")
	events.Errorf("kaboom-%d", 42)
	select {
	case c := <-ch:
		if !strings.Contains(string(c.body), "kaboom-42") {
			t.Fatalf("body %s", c.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no delivery")
	}
	select {
	case c := <-ch:
		t.Fatalf("unexpected extra delivery %s", c.body)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestStorePersistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "wh.json")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.Add("n", "https://example.com/h", []string{"error", "device"}, "s")
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := s.Update(w.ID, Patch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	s2, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Get(w.ID)
	if !ok || got.Enabled || got.Secret != "s" || len(got.Events) != 2 || got.URL != "https://example.com/h" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	if err := s2.Remove(w.ID); err != nil {
		t.Fatal(err)
	}
	if s3, _ := New(path); len(s3.List()) != 0 {
		t.Fatal("remove not persisted")
	}
	if err := s2.Remove("nope"); err != ErrNotFound {
		t.Fatalf("err %v", err)
	}
}

func TestAddValidatesURL(t *testing.T) {
	s := newStore(t)
	for _, u := range []string{"", "ftp://x.com", "not a url", "http://"} {
		if _, err := s.Add("x", u, nil, ""); err == nil {
			t.Errorf("expected error for %q", u)
		}
	}
}

func TestMatch(t *testing.T) {
	ev := events.Event{Level: "success", Message: "Device Pixel-7 connected"}
	cases := []struct {
		filter []string
		want   bool
	}{
		{nil, true},
		{[]string{"success"}, true},
		{[]string{"error"}, false},
		{[]string{"error", "pixel-7"}, true},
		{[]string{"agent done"}, false},
		{[]string{"device *connected"}, true},
	}
	for _, c := range cases {
		if got := Match(Webhook{Events: c.filter}, ev); got != c.want {
			t.Errorf("filter %v: got %v want %v", c.filter, got, c.want)
		}
	}
}

func TestRoutesMaskSecretAndTest(t *testing.T) {
	srv, ch := receiver(t)
	m := NewManager(newStore(t))
	h := m.Routes()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rr
	}
	rr := do("POST", "/", `{"name":"x","url":"`+srv.URL+`","secret":"hunter2"}`)
	if rr.Code != 201 || strings.Contains(rr.Body.String(), "hunter2") {
		t.Fatalf("create %d %s", rr.Code, rr.Body)
	}
	var v webhookView
	_ = json.Unmarshal(rr.Body.Bytes(), &v)
	if !v.HasSecret || v.Secret != "********" {
		t.Fatalf("view %+v", v)
	}
	if rr := do("GET", "/", ""); strings.Contains(rr.Body.String(), "hunter2") {
		t.Fatal("secret leaked in list")
	}
	if rr := do("POST", "/"+v.ID+"/test", ""); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"ok":true`) {
		t.Fatalf("test %d %s", rr.Code, rr.Body)
	}
	<-ch
	if rr := do("PUT", "/"+v.ID, `{"enabled":false}`); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"enabled":false`) {
		t.Fatalf("put %d %s", rr.Code, rr.Body)
	}
	if rr := do("POST", "/", `{"url":"bad"}`); rr.Code != 400 {
		t.Fatalf("bad url %d", rr.Code)
	}
	if rr := do("DELETE", "/"+v.ID, ""); rr.Code != 200 {
		t.Fatalf("delete %d", rr.Code)
	}
	if rr := do("DELETE", "/"+v.ID, ""); rr.Code != 404 {
		t.Fatalf("delete again %d", rr.Code)
	}
}
