package server

import (
	"context"
	"log"
	"time"

	"pocketlab/internal/device"
	"pocketlab/internal/events"
	"pocketlab/internal/registry"
)

// RescanInterval is the safety-net poll period. usbmuxd Listen events trigger
// an immediate rescan; this covers Android (adb has no push), a dead listener
// and anything else that changes without a notification.
const RescanInterval = 3 * time.Second

// kickDebounce lets a burst of attach/detach messages (one plug-in produces a
// USB and often a Wi-Fi entry) collapse into one rescan.
const kickDebounce = 300 * time.Millisecond

// deviceLoop rescans the registry on a timer and whenever kick fires, diffs the
// device set, publishes connect/disconnect events and runs cleanup hooks.
type deviceLoop struct {
	reg      *registry.Registry
	interval time.Duration
	debounce time.Duration
	kick     <-chan struct{}
	known    map[string]device.Info

	// OnAdded / OnRemoved run on the loop goroutine after the registry already
	// reflects the change. Both may be nil.
	OnAdded   func(device.Info)
	OnRemoved func(device.Info)
	// AfterScan runs after every scan with the full device list. May be nil.
	AfterScan func([]device.Info)
	// Logf receives scan errors (nil: log.Printf).
	Logf func(format string, a ...any)
}

func newDeviceLoop(reg *registry.Registry, kick <-chan struct{}) *deviceLoop {
	return &deviceLoop{reg: reg, interval: RescanInterval, debounce: kickDebounce, kick: kick, known: map[string]device.Info{}}
}

// Baseline records the current devices without emitting events (call after the
// initial registry scan).
func (l *deviceLoop) Baseline() {
	for _, d := range l.reg.List() {
		l.known[d.ID] = d
	}
}

// Scan refreshes the registry once and reports changes.
func (l *deviceLoop) Scan(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, registry.DiscoverTimeout+2*time.Second)
	err := l.reg.Refresh(cctx)
	cancel()
	if err != nil && ctx.Err() == nil {
		logf := l.Logf
		if logf == nil {
			logf = log.Printf
		}
		logf("device rescan: %v", err)
	}
	cur := l.reg.List()
	seen := make(map[string]bool, len(cur))
	for _, d := range cur {
		seen[d.ID] = true
		if _, ok := l.known[d.ID]; !ok {
			l.known[d.ID] = d
			events.Success("Device connected: " + d.Name)
			if l.OnAdded != nil {
				l.OnAdded(d)
			}
		}
	}
	for id, d := range l.known {
		if !seen[id] {
			delete(l.known, id)
			events.Info("Device disconnected: " + d.Name)
			if l.OnRemoved != nil {
				l.OnRemoved(d)
			}
		}
	}
	if l.AfterScan != nil {
		l.AfterScan(cur)
	}
}

// Run scans every interval and on every kick until ctx ends.
func (l *deviceLoop) Run(ctx context.Context) {
	t := time.NewTicker(l.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-l.kick:
			// Let the burst settle (and the device finish enumerating).
			select {
			case <-ctx.Done():
				return
			case <-time.After(l.debounce):
			}
			select { // swallow kicks that arrived while waiting
			case <-l.kick:
			default:
			}
		}
		l.Scan(ctx)
	}
}
