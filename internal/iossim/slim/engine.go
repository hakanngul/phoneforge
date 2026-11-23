package slim

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Runner executes `xcrun simctl <args>` and returns stdout (same shape as
// iossim.Runner, which is assignable to it).
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// Options selects what to apply.
type Options struct {
	Preset string   // "", minimal-safe, aggressive, custom
	Groups []string // explicit groups (implies custom)
	DryRun bool
	// App is a built .app bundle: the groups it needs are kept and everything
	// else in the aggressive profile is disabled (profile name "app").
	App string
	// ProfileName overrides the recorded profile name (used to re-apply an
	// "app" record from its stored groups).
	ProfileName string
}

// Result reports one Apply.
type Result struct {
	UDID           string   `json:"udid"`
	Profile        string   `json:"profile"`
	ProfileVersion int      `json:"profileVersion"`
	RuntimeID      string   `json:"runtimeId"`
	RuntimeKey     string   `json:"runtimeKey"`
	Groups         []string `json:"groups"`
	Applied        []string `json:"applied"`
	Missing        []string `json:"missing"`
	Failed         []string `json:"failed"`
	Protected      []string `json:"protected,omitempty"`
	Apps           []string `json:"apps,omitempty"`
	// App and KeptBecause are set for app-aware slimming: the scanned bundle
	// and why each group was left enabled.
	App         string   `json:"app,omitempty"`
	KeptBecause []string `json:"keptBecause,omitempty"`
	// Informational lists app signals that need no group (push, fetch...).
	Informational []string `json:"informational,omitempty"`
	// Reenabled lists labels of an earlier, larger profile that were enabled
	// again; their daemons only come back after the next reboot (RebootNeeded).
	Reenabled    []string `json:"reenabled,omitempty"`
	RebootNeeded bool     `json:"rebootNeeded,omitempty"`
	DryRun       bool     `json:"dryRun,omitempty"`
	BaselineMB   float64  `json:"baselineMB,omitempty"`
	SlimMB       float64  `json:"slimMB,omitempty"`
	// Errors maps a failed label to its error text.
	Errors map[string]string `json:"errors,omitempty"`
}

// Engine applies and reverts profiles on booted simulators.
type Engine struct {
	Run     Runner
	Store   *Store
	Profile *Profile
	// Parallel bounds concurrent launchctl calls (default 8).
	Parallel int
	// LabelTimeout bounds each launchctl call (default 15s).
	LabelTimeout time.Duration
	// Measure, if set, returns the simulator's current memory in MB; it is
	// used for BaselineMB before the first change.
	Measure func(ctx context.Context, udid string) (float64, error)
	Now     func() time.Time
}

// NewEngine returns an Engine over the embedded profile.
func NewEngine(run Runner, store *Store) (*Engine, error) {
	p, err := Load()
	if err != nil {
		return nil, err
	}
	return &Engine{Run: run, Store: store, Profile: p}, nil
}

func (e *Engine) parallel() int {
	if e.Parallel > 0 {
		return e.Parallel
	}
	return 8
}

func (e *Engine) labelCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	d := e.LabelTimeout
	if d <= 0 {
		d = 15 * time.Second
	}
	return context.WithTimeout(ctx, d)
}

func (e *Engine) launchctl(ctx context.Context, udid string, args ...string) ([]byte, error) {
	return e.Run(ctx, append([]string{"spawn", udid, "launchctl"}, args...)...)
}

// uidDomain reads the simulator user's uid and returns "user/<uid>".
func (e *Engine) uidDomain(ctx context.Context, udid string) (string, error) {
	b, err := e.launchctl(ctx, udid, "manageruid")
	if err != nil {
		return "", err
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return "", fmt.Errorf("slim: cannot read simulator uid from %q", strings.TrimSpace(string(b)))
	}
	return "user/" + strconv.Itoa(uid), nil
}

// ParseList returns the labels in `launchctl list` output.
func ParseList(data []byte) map[string]bool {
	out := map[string]bool{}
	for i, line := range strings.Split(string(data), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 3 || (i == 0 && f[0] == "PID") {
			continue
		}
		out[f[2]] = true
	}
	return out
}

// ParseRunning returns the labels in `launchctl list` output that have a pid.
func ParseRunning(data []byte) map[string]bool {
	out := map[string]bool{}
	for i, line := range strings.Split(string(data), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 3 || (i == 0 && f[0] == "PID") || f[0] == "-" {
			continue
		}
		out[f[2]] = true
	}
	return out
}

// ParseDisabled returns the labels reported `=> disabled` by
// `launchctl print-disabled`.
func ParseDisabled(data []byte) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, "=> disabled") && !strings.HasSuffix(line, "=> true") {
			continue
		}
		if i := strings.Index(line, `"`); i >= 0 {
			if j := strings.Index(line[i+1:], `"`); j >= 0 {
				out[line[i+1:i+1+j]] = true
			}
		}
	}
	return out
}

// Apply slims the booted simulator udid. runtimeID selects the label list.
// Each present label is disabled (persisted by launchd across reboot and
// erase) and booted out; labels that are neither loaded nor already disabled
// are reported missing. The sidecar record is written only when something was
// disabled.
func (e *Engine) Apply(ctx context.Context, udid, runtimeID string, o Options) (Result, error) {
	var kept, info []string
	if o.App != "" {
		g, k, i, err := e.appGroups(o.App)
		if err != nil {
			return Result{}, err
		}
		o.Groups, kept, info = g, k, i
		if len(g) == 0 { // the app needs everything aggressive would disable
			return Result{UDID: udid, Profile: "app", RuntimeID: runtimeID, App: o.App, KeptBecause: k, Informational: i,
				Groups: []string{}, Applied: []string{}, Missing: []string{}, Failed: []string{}, DryRun: o.DryRun}, nil
		}
		if o.ProfileName == "" {
			o.ProfileName = "app"
		}
	}
	plan, err := e.Profile.Resolve(runtimeID, o.Preset, o.Groups)
	if err != nil {
		return Result{}, err
	}
	if o.ProfileName != "" {
		plan.Preset = o.ProfileName
	}
	res := Result{UDID: udid, Profile: plan.Preset, ProfileVersion: plan.Version, RuntimeID: runtimeID,
		RuntimeKey: plan.RuntimeKey, Groups: plan.Groups, Protected: plan.Protected, Apps: plan.Apps, DryRun: o.DryRun,
		App: o.App, KeptBecause: kept, Informational: info,
		Applied: []string{}, Missing: []string{}, Failed: []string{}}
	domain, err := e.uidDomain(ctx, udid)
	if err != nil {
		return res, err
	}
	loaded, disabled := map[string]bool{}, map[string]bool{}
	if b, err := e.launchctl(ctx, udid, "list"); err != nil {
		return res, err
	} else {
		loaded = ParseList(b)
	}
	if b, err := e.launchctl(ctx, udid, "print-disabled", domain); err == nil {
		disabled = ParseDisabled(b)
	}
	var todo []string
	for _, l := range plan.Labels {
		switch {
		case IsKept(l): // defence in depth: Resolve already filtered
			res.Protected = append(res.Protected, l)
		case !loaded[l] && !disabled[l]:
			res.Missing = append(res.Missing, l)
		default:
			todo = append(todo, l)
		}
	}
	if o.DryRun {
		res.Applied = append(res.Applied, todo...)
		return res, nil
	}
	if e.Measure != nil {
		if mb, err := e.Measure(ctx, udid); err == nil {
			res.BaselineMB = mb
		}
	}
	type outcome struct {
		ok  bool
		err error
	}
	outs := make([]outcome, len(todo))
	sem := make(chan struct{}, e.parallel())
	var wg sync.WaitGroup
	for i, l := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			lctx, cancel := e.labelCtx(ctx)
			defer cancel()
			target := domain + "/" + l
			if !disabled[l] {
				if _, err := e.launchctl(lctx, udid, "disable", target); err != nil {
					outs[i] = outcome{err: err}
					return
				}
			}
			if loaded[l] {
				// A bootout failure leaves the disable in place (the daemon is
				// gone after the next boot), so it is not a failed label.
				_, _ = e.launchctl(lctx, udid, "bootout", target)
			}
			outs[i] = outcome{ok: true}
		}()
	}
	wg.Wait()
	for i, l := range todo {
		if outs[i].ok {
			res.Applied = append(res.Applied, l)
		} else {
			res.Failed = append(res.Failed, l)
			if res.Errors == nil {
				res.Errors = map[string]string{}
			}
			res.Errors[l] = outs[i].err.Error()
		}
	}
	var keepRecorded []string
	if e.Store != nil {
		inPlan := map[string]bool{}
		for _, l := range plan.Labels {
			inPlan[l] = true
		}
		for _, l := range e.recorded(udid) {
			if inPlan[l] {
				keepRecorded = append(keepRecorded, l)
				continue
			}
			// Switching to a smaller profile: give the extra labels back.
			lctx, cancel := e.labelCtx(ctx)
			if _, err := e.launchctl(lctx, udid, "enable", domain+"/"+l); err != nil {
				keepRecorded = append(keepRecorded, l) // still disabled: keep tracking it
			} else {
				res.Reenabled = append(res.Reenabled, l)
			}
			cancel()
		}
		res.RebootNeeded = len(res.Reenabled) > 0
	}
	for _, a := range plan.Apps {
		actx, cancel := e.labelCtx(ctx)
		_, _ = e.Run(actx, "terminate", udid, a)
		cancel()
	}
	if (len(res.Applied) > 0 || len(res.Reenabled) > 0) && e.Store != nil {
		now := time.Now
		if e.Now != nil {
			now = e.Now
		}
		rec := Record{UDID: udid, Profile: plan.Preset, ProfileVersion: plan.Version, RuntimeID: runtimeID,
			Groups: plan.Groups, Disabled: mergeUnique(keepRecorded, res.Applied), Apps: plan.Apps,
			AppliedAt: now().UTC(), BaselineMB: res.BaselineMB}
		if old, ok := e.Store.Get(udid); ok && old.BaselineMB > 0 {
			rec.BaselineMB = old.BaselineMB // keep the first, stock baseline
			res.BaselineMB = old.BaselineMB
		}
		if err := e.Store.Put(rec); err != nil {
			return res, err
		}
	}
	return res, nil
}

func (e *Engine) recorded(udid string) []string {
	if e.Store == nil {
		return nil
	}
	r, _ := e.Store.Get(udid)
	return r.Disabled
}

func mergeUnique(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range append(append([]string(nil), a...), b...) {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// Revert enables every recorded label (best effort) and drops the sidecar
// record. The caller reboots the simulator afterwards so the daemons start.
// It returns the labels that were re-enabled and those that failed.
func (e *Engine) Revert(ctx context.Context, udid string) (enabled, failed []string, err error) {
	rec, ok := e.Store.Get(udid)
	if !ok {
		return nil, nil, ErrNotSlimmed
	}
	domain, err := e.uidDomain(ctx, udid)
	if err != nil {
		return nil, nil, err
	}
	enabled, failed = []string{}, []string{}
	for _, l := range rec.Disabled {
		lctx, cancel := e.labelCtx(ctx)
		_, err := e.launchctl(lctx, udid, "enable", domain+"/"+l)
		cancel()
		if err != nil {
			failed = append(failed, l)
		} else {
			enabled = append(enabled, l)
		}
	}
	if len(failed) == 0 {
		if err := e.Store.Delete(udid); err != nil {
			return enabled, failed, err
		}
	}
	return enabled, failed, nil
}

// Running returns the recorded labels that currently have a running process
// in the simulator (drift: a daemon came back, e.g. after the disabled state
// was lost).
func (e *Engine) Running(ctx context.Context, udid string) ([]string, error) {
	rec, ok := e.Store.Get(udid)
	if !ok {
		return nil, ErrNotSlimmed
	}
	b, err := e.launchctl(ctx, udid, "list")
	if err != nil {
		return nil, err
	}
	running := ParseRunning(b)
	var out []string
	for _, l := range rec.Disabled {
		if running[l] {
			out = append(out, l)
		}
	}
	return out, nil
}

// appGroups scans appPath and returns aggressive minus the groups the app
// needs, with a "kept because" line per kept group and the informational
// (group-less) signals.
func (e *Engine) appGroups(appPath string) (groups, kept, info []string, err error) {
	ai, err := ScanApp(appPath)
	if err != nil {
		return nil, nil, nil, inputErr("slim: %v", err)
	}
	m, err := LoadAppMap()
	if err != nil {
		return nil, nil, nil, err
	}
	reasons, infos := m.Needed(ai, e.Profile.Groups)
	groups = AppGroups(e.Profile.Presets[PresetAggressive].Groups, reasons)
	for _, r := range reasons {
		kept = append(kept, r.String())
	}
	for _, r := range infos {
		info = append(info, r.String())
	}
	return groups, kept, info, nil
}
