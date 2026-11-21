package farms

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type memSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func newMem() *memSecrets { return &memSecrets{m: map[string]string{}} }
func (s *memSecrets) Set(n, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[n] = v
	return nil
}
func (s *memSecrets) Reveal(n string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[n]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}
func (s *memSecrets) Has(n string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[n]
	return ok
}
func (s *memSecrets) Delete(n string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, n)
	return nil
}

func TestConfigureAndStatusHidesSecrets(t *testing.T) {
	sec := newMem()
	m := NewManager(sec, filepath.Join(t.TempDir(), "farms.json"))
	if err := m.Configure(BrowserStack, map[string]string{"username": "bob", "access_key": "SUPERSECRET"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := sec.Reveal("farm:browserstack:user"); v != "bob" {
		t.Fatalf("user not stored: %q", v)
	}
	var bs Config
	for _, c := range m.Status() {
		if c.Provider == BrowserStack {
			bs = c
		}
	}
	if !bs.Enabled || !bs.HasCredentials {
		t.Fatalf("bad status: %+v", bs)
	}
	rr := httptest.NewRecorder()
	m.Routes().ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if strings.Contains(rr.Body.String(), "SUPERSECRET") || strings.Contains(rr.Body.String(), "bob") {
		t.Fatalf("secret leaked: %s", rr.Body)
	}
	// Persistence across managers.
	if err := m.Disable(BrowserStack); err != nil {
		t.Fatal(err)
	}
	if sec.Has("farm:browserstack:key") {
		t.Fatal("credentials not deleted")
	}
}

func TestBrowserStackListDevices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if r.URL.Path != "/app-automate/devices.json" || !ok || u != "bob" || p != "k3y" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `[{"os":"android","os_version":"13.0","device":"Samsung Galaxy S23","realMobile":true},
		{"os":"ios","os_version":"17","device":"iPhone 15","realMobile":true}]`)
	}))
	defer srv.Close()
	old := BrowserStackBaseURL
	BrowserStackBaseURL = srv.URL
	defer func() { BrowserStackBaseURL = old }()

	m := NewManager(newMem(), "")
	if _, err := m.ListDevices(context.Background(), BrowserStack); !errors.Is(err, ErrMissingCredentials) {
		t.Fatalf("want missing creds, got %v", err)
	}
	if err := m.Configure(BrowserStack, map[string]string{"user": "bob", "key": "k3y"}); err != nil {
		t.Fatal(err)
	}
	devs, err := m.ListDevices(context.Background(), BrowserStack)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 || devs[0].Name != "Samsung Galaxy S23" || devs[0].OS != "android" ||
		devs[0].OSVersion != "13.0" || !devs[0].Real || devs[1].Provider != BrowserStack {
		t.Fatalf("bad devices: %+v", devs)
	}

	// Via HTTP route.
	rr := httptest.NewRecorder()
	m.Routes().ServeHTTP(rr, httptest.NewRequest("GET", "/browserstack/devices", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "iPhone 15") {
		t.Fatalf("route: %d %s", rr.Code, rr.Body)
	}
}

func TestSauceLabsListDevices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"descriptor":"Pixel_7_real","name":"Google Pixel 7","os":"ANDROID","osVersion":"13"}]`)
	}))
	defer srv.Close()
	old := SauceLabsBaseURL
	SauceLabsBaseURL = srv.URL
	defer func() { SauceLabsBaseURL = old }()
	m := NewManager(newMem(), "")
	_ = m.Configure(SauceLabs, map[string]string{"user": "u", "key": "k"})
	devs, err := m.ListDevices(context.Background(), SauceLabs)
	if err != nil || len(devs) != 1 || devs[0].DeviceID != "Pixel_7_real" {
		t.Fatalf("%v %+v", err, devs)
	}
}

func TestAWSStub(t *testing.T) {
	m := NewManager(newMem(), "")
	if err := m.Configure(AWSDeviceFarm, map[string]string{"access_key_id": "a", "secret_access_key": "b"}); err != nil {
		t.Fatal(err)
	}
	_, err := m.ListDevices(context.Background(), AWSDeviceFarm)
	if !errors.Is(err, ErrAWSNotImplemented) || !strings.Contains(err.Error(), "requires AWS SDK") {
		t.Fatalf("got %v", err)
	}
	rr := httptest.NewRecorder()
	m.Routes().ServeHTTP(rr, httptest.NewRequest("GET", "/awsdevicefarm/devices", nil))
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("code %d", rr.Code)
	}
}

func TestUnknownProvider(t *testing.T) {
	m := NewManager(newMem(), "")
	if err := m.Configure("nope", nil); !errors.Is(err, ErrUnknownProvider) {
		t.Fatal(err)
	}
}
