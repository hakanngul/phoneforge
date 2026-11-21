package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListOllamaModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"models":[{"name":"a:1"},{"name":""},{"name":"b:2"}]}`))
	}))
	defer srv.Close()
	got, err := ListOllamaModels(context.Background(), srv.URL)
	if err != nil || len(got) != 2 || got[0] != "a:1" || got[1] != "b:2" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := ListOllamaModels(context.Background(), "127.0.0.1:1"); err == nil {
		t.Fatal("expected error for unreachable host")
	}
}

func TestOllamaHost(t *testing.T) {
	if h := OllamaHost(func(string) string { return "" }); h != DefaultOllamaHost {
		t.Fatal(h)
	}
	if h := OllamaHost(func(string) string { return "box:11434/" }); h != "http://box:11434" {
		t.Fatal(h)
	}
}
