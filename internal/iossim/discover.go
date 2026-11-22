package iossim

import (
	"context"
	"log"
	"sync"

	"pocketlab/internal/device"
	"pocketlab/internal/registry"
)

type discoverer struct {
	c    *Control
	once sync.Once
}

// NewDiscoverer lists booted iOS simulators as devices (id "sim-<udid>"),
// already wrapped so UI control goes through WDA while it runs. It never
// returns an error (a Mac without Xcode simply has no simulators).
func NewDiscoverer(c *Control) registry.Discoverer { return &discoverer{c: c} }

func (d *discoverer) Discover(ctx context.Context) ([]device.Device, error) {
	sims, err := d.c.List(ctx)
	if err != nil {
		d.once.Do(func() { log.Printf("simulators: %v", err) })
		return nil, nil
	}
	var out []device.Device
	for _, s := range sims {
		if s.Platform != "iOS" || !s.Booted() {
			continue
		}
		out = append(out, d.c.Wrap(newDevice(s, d.c.Run)))
	}
	return out, nil
}
