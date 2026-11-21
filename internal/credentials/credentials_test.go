package credentials

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStores(t *testing.T) (*SecretStore, *CardStore, string) {
	t.Helper()
	dir := t.TempDir()
	sec, err := NewWithBackend(filepath.Join(dir, "secrets.json"), NewMemoryBackend())
	if err != nil {
		t.Fatal(err)
	}
	cards, err := NewCardStore(filepath.Join(dir, "cards.json"), sec)
	if err != nil {
		t.Fatal(err)
	}
	return sec, cards, dir
}

func TestSecretStore(t *testing.T) {
	sec, _, dir := newStores(t)
	if err := sec.Set("b", "v1"); err != nil {
		t.Fatal(err)
	}
	_ = sec.Set("a", "v2")
	if !sec.Has("a") || sec.Has("zzz") {
		t.Fatal("Has wrong")
	}
	if n := sec.Names(); len(n) != 2 || n[0] != "a" || n[1] != "b" {
		t.Fatalf("names %v", n)
	}
	if v, _ := sec.Reveal("b"); v != "v1" {
		t.Fatal("reveal")
	}
	idx, _ := os.ReadFile(filepath.Join(dir, "secrets.json"))
	if strings.Contains(string(idx), "v1") {
		t.Fatal("index leaks value")
	}
	if err := sec.Delete("a"); err != nil || sec.Has("a") {
		t.Fatal("delete")
	}
	if err := sec.Delete("a"); err != nil {
		t.Fatal("double delete should be ok")
	}
}

func TestAppCards(t *testing.T) {
	_, cards, dir := newStores(t)
	c, err := cards.Add(AppCard{
		BundleID: "com.example.app", Label: "Example",
		Fields: []Field{{Name: "user"}, {Name: "password", Secret: true}},
		Values: map[string]string{"user": "bob", "password": "LEAK"},
	}, map[string]string{"password": "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Values["password"] != MaskedValue || c.Values["user"] != "bob" {
		t.Fatalf("add result %+v", c)
	}
	for _, l := range cards.List() {
		if l.Values["password"] != MaskedValue {
			t.Fatal("list not masked")
		}
	}
	if v, err := cards.Reveal(c.ID, "password"); err != nil || v != "s3cret" {
		t.Fatalf("reveal %q %v", v, err)
	}
	if v, _ := cards.Reveal(c.ID, "user"); v != "bob" {
		t.Fatal("reveal plain")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "cards.json"))
	if strings.Contains(string(data), "s3cret") || strings.Contains(string(data), "LEAK") {
		t.Fatal("metadata file leaks secret")
	}
	if err := cards.Delete(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := cards.Reveal(c.ID, "password"); err != ErrNotFound {
		t.Fatal("should be gone")
	}
	if _, err := cards.Add(AppCard{BundleID: "x", Fields: []Field{{Name: "u"}}}, map[string]string{"u": "v"}); err == nil {
		t.Fatal("secret for non-secret field must fail")
	}
}

func TestRoutesNeverLeak(t *testing.T) {
	sec, cards, _ := newStores(t)
	h := Routes(sec, cards)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rr
	}
	if rr := do("PUT", "/secrets/api", `{"value":"TOPSECRET"}`); rr.Code != 200 || strings.Contains(rr.Body.String(), "TOPSECRET") {
		t.Fatalf("put: %d %s", rr.Code, rr.Body)
	}
	if rr := do("GET", "/secrets", ""); !strings.Contains(rr.Body.String(), `"api"`) || strings.Contains(rr.Body.String(), "TOPSECRET") {
		t.Fatalf("get secrets: %s", rr.Body)
	}
	rr := do("POST", "/app-cards", `{"id":"c1","bundleId":"com.x","label":"X","fields":[{"name":"pw","secret":true}],"secrets":{"pw":"PWVALUE"}}`)
	if rr.Code != http.StatusCreated || strings.Contains(rr.Body.String(), "PWVALUE") {
		t.Fatalf("post: %d %s", rr.Code, rr.Body)
	}
	if rr := do("GET", "/app-cards", ""); strings.Contains(rr.Body.String(), "PWVALUE") || !strings.Contains(rr.Body.String(), "***") {
		t.Fatalf("list: %s", rr.Body)
	}
	if rr := do("DELETE", "/app-cards/c1", ""); rr.Code != 204 {
		t.Fatal(rr.Code)
	}
	if rr := do("DELETE", "/app-cards/c1", ""); rr.Code != 404 {
		t.Fatal(rr.Code)
	}
	if rr := do("DELETE", "/secrets/api", ""); rr.Code != 204 || sec.Has("api") {
		t.Fatal("delete secret")
	}
}
