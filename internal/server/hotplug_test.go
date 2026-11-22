package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"phoneforge/internal/device"
	"phoneforge/internal/registry"
)

type fdev struct {
	device.Device
	info device.Info
}

func (f fdev) Info() device.Info { return f.info }

type fakeDisc struct {
	mu   sync.Mutex
	devs []device.Device
	err  error
}

func (f *fakeDisc) set(ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devs = nil
	for _, id := range ids {
		f.devs = append(f.devs, fdev{info: device.Info{ID: id, Serial: id, Name: id, Platform: device.PlatformIOS}})
	}
}
func (f *fakeDisc) Discover(context.Context) ([]device.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.devs, f.err
}

func ids(reg *registry.Registry) []string {
	var out []string
	for _, d := range reg.List() {
		out = append(out, d.ID)
	}
	return out
}

func TestDeviceLoopAttachDetachReattach(t *testing.T) {
	fd := &fakeDisc{}
	reg := registry.New()
	reg.AddDiscoverer(fd)
	l := newDeviceLoop(reg, make(chan struct{}, 1))
	var added, removed []string
	l.OnAdded = func(d device.Info) { added = append(added, d.ID) }
	l.OnRemoved = func(d device.Info) { removed = append(removed, d.ID) }
	ctx := context.Background()

	l.Baseline()
	fd.set("ios-A")
	l.Scan(ctx) // attach
	fd.set()
	l.Scan(ctx) // detach
	if len(ids(reg)) != 0 {
		t.Fatalf("still listed: %v", ids(reg))
	}
	fd.set("ios-A")
	l.Scan(ctx) // re-attach
	if len(added) != 2 || len(removed) != 1 || len(ids(reg)) != 1 {
		t.Fatalf("added=%v removed=%v list=%v", added, removed, ids(reg))
	}
}

func TestDeviceLoopKickTriggersRescanAndTickerIsFallback(t *testing.T) {
	fd := &fakeDisc{}
	reg := registry.New()
	reg.AddDiscoverer(fd)
	kick := make(chan struct{}, 1)
	l := newDeviceLoop(reg, kick)
	l.interval = 150 * time.Millisecond // listener "down": only the ticker can notice
	l.debounce = 5 * time.Millisecond
	var mu sync.Mutex
	var added []string
	l.OnAdded = func(d device.Info) { mu.Lock(); added = append(added, d.ID); mu.Unlock() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	wait := func(n int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			c := len(added)
			mu.Unlock()
			if c >= n {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %d adds, got %v", n, added)
	}
	fd.set("ios-A")
	kick <- struct{}{} // Listen path: fast
	wait(1)
	fd.set("ios-A", "ios-B") // no kick: ticker fallback
	wait(2)
}

func TestDeviceLoopDiscoveryErrorKeepsDevices(t *testing.T) {
	fd := &fakeDisc{}
	reg := registry.New()
	reg.AddDiscoverer(fd)
	l := newDeviceLoop(reg, make(chan struct{}, 1))
	l.Logf = func(string, ...any) {}
	var removed []string
	l.OnRemoved = func(d device.Info) { removed = append(removed, d.ID) }
	fd.set("ios-A")
	l.Scan(context.Background())
	fd.err = errors.New("usbmuxd down")
	l.Scan(context.Background())
	if len(removed) != 0 || len(ids(reg)) != 1 {
		t.Fatalf("transient error must not drop devices: removed=%v list=%v", removed, ids(reg))
	}
}
