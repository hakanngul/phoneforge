package iossim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"pocketlab/internal/iossim/slim"
)

// ErrBooted means a lifecycle verb needs the simulator shut down first.
var ErrBooted = errors.New("iossim: simulator is booted; shut it down first (or pass shutdown)")

func inputErr(format string, a ...any) error {
	return &slim.InputError{Msg: fmt.Sprintf(format, a...)}
}

// DeviceType is one simulator device type (hardware model).
type DeviceType struct {
	ID     string `json:"id"`   // short form, e.g. "iPhone-17"
	Name   string `json:"name"` // "iPhone 17"
	Family string `json:"family,omitempty"`
	FullID string `json:"identifier,omitempty"`
}

// RuntimeInfo is one installed simulator runtime.
type RuntimeInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"` // "iOS 27.2"
	Platform  string `json:"platform"`
	Version   string `json:"version"`
	Available bool   `json:"available"`
}

// Catalog is what Create can choose from.
type Catalog struct {
	DeviceTypes []DeviceType  `json:"deviceTypes"`
	Runtimes    []RuntimeInfo `json:"runtimes"`
}

const deviceTypePrefix = "com.apple.CoreSimulator.SimDeviceType."

// ParseCatalog parses `simctl list devicetypes -j` and `simctl list runtimes -j`.
func ParseCatalog(types, runtimes []byte) (Catalog, error) {
	var t struct {
		DeviceTypes []struct {
			Identifier    string `json:"identifier"`
			Name          string `json:"name"`
			ProductFamily string `json:"productFamily"`
		} `json:"devicetypes"`
	}
	var r struct {
		Runtimes []struct {
			Identifier  string `json:"identifier"`
			Name        string `json:"name"`
			Platform    string `json:"platform"`
			Version     string `json:"version"`
			IsAvailable bool   `json:"isAvailable"`
		} `json:"runtimes"`
	}
	if err := json.Unmarshal(types, &t); err != nil {
		return Catalog{}, fmt.Errorf("simctl list devicetypes: %w", err)
	}
	if err := json.Unmarshal(runtimes, &r); err != nil {
		return Catalog{}, fmt.Errorf("simctl list runtimes: %w", err)
	}
	cat := Catalog{DeviceTypes: []DeviceType{}, Runtimes: []RuntimeInfo{}}
	for _, d := range t.DeviceTypes {
		cat.DeviceTypes = append(cat.DeviceTypes, DeviceType{ID: strings.TrimPrefix(d.Identifier, deviceTypePrefix), Name: d.Name, Family: d.ProductFamily, FullID: d.Identifier})
	}
	for _, x := range r.Runtimes {
		plat := x.Platform
		if plat == "" {
			plat, _ = parseRuntime(x.Identifier)
		}
		cat.Runtimes = append(cat.Runtimes, RuntimeInfo{ID: x.Identifier, Name: x.Name, Platform: plat, Version: x.Version, Available: x.IsAvailable})
	}
	return cat, nil
}

// Catalog lists device types and runtimes.
func (c *Control) Catalog(ctx context.Context) (Catalog, error) {
	t, err := c.Run(ctx, "list", "devicetypes", "-j")
	if err != nil {
		return Catalog{}, err
	}
	r, err := c.Run(ctx, "list", "runtimes", "-j")
	if err != nil {
		return Catalog{}, err
	}
	return ParseCatalog(t, r)
}

func norm(s string) string {
	return strings.NewReplacer(" ", "-", "_", "-").Replace(strings.ToLower(strings.TrimSpace(s)))
}

// ResolveDeviceType matches q against ids, full identifiers and names
// (case-insensitive, spaces and dashes equal) and returns the full identifier.
func (cat Catalog) ResolveDeviceType(q string) (string, error) {
	if strings.TrimSpace(q) == "" {
		return "", inputErr("create: device type is required (e.g. iPhone-17; see GET /simulators/catalog)")
	}
	nq := norm(q)
	for _, d := range cat.DeviceTypes {
		if norm(d.ID) == nq || norm(d.FullID) == nq || norm(d.Name) == nq {
			return d.FullID, nil
		}
	}
	return "", inputErr("create: unknown device type %q (see 'pocketlab sim create --help' or GET /simulators/catalog)", q)
}

// ResolveRuntime matches q ("", "iOS", "iOS 27.2", "27.2" or an identifier).
// "" means the newest available iOS runtime.
func (cat Catalog) ResolveRuntime(q string) (RuntimeInfo, error) {
	q = strings.TrimSpace(q)
	var avail []RuntimeInfo
	for _, r := range cat.Runtimes {
		if r.Available {
			avail = append(avail, r)
		}
	}
	newest := func(plat string) (RuntimeInfo, bool) {
		var best RuntimeInfo
		ok := false
		for _, r := range avail {
			if plat != "" && !strings.EqualFold(r.Platform, plat) {
				continue
			}
			if !ok || versionLess(best.Version, r.Version) {
				best, ok = r, true
			}
		}
		return best, ok
	}
	if q == "" {
		if r, ok := newest("iOS"); ok {
			return r, nil
		}
		return RuntimeInfo{}, inputErr("create: no iOS runtime is installed")
	}
	nq := norm(q)
	for _, r := range avail {
		if norm(r.ID) == nq || strings.HasSuffix(norm(r.ID), "."+nq) || norm(r.Name) == nq || r.Version == q && strings.EqualFold(r.Platform, "iOS") {
			return r, nil
		}
	}
	if r, ok := newest(q); ok { // a bare platform name
		return r, nil
	}
	return RuntimeInfo{}, inputErr("create: runtime %q is not installed (see GET /simulators/catalog)", q)
}

func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// SlimSpec says how to slim a freshly created or cloned simulator.
type SlimSpec struct {
	Slim    *bool    `json:"slim,omitempty"` // nil: inherit from the source, if any
	Profile string   `json:"profile,omitempty"`
	Groups  []string `json:"groups,omitempty"`
	App     string   `json:"app,omitempty"`
}

// options resolves the spec against the source record (src may be "").
func (c *Control) slimOptions(src string, sp SlimSpec) (slim.Options, bool) {
	var rec slim.Record
	has := false
	if src != "" {
		if eng, err := c.slimEngine(); err == nil {
			rec, has = eng.Store.Get(src)
		}
	}
	explicit := sp.App != "" || sp.Profile != "" || len(sp.Groups) > 0
	want := has || explicit
	if sp.Slim != nil {
		want = *sp.Slim
	}
	switch {
	case sp.App != "":
		return slim.Options{App: sp.App}, want
	case explicit:
		return slim.Options{Preset: sp.Profile, Groups: sp.Groups}, want
	case has:
		return recordOptions(rec), want
	}
	return slim.Options{}, want
}

// CreateOptions drives Create.
type CreateOptions struct {
	Name       string `json:"name"`
	DeviceType string `json:"deviceType"`
	Runtime    string `json:"runtime"`
	SlimSpec
	Boot    bool `json:"boot"`
	Install bool `json:"install"` // install SlimSpec.App after boot
}

// LifecycleResult is one created simulator.
type LifecycleResult struct {
	Simulator SimStatus    `json:"simulator"`
	Slim      *slim.Result `json:"slim,omitempty"`
	Installed bool         `json:"installed,omitempty"`
	Error     string       `json:"error,omitempty"`
}

var uuidRe = regexp.MustCompile(`[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`)

func parseUDID(out []byte) (string, error) {
	if u := uuidRe.FindString(string(out)); u != "" {
		return strings.ToUpper(u), nil
	}
	return "", fmt.Errorf("simctl: no udid in output %q", strings.TrimSpace(string(out)))
}

// finish boots, slims and installs a new simulator per the options.
func (c *Control) finish(ctx context.Context, udid string, src string, sp SlimSpec, boot, install bool, keepBooted bool) LifecycleResult {
	res := LifecycleResult{}
	opts, wantSlim := c.slimOptions(src, sp)
	setErr := func(err error) LifecycleResult {
		res.Error = err.Error()
		if s, e := c.Get(ctx, udid); e == nil {
			res.Simulator = c.status(s)
		}
		return res
	}
	needBoot := boot || wantSlim || (install && sp.App != "")
	if needBoot {
		if _, err := c.bootRaw(ctx, udid); err != nil {
			return setErr(err)
		}
	}
	if wantSlim {
		r, err := c.Slim(ctx, udid, opts)
		if err != nil {
			return setErr(err)
		}
		res.Slim = &r
		if src != "" { // the clone inherits the stock baseline of its source
			if eng, e := c.slimEngine(); e == nil {
				if sr, ok := eng.Store.Get(src); ok && sr.BaselineMB > 0 {
					if cr, ok := eng.Store.Get(udid); ok {
						cr.BaselineMB = sr.BaselineMB
						_ = eng.Store.Put(cr)
					}
				}
			}
		}
	}
	if install && sp.App != "" {
		if _, err := c.Run(ctx, "install", udid, sp.App); err != nil {
			return setErr(err)
		}
		res.Installed = true
	}
	if needBoot && !boot && !keepBooted {
		// Slimming needed a boot but the caller did not ask for a running
		// simulator: the disabled state persists, so shut it down again.
		_, _ = c.Shutdown(ctx, udid)
	}
	s, err := c.Get(ctx, udid)
	if err != nil {
		return setErr(err)
	}
	res.Simulator = c.status(s)
	return res
}

// Create makes a new simulator, optionally booting, slimming and installing an app.
func (c *Control) Create(ctx context.Context, o CreateOptions) (LifecycleResult, error) {
	cat, err := c.Catalog(ctx)
	if err != nil {
		return LifecycleResult{}, err
	}
	dt, err := cat.ResolveDeviceType(o.DeviceType)
	if err != nil {
		return LifecycleResult{}, err
	}
	rt, err := cat.ResolveRuntime(o.Runtime)
	if err != nil {
		return LifecycleResult{}, err
	}
	name := strings.TrimSpace(o.Name)
	if name == "" {
		name = "Pocketlab sim"
	}
	out, err := c.Run(ctx, "create", name, dt, rt.ID)
	if err != nil {
		return LifecycleResult{}, err
	}
	udid, err := parseUDID(out)
	if err != nil {
		return LifecycleResult{}, err
	}
	if c.OnChange != nil {
		c.OnChange()
	}
	return c.finish(ctx, udid, "", o.SlimSpec, o.Boot, o.Install, false), nil
}

// CloneOptions drives Clone.
type CloneOptions struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	SlimSpec
	// Shutdown shuts the (booted) source down first; without it a booted
	// source is an error.
	Shutdown bool `json:"shutdown"`
	Boot     bool `json:"boot"`
	Install  bool `json:"install"`
}

var cloneMu sync.Mutex // simctl clone of one source is serialised

// cloneOnce runs `simctl clone` and returns the new udid.
func (c *Control) cloneOnce(ctx context.Context, src, name string) (string, error) {
	cloneMu.Lock()
	defer cloneMu.Unlock()
	out, err := c.Run(ctx, "clone", src, name)
	if err != nil {
		return "", err
	}
	return parseUDID(out)
}

// prepareSource makes sure src is shut down (when allowed).
func (c *Control) prepareSource(ctx context.Context, udid string, shutdown bool) (Sim, error) {
	sim, err := c.Get(ctx, udid)
	if err != nil {
		return Sim{}, err
	}
	if sim.State != "Shutdown" {
		if !shutdown {
			return Sim{}, ErrBooted
		}
		if sim, err = c.Shutdown(ctx, udid); err != nil {
			return Sim{}, err
		}
	}
	return sim, nil
}

// Clone duplicates a simulator Count times. The source must be shut down
// (shutdown=true shuts it down). Slimming does not survive `simctl clone`, so
// a slimmed source's profile (or an explicit one) is re-applied to every clone.
func (c *Control) Clone(ctx context.Context, udid string, o CloneOptions) ([]LifecycleResult, error) {
	src, err := c.prepareSource(ctx, udid, o.Shutdown)
	if err != nil {
		return nil, err
	}
	n := o.Count
	if n <= 0 {
		n = 1
	}
	if n > 20 {
		return nil, inputErr("clone: count %d is too high (max 20)", n)
	}
	base := strings.TrimSpace(o.Name)
	if base == "" {
		base = src.Name + " clone"
	}
	var out []LifecycleResult
	for i := 1; i <= n; i++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s %d", base, i)
		}
		nu, err := c.cloneOnce(ctx, udid, name)
		if err != nil {
			if len(out) == 0 {
				return nil, err
			}
			out = append(out, LifecycleResult{Error: err.Error()})
			break
		}
		if c.OnChange != nil {
			c.OnChange()
		}
		out = append(out, c.finish(ctx, nu, udid, o.SlimSpec, o.Boot, o.Install, false))
	}
	return out, nil
}

// EraseOptions drives Erase.
type EraseOptions struct {
	KeepSlim bool `json:"keepSlim"`
	Shutdown bool `json:"shutdown"`
}

// Erase wipes a simulator's content and settings. The disabled-daemon state
// lives outside the data directory and survives an erase, so without KeepSlim
// the slim record is reverted first (the simulator is booted briefly to
// re-enable the labels); with KeepSlim the record stays and the profile is
// re-applied after the next boot.
func (c *Control) Erase(ctx context.Context, udid string, o EraseOptions) (SimStatus, error) {
	sim, err := c.prepareSource(ctx, udid, o.Shutdown)
	if err != nil {
		return SimStatus{}, err
	}
	_ = c.Mgr.Stop(udid)
	if eng, err := c.slimEngine(); err == nil && sim.Platform == "iOS" {
		if _, ok := eng.Store.Get(udid); ok && !o.KeepSlim {
			if _, err := c.Unslim(ctx, udid); err != nil {
				return SimStatus{}, fmt.Errorf("erase: could not clear slimming first: %w", err)
			}
		}
	}
	if _, err := c.Run(ctx, "erase", udid); err != nil {
		return SimStatus{}, err
	}
	if c.OnChange != nil {
		c.OnChange()
	}
	s, err := c.Get(ctx, udid)
	if err != nil {
		return SimStatus{}, err
	}
	return c.status(s), nil
}

// DeleteOptions drives Delete.
type DeleteOptions struct {
	Shutdown bool `json:"shutdown"`
}

// Delete removes a simulator and forgets its slim record and WDA state.
func (c *Control) Delete(ctx context.Context, udid string, o DeleteOptions) error {
	if _, err := c.prepareSource(ctx, udid, o.Shutdown); err != nil {
		return err
	}
	c.StopWDA(udid)
	_ = c.Mgr.SetDesired(udid, false)
	if _, err := c.Run(ctx, "delete", udid); err != nil {
		return err
	}
	if eng, err := c.slimEngine(); err == nil {
		_ = eng.Store.Delete(udid)
	}
	c.forget(udid)
	// launchd keeps the disabled set in a per-device directory that simctl
	// delete leaves behind.
	if uuidRe.MatchString(udid) && len(udid) == 36 {
		_ = os.RemoveAll(filepath.Join("/private/var/tmp", "com.apple.CoreSimulator.SimDevice."+udid))
	}
	if c.OnChange != nil {
		c.OnChange()
	}
	return nil
}

// forget drops all in-memory state for a deleted simulator.
func (c *Control) forget(udid string) {
	c.mu.Lock()
	delete(c.clients, udid)
	delete(c.known, udid)
	delete(c.slots, udid)
	delete(c.lastPorts, udid)
	delete(c.rebootNeeded, udid)
	c.mu.Unlock()
	memCache.Lock()
	delete(memCache.m, udid)
	memCache.Unlock()
}

// SlimRequest is the REST/CLI slim request.
type SlimRequest struct {
	Profile string   `json:"profile"`
	Groups  []string `json:"groups"`
	DryRun  bool     `json:"dryRun"`
	App     string   `json:"app"`
	// Boot boots the simulator first when it is not booted.
	Boot bool `json:"boot"`
	// Install installs App after slimming.
	Install bool `json:"install"`
}

// SlimRequest applies a slim request (see SlimRequest).
func (c *Control) SlimRequest(ctx context.Context, udid string, r SlimRequest) (slim.Result, error) {
	if r.Boot && !r.DryRun {
		sim, err := c.iosBooted(ctx, udid, false)
		if err != nil {
			return slim.Result{}, err
		}
		if !sim.Booted() {
			if _, err := c.bootRaw(ctx, udid); err != nil {
				return slim.Result{}, err
			}
		}
	}
	res, err := c.Slim(ctx, udid, slim.Options{Preset: r.Profile, Groups: r.Groups, DryRun: r.DryRun, App: r.App})
	if err != nil || r.DryRun {
		return res, err
	}
	if r.Install && r.App != "" {
		if _, err := c.Run(ctx, "install", udid, r.App); err != nil {
			return res, err
		}
	}
	return res, nil
}

var _ = sort.Strings
