package remotenode

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"phoneforge/internal/device"
	"phoneforge/internal/registry"
)

type discoverer struct{ store *Store }

// NewDiscoverer returns a registry.Discoverer that exposes the devices of
// every node in store. Unreachable nodes are skipped.
func NewDiscoverer(store *Store) registry.Discoverer { return &discoverer{store: store} }

// Discover queries each node's /api/v1/devices and wraps the results.
func (d *discoverer) Discover(ctx context.Context) ([]device.Device, error) {
	out := []device.Device{}
	for _, n := range d.store.List() {
		if ctx.Err() != nil {
			return out, nil
		}
		b, err := do(ctx, n, http.MethodGet, "/api/v1/devices", nil)
		if err != nil {
			log.Printf("remotenode: skipping node %q: %v", n.Name, err)
			continue
		}
		var infos []device.Info
		if err := json.Unmarshal(b, &infos); err != nil {
			log.Printf("remotenode: node %q returned bad device list: %v", n.Name, err)
			continue
		}
		for _, in := range infos {
			out = append(out, newRemoteDevice(n, in))
		}
	}
	return out, nil
}
