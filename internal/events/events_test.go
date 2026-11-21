package events

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func reset() {
	defaultBus.mu.Lock()
	defaultBus.ring = nil
	defaultBus.mu.Unlock()
}

func TestPublishRecentOrderAndCap(t *testing.T) {
	reset()
	for i := 0; i < 250; i++ {
		Info(string(rune('a' + i%26)))
	}
	Errorf("boom %d", 7)
	got := Recent()
	if len(got) != 200 {
		t.Fatalf("len = %d, want 200", len(got))
	}
	last := got[len(got)-1]
	if last.Level != "error" || last.Message != "boom 7" {
		t.Fatalf("last = %+v", last)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Time.Before(got[i-1].Time) {
			t.Fatal("events not oldest-first")
		}
	}
	if got[0].Time.Location() != time.UTC {
		t.Fatal("time not UTC")
	}
}

func TestSubscribe(t *testing.T) {
	reset()
	ch, unsub := Subscribe()
	defer unsub()
	Success("ok")
	select {
	case e := <-ch:
		if e.Level != "success" || e.Message != "ok" {
			t.Fatalf("got %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
	unsub()
	unsub() // idempotent
}

func TestPublishNeverBlocks(t *testing.T) {
	reset()
	_, unsub := Subscribe() // never drained
	defer unsub()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			Info("x")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber")
	}
}

func TestHandlerReplay(t *testing.T) {
	reset()
	Info("replayed")
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	read := func() Event {
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data: ") {
				var e Event
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
					t.Fatal(err)
				}
				return e
			}
		}
		t.Fatal("stream ended")
		return Event{}
	}
	if e := read(); e.Message != "replayed" {
		t.Fatalf("replay = %+v", e)
	}
	Success("live")
	if e := read(); e.Message != "live" {
		t.Fatalf("live = %+v", e)
	}
}
