package httpapi

import (
	"phoneforge/internal/iosbridge"
	"phoneforge/internal/iossim"
)

// SetSim attaches the simulator control so the generic iOS bridge routes
// (/bridge, /iosbridge, /mjpeg) also work for "sim-<udid>" devices.
func (c *IOSControl) SetSim(s *iossim.Control) { c.Sim = s }

func (c *IOSControl) isSim(udid string) bool { return c.Sim != nil && c.Sim.IsSim(udid) }

// M returns the runner manager responsible for udid (simulator or real device).
func (c *IOSControl) M(udid string) *iosbridge.Manager {
	if c.isSim(udid) {
		return c.Sim.Mgr
	}
	return c.Mgr
}

// prepare applies simulator port allocation to start options.
func (c *IOSControl) prepare(udid string, o iosbridge.Options) iosbridge.Options {
	if c.isSim(udid) {
		return c.Sim.PrepareOpts(udid, o)
	}
	return o
}
