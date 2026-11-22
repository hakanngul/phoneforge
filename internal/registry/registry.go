// Package registry tracks connected devices and their drivers.
package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"pocketlab/internal/device"
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
	lastGood    map[int][]device.Device // last successful result per discoverer

	refreshMu sync.Mutex // serialises Refresh so scans never overlap
}

// DiscoverTimeout (a var only so tests can shorten it) bounds one discoverer's scan so a hung platform backend
// (e.g. a locked iPhone that never answers lockdown) cannot freeze the
// registry for every other platform.
var DiscoverTimeout = 8 * time.Second

// New returns an empty registry.
func New() *Registry {
	return &Registry{byID: map[string]device.Device{}, lastGood: map[int][]device.Device{}}
}

// AddDiscoverer registers a platform discoverer.
func (r *Registry) AddDiscoverer(d Discoverer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discoverers = append(r.discoverers, d)
}

// Refresh re-scans all discoverers (concurrently, each bounded by
// DiscoverTimeout) and updates the id->device map. A discoverer that fails or
// times out keeps its previous devices instead of blanking the list or
// aborting the other platforms; the joined errors are returned for logging.
func (r *Registry) Refresh(ctx context.Context) error {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	r.mu.RLock()
	ds := append([]Discoverer(nil), r.discoverers...)
	r.mu.RUnlock()

	type result struct {
		devs []device.Device
		err  error
	}
	res := make([]result, len(ds))
	var wg sync.WaitGroup
	for i, disc := range ds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, DiscoverTimeout)
			defer cancel()
			ch := make(chan result, 1)
			go func() {
				devs, err := disc.Discover(cctx)
				ch <- result{devs, err}
			}()
			select {
			case res[i] = <-ch:
			case <-cctx.Done():
				res[i] = result{err: fmt.Errorf("device scan timed out: %w", cctx.Err())}
			}
		}()
	}
	wg.Wait()

	var errs []error
	r.mu.Lock()
	next := map[string]device.Device{}
	for i := range ds {
		devs := res[i].devs
		if res[i].err != nil {
			errs = append(errs, res[i].err)
			devs = r.lastGood[i] // keep what we knew
		} else {
			r.lastGood[i] = devs
		}
		for _, dev := range devs {
			next[dev.Info().ID] = dev
		}
	}
	r.byID = next
	r.mu.Unlock()
	return errors.Join(errs...)
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
