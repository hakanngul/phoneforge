package httpapi

import (
	"context"
	"sync"
	"time"

	"phoneforge/internal/avcapture"
	"phoneforge/internal/events"
)

// AVBackend is the native-capture surface the /mjpeg route needs
// (*avcapture.Manager implements it).
type AVBackend interface {
	Subscribe(ctx context.Context, udid string, o avcapture.Options, names ...string) (*avcapture.Subscription, error)
	Check(ctx context.Context, udid string, names ...string) error
}

// avInvalidator is optionally implemented by backends that cache listings.
type avInvalidator interface{ Invalidate() }

const (
	avKnownGoodWait = 900 * time.Millisecond // known-good av must deliver a frame this fast or WDA takes over
	avKnownGoodTTL  = 5 * time.Minute        // how long a successful stream/probe vouches for av
	avProbeEvery    = 60 * time.Second       // min spacing of background probes per device
	avProbeTimeout  = 20 * time.Second
)

type avState struct {
	goodAt    time.Time // last time av produced a frame
	probeAt   time.Time // last probe start
	probing   bool
	lastErr   string
	lastErrAt time.Time
}

// avGate decides, per device, whether /mjpeg may use native capture without
// risking a slow first frame. Unknown or failing av never blocks viewers: they
// get WDA while a throttled background probe checks whether av works.
type avGate struct {
	mu        sync.Mutex
	st        map[string]*avState
	now       func() time.Time
	supported func() bool
}

func newAVGate() *avGate {
	return &avGate{st: map[string]*avState{}, now: time.Now, supported: avcapture.Supported}
}

func (g *avGate) state(udid string) *avState {
	s := g.st[udid]
	if s == nil {
		s = &avState{}
		g.st[udid] = s
	}
	return s
}

// knownGood reports whether av recently produced frames for udid.
func (g *avGate) knownGood(udid string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.st[udid]
	return s != nil && !s.goodAt.IsZero() && g.now().Sub(s.goodAt) < avKnownGoodTTL
}

func (g *avGate) markGood(udid string) {
	g.mu.Lock()
	s := g.state(udid)
	s.goodAt, s.lastErr = g.now(), ""
	g.mu.Unlock()
}

func (g *avGate) markBad(udid, reason string) {
	g.mu.Lock()
	s := g.state(udid)
	s.goodAt = time.Time{}
	s.lastErr, s.lastErrAt = reason, g.now()
	g.mu.Unlock()
}

func (g *avGate) lastError(udid string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s := g.st[udid]; s != nil {
		return s.lastErr
	}
	return ""
}

// shouldProbe claims a probe slot: not known-good, none running, and the last
// one older than avProbeEvery.
func (g *avGate) shouldProbe(udid string) bool {
	if !g.supported() {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.state(udid)
	now := g.now()
	if s.probing || (!s.goodAt.IsZero() && now.Sub(s.goodAt) < avKnownGoodTTL) {
		return false
	}
	if !s.probeAt.IsZero() && now.Sub(s.probeAt) < avProbeEvery {
		return false
	}
	s.probing, s.probeAt = true, now
	return true
}

// maybeProbe starts a background probe if one is due. done (optional) is
// called when the probe finishes; it exists for tests.
func (g *avGate) maybeProbe(be AVBackend, udid string, names []string, done func()) {
	if be == nil || !g.shouldProbe(udid) {
		return
	}
	go func() {
		defer func() {
			g.mu.Lock()
			g.state(udid).probing = false
			g.mu.Unlock()
			if done != nil {
				done()
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), avProbeTimeout)
		defer cancel()
		sub, err := be.Subscribe(ctx, udid, avcapture.Options{}, names...)
		if err != nil {
			g.markBad(udid, err.Error())
			if inv, ok := be.(avInvalidator); ok {
				inv.Invalidate()
			}
			return
		}
		sub.Close()
		g.markGood(udid)
		events.Info("avcapture probe succeeded for " + udid + ": native capture will be used")
	}()
}
