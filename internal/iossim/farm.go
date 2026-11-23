package iossim

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pocketlab/internal/brand"
	"pocketlab/internal/iossim/slim"
)

// FarmSim is one simulator created by a farm run.
type FarmSim struct {
	UDID string `json:"udid"`
	Name string `json:"name"`
	// Booted, Slimmed and Installed report what the run achieved.
	Booted    bool    `json:"booted"`
	Slimmed   bool    `json:"slimmed"`
	Profile   string  `json:"profile,omitempty"`
	Installed bool    `json:"installed,omitempty"`
	MemoryMB  float64 `json:"memoryMB,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// FarmRun is the record of one `sim farm` run (kept until --cleanup).
type FarmRun struct {
	ID        string    `json:"id"`
	Base      string    `json:"base"` // udid of the source
	BaseName  string    `json:"baseName"`
	CreatedAt time.Time `json:"createdAt"`
	Sims      []FarmSim `json:"sims"`
}

// FarmResult is the outcome of Farm: Status is "ok", "partial" or "failed".
type FarmResult struct {
	FarmRun
	Requested int    `json:"requested"`
	Succeeded int    `json:"succeeded"`
	Status    string `json:"status"`
	// RolledBack is set when strict mode deleted the run's clones again.
	RolledBack bool `json:"rolledBack,omitempty"`
}

// FarmCleanupResult reports Cleanup.
type FarmCleanupResult struct {
	Deleted []string          `json:"deleted"`
	Failed  map[string]string `json:"failed,omitempty"`
}

// farmStore persists farm runs (sidecar iossim-farm.json). Only udids in it
// are ever deleted by cleanup.
type farmStore struct {
	mu   sync.Mutex
	path string
	runs []FarmRun
}

func (c *Control) farmFile() string {
	c.mu.Lock()
	p := c.slimPath
	c.mu.Unlock()
	if p == "" {
		return filepath.Join(brand.Home(), "iossim-farm.json")
	}
	return filepath.Join(filepath.Dir(p), "iossim-farm.json")
}

func (c *Control) farm() *farmStore {
	c.mu.Lock()
	if c.farmSt != nil && c.farmSt.path == c.farmPathLocked() {
		defer c.mu.Unlock()
		return c.farmSt
	}
	c.mu.Unlock()
	fs := &farmStore{path: c.farmFile()}
	if b, err := os.ReadFile(fs.path); err == nil {
		var f struct {
			Runs []FarmRun `json:"runs"`
		}
		if json.Unmarshal(b, &f) == nil {
			fs.runs = f.Runs
		}
	}
	c.mu.Lock()
	c.farmSt = fs
	c.mu.Unlock()
	return fs
}

func (c *Control) farmPathLocked() string {
	if c.slimPath == "" {
		return filepath.Join(brand.Home(), "iossim-farm.json")
	}
	return filepath.Join(filepath.Dir(c.slimPath), "iossim-farm.json")
}

func (f *farmStore) saveLocked() {
	b, err := json.MarshalIndent(map[string]any{"runs": f.runs}, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(f.path), 0o755)
	tmp := f.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, f.path)
	}
}

// update mutates the run with id (creating it when missing) under the lock.
func (f *farmStore) update(run FarmRun, fn func(r *FarmRun)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.runs {
		if f.runs[i].ID == run.ID {
			fn(&f.runs[i])
			f.saveLocked()
			return
		}
	}
	f.runs = append(f.runs, run)
	fn(&f.runs[len(f.runs)-1])
	f.saveLocked()
}

// Runs returns the recorded farm runs.
func (c *Control) FarmRuns() []FarmRun {
	f := c.farm()
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FarmRun(nil), f.runs...)
}

// FarmOptions drives Farm.
type FarmOptions struct {
	From  string `json:"from"` // udid of the source simulator
	Count int    `json:"count"`
	SlimSpec
	// Install installs SlimSpec.App on every clone.
	Install bool `json:"install"`
	// ShutdownBase shuts a booted source down first (otherwise it is an error).
	ShutdownBase bool `json:"shutdownBase"`
	// Strict rolls the whole run back (deletes its clones) unless every clone succeeded.
	Strict bool `json:"strict"`
	// Parallel bounds concurrent boot+slim+install (default 3).
	Parallel int `json:"parallel"`
}

// Farm clones the base simulator Count times, boots each clone, slims it
// (Slim=true or an explicit profile/app) and optionally installs an app. Every
// clone is recorded before it is booted so Cleanup can remove it after a crash.
func (c *Control) Farm(ctx context.Context, o FarmOptions) (FarmResult, error) {
	if o.Count < 1 || o.Count > 20 {
		return FarmResult{}, inputErr("farm: count must be 1-20")
	}
	src, err := c.prepareSource(ctx, o.From, o.ShutdownBase)
	if err != nil {
		return FarmResult{}, err
	}
	run := FarmRun{ID: fmt.Sprintf("farm-%d", time.Now().Unix()), Base: src.UDID, BaseName: src.Name, CreatedAt: time.Now().UTC()}
	st := c.farm()
	st.update(run, func(*FarmRun) {})
	par := o.Parallel
	if par <= 0 {
		par = 3
	}
	sem := make(chan struct{}, par)
	results := make([]FarmSim, o.Count)
	var wg sync.WaitGroup
	for i := 0; i < o.Count; i++ {
		name := fmt.Sprintf("pl-farm-%s-%d", strings.ReplaceAll(src.Name, " ", "-"), i+1)
		nu, err := c.cloneOnce(ctx, src.UDID, name)
		if err != nil {
			results[i] = FarmSim{Name: name, Error: err.Error()}
			continue
		}
		fsim := FarmSim{UDID: nu, Name: name}
		st.update(run, func(r *FarmRun) { r.Sims = append(r.Sims, fsim) })
		results[i] = fsim
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, nu string) {
			defer wg.Done()
			defer func() { <-sem }()
			sp := o.SlimSpec
			if sp.Slim == nil && sp.Profile == "" && len(sp.Groups) == 0 && sp.App == "" {
				no := false
				sp.Slim = &no
			}
			lr := c.finish(ctx, nu, src.UDID, sp, true, o.Install, true)
			fs := results[i]
			fs.Booted = lr.Simulator.State == "Booted"
			if lr.Slim != nil {
				fs.Slimmed = true
				fs.Profile = lr.Slim.Profile
			}
			fs.Installed = lr.Installed
			fs.Error = lr.Error
			results[i] = fs
		}(i, nu)
	}
	wg.Wait()
	if c.OnChange != nil {
		c.OnChange()
	}
	res := FarmResult{FarmRun: run, Requested: o.Count}
	res.Sims = results
	for _, s := range results {
		if s.Error == "" && s.UDID != "" {
			res.Succeeded++
		}
	}
	st.update(run, func(r *FarmRun) {
		var keep []FarmSim
		for _, s := range results {
			if s.UDID != "" {
				keep = append(keep, s)
			}
		}
		r.Sims = keep
	})
	switch {
	case res.Succeeded == o.Count:
		res.Status = "ok"
	case res.Succeeded == 0:
		res.Status = "failed"
	default:
		res.Status = "partial"
	}
	if o.Strict && res.Status != "ok" {
		c.cleanupRun(ctx, run.ID)
		res.RolledBack = true
		res.Status = "failed"
	}
	return res, nil
}

// cleanupRun deletes the sims of one run (id "" = all runs).
func (c *Control) cleanupRun(ctx context.Context, id string) FarmCleanupResult {
	f := c.farm()
	f.mu.Lock()
	var todo []FarmSim
	for _, r := range f.runs {
		if id == "" || r.ID == id {
			todo = append(todo, r.Sims...)
		}
	}
	f.mu.Unlock()
	out := FarmCleanupResult{Deleted: []string{}}
	gone := map[string]bool{}
	for _, s := range todo {
		err := c.Delete(ctx, s.UDID, DeleteOptions{Shutdown: true})
		if err != nil && !strings.Contains(err.Error(), "not found") && !isNotFound(err) {
			if out.Failed == nil {
				out.Failed = map[string]string{}
			}
			out.Failed[s.UDID] = err.Error()
			continue
		}
		out.Deleted = append(out.Deleted, s.UDID)
		gone[s.UDID] = true
	}
	f.mu.Lock()
	var runs []FarmRun
	for _, r := range f.runs {
		if id != "" && r.ID != id {
			runs = append(runs, r)
			continue
		}
		var left []FarmSim
		for _, s := range r.Sims {
			if !gone[s.UDID] {
				left = append(left, s)
			}
		}
		if len(left) > 0 {
			r.Sims = left
			runs = append(runs, r)
		}
	}
	f.runs = runs
	f.saveLocked()
	f.mu.Unlock()
	return out
}

// FarmCleanup deletes every simulator recorded by a farm run (never anything
// else) and clears the record.
func (c *Control) FarmCleanup(ctx context.Context) FarmCleanupResult { return c.cleanupRun(ctx, "") }

var _ = slim.PresetAggressive
