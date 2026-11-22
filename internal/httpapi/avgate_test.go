package httpapi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"pocketlab/internal/avcapture"
)

type fakeAV struct {
	mu          sync.Mutex
	err         error
	subscribes  int
	invalidates int
	block       chan struct{}
}

func (f *fakeAV) Subscribe(ctx context.Context, udid string, o avcapture.Options, names ...string) (*avcapture.Subscription, error) {
	f.mu.Lock()
	f.subscribes++
	err, block := f.err, f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	ch := make(chan []byte, 1)
	ch <- []byte("jpeg")
	return avcapture.NewFakeSubscription(ch), nil
}
func (f *fakeAV) Check(ctx context.Context, udid string, names ...string) error { return f.err }
func (f *fakeAV) Invalidate()                                                   { f.mu.Lock(); f.invalidates++; f.mu.Unlock() }
func (f *fakeAV) count() int                                                    { f.mu.Lock(); defer f.mu.Unlock(); return f.subscribes }

func newTestGate() (*avGate, *time.Time) {
	now := time.Unix(1000, 0)
	g := newAVGate()
	g.supported = func() bool { return true }
	g.now = func() time.Time { return now }
	return g, &now
}

func runProbe(t *testing.T, g *avGate, be AVBackend) bool {
	t.Helper()
	done := make(chan struct{})
	before := 0
	if f, ok := be.(*fakeAV); ok {
		before = f.count()
	}
	g.maybeProbe(be, "u", nil, func() { close(done) })
	select {
	case <-done:
		return true
	case <-time.After(200 * time.Millisecond):
		f := be.(*fakeAV)
		return f.count() > before
	}
}

func TestGateUnknownIsNotKnownGood(t *testing.T) {
	g, _ := newTestGate()
	if g.knownGood("u") {
		t.Fatal("unknown device must not be known-good")
	}
}

func TestProbeSuccessMakesKnownGood(t *testing.T) {
	g, _ := newTestGate()
	f := &fakeAV{}
	runProbe(t, g, f)
	if !g.knownGood("u") {
		t.Fatal("expected known-good after successful probe")
	}
}

func TestProbeFailureThrottledAndInvalidates(t *testing.T) {
	g, now := newTestGate()
	f := &fakeAV{err: errors.New("TCC")}
	if !runProbe(t, g, f) {
		t.Fatal("first probe should run")
	}
	if g.knownGood("u") || g.lastError("u") == "" || f.invalidates != 1 {
		t.Fatalf("bad state: good=%v err=%q inv=%d", g.knownGood("u"), g.lastError("u"), f.invalidates)
	}
	g.maybeProbe(f, "u", nil, nil)
	time.Sleep(20 * time.Millisecond)
	if f.count() != 1 {
		t.Fatalf("probe within 60s must be throttled, subscribes=%d", f.count())
	}
	*now = now.Add(avProbeEvery + time.Second)
	runProbe(t, g, f)
	if f.count() != 2 {
		t.Fatalf("probe after throttle window should run, subscribes=%d", f.count())
	}
}

func TestNoProbeWhileProbingOrKnownGoodOrUnsupported(t *testing.T) {
	g, now := newTestGate()
	if !g.shouldProbe("u") {
		t.Fatal("first probe should be claimed")
	}
	if g.shouldProbe("u") {
		t.Fatal("concurrent probe must be refused")
	}
	g.mu.Lock()
	g.state("u").probing = false
	g.mu.Unlock()
	g.markGood("u")
	*now = now.Add(2 * avProbeEvery)
	if g.shouldProbe("u") {
		t.Fatal("known-good must not be probed")
	}
	g2, _ := newTestGate()
	g2.supported = func() bool { return false }
	if g2.shouldProbe("u") {
		t.Fatal("unsupported (PL_AVCAPTURE=0) must not probe")
	}
}

func TestKnownGoodExpiresAndMarkBadClears(t *testing.T) {
	g, now := newTestGate()
	g.markGood("u")
	*now = now.Add(avKnownGoodTTL + time.Second)
	if g.knownGood("u") {
		t.Fatal("known-good should expire")
	}
	g.markGood("u")
	g.markBad("u", "boom")
	if g.knownGood("u") || g.lastError("u") != "boom" {
		t.Fatal("markBad should clear known-good and record reason")
	}
}

func TestSlowProbeDoesNotBlockCaller(t *testing.T) {
	g, _ := newTestGate()
	f := &fakeAV{block: make(chan struct{})}
	start := time.Now()
	g.maybeProbe(f, "u", nil, nil)
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("maybeProbe must return immediately")
	}
	close(f.block)
}
