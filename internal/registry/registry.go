// Package registry tracks connected devices and their drivers.
package registry

import (
	"context"
	"sort"
	"strings"
	"sync"

	"phoneforge/internal/device"
)

// Discoverer lists currently-connected devices for one platform.
type Discoverer interface {
	Discover(ctx context.Context) ([]device.Device, error)
}

// Registry aggregates devices from one or more discoverers.
type Registry struct {
	mu          sync.RWMutex
	discoverers []Discoverer
	byID        map[string]device.Device
}

// New returns an empty registry.
func New() *Registry { return &Registry{byID: map[string]device.Device{}} }

// AddDiscoverer registers a platform discoverer.
func (r *Registry) AddDiscoverer(d Discoverer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discoverers = append(r.discoverers, d)
}

// Refresh re-scans all discoverers and updates the id->device map.
func (r *Registry) Refresh(ctx context.Context) error {
	r.mu.RLock()
	ds := append([]Discoverer(nil), r.discoverers...)
	r.mu.RUnlock()

	next := map[string]device.Device{}
	for _, disc := range ds {
		devs, err := disc.Discover(ctx)
		if err != nil {
			return err
		}
		for _, dev := range devs {
			next[dev.Info().ID] = dev
		}
	}
	r.mu.Lock()
	r.byID = next
	r.mu.Unlock()
	return nil
}

// List returns all known devices in a stable order (local Android, iOS,
// remote; then by name, then id).
func (r *Registry) List() []device.Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]device.Info, 0, len(r.byID))
	for _, d := range r.byID {
		out = append(out, d.Info())
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rank(out[i]), rank(out[j])
		if ri != rj {
			return ri < rj
		}
		ni, nj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if ni != nj {
			return ni < nj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// rank orders devices: local Android, then iOS, then remote.
func rank(i device.Info) int {
	switch {
	case strings.HasPrefix(i.ID, "remote-"):
		return 2
	case i.Platform == device.PlatformIOS || i.Platform == device.PlatformSimulator:
		return 1
	}
	return 0
}

// Get returns a device by id, or device.ErrNotFound.
func (r *Registry) Get(id string) (device.Device, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.byID[id]
	if !ok {
		return nil, device.ErrNotFound
	}
	return d, nil
}
