package registry

import (
	"context"
	"sync"
	"time"

	"phoneforge/internal/device"
)

// DefaultGrace is how long a flapping (e.g. Wi-Fi paired iOS) device is kept
// in the registry after it vanishes from discovery.
const DefaultGrace = 10 * time.Second

type graceEntry struct {
	dev      device.Device
	lastSeen time.Time
}

// graceDiscoverer debounces disappearance: a device missing from the inner
// scan is still reported (as its last known object) until it has been absent
// for the full grace period. Appearance is immediate.
type graceDiscoverer struct {
	inner Discoverer
	grace time.Duration
	now   func() time.Time

	mu   sync.Mutex
	last map[string]graceEntry
}

// WithGrace wraps inner so devices are only dropped after being absent for
// grace. A non-positive grace returns inner unchanged. Scan errors pass
// through untouched (and do not age anything).
func WithGrace(inner Discoverer, grace time.Duration) Discoverer {
	return withGraceClock(inner, grace, time.Now)
}

func withGraceClock(inner Discoverer, grace time.Duration, now func() time.Time) Discoverer {
	if grace <= 0 {
		return inner
	}
	return &graceDiscoverer{inner: inner, grace: grace, now: now, last: map[string]graceEntry{}}
}

func (g *graceDiscoverer) Discover(ctx context.Context) ([]device.Device, error) {
	devs, err := g.inner.Discover(ctx)
	if err != nil {
		return nil, err
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	seen := make(map[string]bool, len(devs))
	for _, d := range devs {
		id := d.Info().ID
		seen[id] = true
		g.last[id] = graceEntry{dev: d, lastSeen: now}
	}
	out := append([]device.Device(nil), devs...)
	for id, e := range g.last {
		if seen[id] {
			continue
		}
		if now.Sub(e.lastSeen) < g.grace {
			out = append(out, e.dev)
		} else {
			delete(g.last, id)
		}
	}
	return out, nil
}
