package iossim

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"pocketlab/internal/brand"
	"pocketlab/internal/iossim/slim"
)

// ErrNotIOS means slimming was asked for a non-iOS simulator.
var ErrNotIOS = errors.New("iossim: only iOS simulators can be slimmed")

// SlimStatus describes the slimming state of one simulator.
type SlimStatus struct {
	Slimmed        bool      `json:"slimmed"`
	Profile        string    `json:"profile,omitempty"`
	ProfileVersion int       `json:"profileVersion,omitempty"`
	RuntimeID      string    `json:"runtimeId,omitempty"`
	Groups         []string  `json:"groups,omitempty"`
	Disabled       int       `json:"disabled"`
	AppliedAt      time.Time `json:"appliedAt,omitempty"`
	BaselineMB     float64   `json:"baselineMB,omitempty"`
	SlimMB         float64   `json:"slimMB,omitempty"`
	// Stale lists recorded labels that are running again (the simulator lost
	// its disabled state); re-run slim to fix.
	Stale []string `json:"stale,omitempty"`
	// Memory is the live measurement (booted simulators only).
	Memory *Memory `json:"memory,omitempty"`
}

// UnslimResult reports an unslim.
type UnslimResult struct {
	UDID     string   `json:"udid"`
	Enabled  []string `json:"enabled"`
	Failed   []string `json:"failed"`
	Rebooted bool     `json:"rebooted"`
}

// SetSlimPath sets the slim sidecar file (default ~/.pocketlab/iossim-slim.json).
func (c *Control) SetSlimPath(path string) {
	c.mu.Lock()
	c.slimPath = path
	c.slimEng = nil
	c.mu.Unlock()
}

func (c *Control) hostRun() HostRunner {
	if c.Host != nil {
		return c.Host
	}
	return ExecHost
}

func (c *Control) slimEngine() (*slim.Engine, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.slimEng != nil {
		return c.slimEng, nil
	}
	path := c.slimPath
	if path == "" {
		path = filepath.Join(brand.Home(), "iossim-slim.json")
	}
	e, err := slim.NewEngine(func(ctx context.Context, args ...string) ([]byte, error) { return c.Run(ctx, args...) }, slim.NewStore(path))
	if err != nil {
		return nil, err
	}
	e.Measure = func(ctx context.Context, udid string) (float64, error) {
		m, err := Measure(ctx, c.hostRun(), udid)
		return m.MB(), err
	}
	c.slimEng = e
	return e, nil
}

func (c *Control) iosBooted(ctx context.Context, udid string, needBooted bool) (Sim, error) {
	if runtime.GOOS != "darwin" {
		return Sim{}, ErrMacOnly
	}
	sim, err := c.Get(ctx, udid)
	if err != nil {
		return Sim{}, err
	}
	if sim.Platform != "iOS" {
		return Sim{}, ErrNotIOS
	}
	if needBooted && !sim.Booted() {
		return Sim{}, ErrNotBooted
	}
	return sim, nil
}

// Slim applies a slimming profile to a booted iOS simulator. WDA is stopped
// first (the supervisor restarts it when it was wanted) so it does not crash
// while daemons disappear.
func (c *Control) Slim(ctx context.Context, udid string, o slim.Options) (slim.Result, error) {
	sim, err := c.iosBooted(ctx, udid, true)
	if err != nil {
		return slim.Result{}, err
	}
	eng, err := c.slimEngine()
	if err != nil {
		return slim.Result{}, err
	}
	if !o.DryRun {
		_ = c.Mgr.Stop(udid)
	}
	res, err := eng.Apply(ctx, udid, sim.RuntimeID, o)
	if o.DryRun {
		return res, err
	}
	c.ClearBlocks(udid)
	c.setRebootNeeded(udid, err == nil && (len(res.Failed) > 0 || res.RebootNeeded))
	if err == nil && len(res.Applied) > 0 {
		go c.recordSlimMemory(udid)
	}
	return res, err
}

// recordSlimMemory stores the settled footprint (after ~30 s) in the record.
func (c *Control) recordSlimMemory(udid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	m, err := MeasureSettled(ctx, c.hostRun(), udid, 30*time.Second, 3, 5*time.Second)
	if err != nil {
		return
	}
	eng, err := c.slimEngine()
	if err != nil {
		return
	}
	if rec, ok := eng.Store.Get(udid); ok {
		rec.SlimMB = m.MB()
		_ = eng.Store.Put(rec)
	}
}

// reapplyAfterBoot re-runs the recorded profile after a boot: SpringBoard
// relaunches system apps (Spotlight, Calendar, widget renderer) that are not
// daemons, and a lost disabled state would bring daemons back.
func (c *Control) reapplyAfterBoot(sim Sim) {
	eng, err := c.slimEngine()
	if err != nil {
		return
	}
	rec, ok := eng.Store.Get(sim.UDID)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		o := recordOptions(rec)
		_, _ = eng.Apply(ctx, sim.UDID, sim.RuntimeID, o)
	}()
}

// recordOptions turns a stored record back into apply options.
func recordOptions(rec slim.Record) slim.Options {
	switch rec.Profile {
	case slim.PresetCustom:
		return slim.Options{Groups: rec.Groups}
	case "app":
		return slim.Options{Groups: rec.Groups, ProfileName: "app"}
	}
	return slim.Options{Preset: rec.Profile}
}

// Unslim re-enables every recorded label and reboots the simulator so the
// daemons come back (a simulator that was shut down is booted for the enable
// and shut down again).
func (c *Control) Unslim(ctx context.Context, udid string) (UnslimResult, error) {
	sim, err := c.iosBooted(ctx, udid, false)
	if err != nil {
		return UnslimResult{}, err
	}
	eng, err := c.slimEngine()
	if err != nil {
		return UnslimResult{}, err
	}
	if _, ok := eng.Store.Get(udid); !ok {
		return UnslimResult{}, slim.ErrNotSlimmed
	}
	wasBooted := sim.Booted()
	_ = c.Mgr.Stop(udid)
	if !wasBooted {
		if _, err := c.bootRaw(ctx, udid); err != nil {
			return UnslimResult{}, err
		}
	}
	en, failed, err := eng.Revert(ctx, udid)
	res := UnslimResult{UDID: udid, Enabled: en, Failed: failed}
	c.setRebootNeeded(udid, len(failed) > 0)
	if err != nil {
		return res, err
	}
	if _, err := c.Shutdown(ctx, udid); err != nil {
		return res, err
	}
	if wasBooted {
		if _, err := c.bootRaw(ctx, udid); err != nil {
			return res, err
		}
		res.Rebooted = true
		c.ClearBlocks(udid)
	}
	return res, nil
}

// bootRaw boots without the post-boot slim re-apply.
func (c *Control) bootRaw(ctx context.Context, udid string) (Sim, error) {
	c.mu.Lock()
	c.noReapply = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.noReapply = false
		c.mu.Unlock()
	}()
	return c.Boot(ctx, udid)
}

// SlimStatus reports the slim record for udid plus live memory when booted.
func (c *Control) SlimStatus(ctx context.Context, udid string) (SlimStatus, error) {
	sim, err := c.iosBooted(ctx, udid, false)
	if err != nil {
		return SlimStatus{}, err
	}
	eng, err := c.slimEngine()
	if err != nil {
		return SlimStatus{}, err
	}
	st := SlimStatus{}
	if rec, ok := eng.Store.Get(udid); ok {
		st = SlimStatus{Slimmed: true, Profile: rec.Profile, ProfileVersion: rec.ProfileVersion, RuntimeID: rec.RuntimeID,
			Groups: rec.Groups, Disabled: len(rec.Disabled), AppliedAt: rec.AppliedAt, BaselineMB: rec.BaselineMB, SlimMB: rec.SlimMB}
		if sim.Booted() {
			if stale, err := eng.Running(ctx, udid); err == nil {
				st.Stale = stale
			}
		}
	}
	if sim.Booted() {
		if m, err := Measure(ctx, c.hostRun(), udid); err == nil {
			st.Memory = &m
		}
	}
	return st, nil
}

// Memory measures a booted simulator.
func (c *Control) Memory(ctx context.Context, udid string) (Memory, error) {
	if _, err := c.iosBooted(ctx, udid, true); err != nil {
		return Memory{}, err
	}
	return Measure(ctx, c.hostRun(), udid)
}

// slimBrief is the per-simulator slim summary in the list.
type slimBrief struct {
	Profile      string   `json:"profile"`
	Disabled     int      `json:"disabled"`
	Groups       []string `json:"groups,omitempty"`
	BaselineMB   float64  `json:"baselineMB,omitempty"`
	SlimMB       float64  `json:"slimMB,omitempty"`
	RebootNeeded bool     `json:"rebootNeeded,omitempty"`
}

type memEntry struct {
	mb   float64
	at   time.Time
	busy bool
}

var memCache = struct {
	sync.Mutex
	m map[string]*memEntry
}{m: map[string]*memEntry{}}

// cachedMemoryMB returns the last measurement for a booted simulator (0 when
// none yet) and refreshes it in the background when older than 20 s, so the
// list route never waits on footprint(1).
func (c *Control) cachedMemoryMB(udid string) float64 {
	memCache.Lock()
	e := memCache.m[udid]
	if e == nil {
		e = &memEntry{}
		memCache.m[udid] = e
	}
	mb := e.mb
	if !e.busy && time.Since(e.at) > 20*time.Second {
		e.busy = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			m, err := Measure(ctx, c.hostRun(), udid)
			memCache.Lock()
			e.busy, e.at = false, time.Now()
			if err == nil {
				e.mb = m.MB()
			} else {
				e.mb = 0
			}
			memCache.Unlock()
		}()
	}
	memCache.Unlock()
	return mb
}

func (c *Control) slimBriefFor(udid string) *slimBrief {
	eng, err := c.slimEngine()
	if err != nil {
		return nil
	}
	rec, ok := eng.Store.Get(udid)
	if !ok {
		return nil
	}
	c.mu.Lock()
	rn := c.rebootNeeded[udid]
	c.mu.Unlock()
	return &slimBrief{Profile: rec.Profile, Disabled: len(rec.Disabled), Groups: rec.Groups, BaselineMB: rec.BaselineMB, SlimMB: rec.SlimMB, RebootNeeded: rn}
}

func (c *Control) setRebootNeeded(udid string, on bool) {
	c.mu.Lock()
	if on {
		c.rebootNeeded[udid] = true
	} else {
		delete(c.rebootNeeded, udid)
	}
	c.mu.Unlock()
}
