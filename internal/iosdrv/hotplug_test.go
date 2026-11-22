package iosdrv

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeSession is one scripted usbmuxd listen session.
type fakeSession struct {
	ch     chan HotplugEvent // closed => next returns error
	closed chan struct{}
	once   sync.Once
}

func newSession() *fakeSession {
	return &fakeSession{ch: make(chan HotplugEvent, 16), closed: make(chan struct{})}
}

func (s *fakeSession) next() (HotplugEvent, error) {
	select {
	case ev, ok := <-s.ch:
		if !ok {
			return HotplugEvent{}, errors.New("conn dropped")
		}
		return ev, nil
	case <-s.closed:
		return HotplugEvent{}, errors.New("closed")
	}
}
func (s *fakeSession) close() error { s.once.Do(func() { close(s.closed) }); return nil }

func collect(t *testing.T, ch <-chan HotplugEvent, n int) []HotplugEvent {
	t.Helper()
	var out []HotplugEvent
	for len(out) < n {
		select {
		case e := <-ch:
			out = append(out, e)
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout; got %v", out)
		}
	}
	return out
}

func TestWatchAttachDetachAndReconnect(t *testing.T) {
	s1, s2 := newSession(), newSession()
	var mu sync.Mutex
	calls := 0
	listen := func() (func() (HotplugEvent, error), func() error, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		switch calls {
		case 1:
			return nil, nil, errors.New("no usbmuxd yet") // listener failure
		case 2:
			return s1.next, s1.close, nil
		default:
			return s2.next, s2.close, nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan HotplugEvent, 32)
	done := make(chan struct{})
	go func() {
		Watch(ctx, listen, func(e HotplugEvent) { out <- e }, WatchOptions{MinBackoff: 5 * time.Millisecond, MaxBackoff: 10 * time.Millisecond})
		close(done)
	}()
	if ev := collect(t, out, 1); !ev[0].Resync {
		t.Fatalf("want resync after connect, got %v", ev)
	}
	s1.ch <- HotplugEvent{Attached: true, UDID: "U1"}
	s1.ch <- HotplugEvent{Detached: true, UDID: "U1"}
	ev := collect(t, out, 2)
	if !ev[0].Attached || ev[0].UDID != "U1" || !ev[1].Detached {
		t.Fatalf("got %v", ev)
	}
	close(s1.ch) // connection drops -> reconnect + resync
	if ev := collect(t, out, 1); !ev[0].Resync {
		t.Fatalf("want resync after reconnect, got %v", ev)
	}
	s2.ch <- HotplugEvent{Attached: true, UDID: "U1"} // re-attach works
	if ev := collect(t, out, 1); !ev[0].Attached {
		t.Fatalf("got %v", ev)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not stop on ctx cancel")
	}
}
