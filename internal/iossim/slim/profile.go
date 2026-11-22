// Package slim turns off simulator background daemons (Siri, Spotlight, photo
// analysis, widgets, ...) with launchctl inside a booted simulator so several
// simulators fit in memory. Label lists live in an embedded, versioned JSON
// profile built from our own measurements (see
// docs/plans/simulator-slimming-spike.md); unknown labels are skipped and
// reported, so an Apple rename degrades to "less slimming", never a broken
// simulator.
package slim

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed profiles.json
var profilesJSON []byte

// Preset names.
const (
	PresetMinimalSafe = "minimal-safe"
	PresetAggressive  = "aggressive"
	PresetCustom      = "custom"
)

// keepPrefixes is the hard keep list (see IsKept for matching): labels matching are
// never disabled, whatever a profile says. It covers boot-critical services
// (bootstatus hangs without nanoregistryd), the test/automation stack
// (testmanagerd, XCTest, WDA, accessibility), process lifecycle, networking,
// text input, pasteboard, launch/install services, push and location.
var keepPrefixes = []string{
	"com.apple.SpringBoard", "com.apple.backboardd", "com.apple.UIKit.", "com.apple.InputUI",
	"com.apple.TextInput", "com.apple.accessibility.", "com.apple.AccessibilityUIServer",
	"com.apple.testmanagerd", "com.apple.dt.", "com.apple.coredevice.", "com.apple.instruments.",
	"com.apple.runningboardd", "com.apple.assertiond",
	"com.apple.configd_sim", "com.apple.mDNSResponder", "com.apple.nsurlsessiond", "com.apple.cfnetwork",
	"com.apple.pasteboard", "com.apple.lsd", "com.apple.mobile.installd", "com.apple.MobileInstallationHelperService",
	"com.apple.containermanagerd", "com.apple.cfprefsd", "com.apple.notifyd", "com.apple.distnoted",
	"com.apple.apsd", "com.apple.locationd", "com.apple.identityservicesd", "com.apple.ids_simd",
	"com.apple.nanoregistryd", "com.apple.CoreSimulator.", "com.apple.UserEventAgent-System", "com.apple.xpc.",
	"com.apple.securityd", "com.apple.trustd", "com.apple.tccd", "com.apple.mobileassetd",
	"com.apple.datamigrator", "com.apple.purplebuddy", "com.apple.iconservices", "com.apple.pluginkit",
	"com.apple.logd", "com.apple.diagnosticd", "com.apple.syslogd", "com.apple.aslmanager",
	"com.apple.fontservicesd", "com.apple.mobilegestalt", "com.apple.webinspectord",
	"com.apple.accountsd", "com.apple.launchservicesd",
}

// IsKept reports whether label is on the hard keep list. A pattern ending in
// "." is a plain prefix; any other pattern matches the label itself or
// "<pattern>.<anything>", so "com.apple.logd" does not cover "com.apple.logd_reporter".
func IsKept(label string) bool {
	for _, p := range keepPrefixes {
		if strings.HasSuffix(p, ".") {
			if strings.HasPrefix(label, p) {
				return true
			}
		} else if label == p || strings.HasPrefix(label, p+".") {
			return true
		}
	}
	return false
}

// KeepList returns a copy of the keep prefixes (for docs and --discover).
func KeepList() []string { return append([]string(nil), keepPrefixes...) }

// GroupDef is one group's targets for one runtime major.
type GroupDef struct {
	Labels []string `json:"labels"`
	// Apps are bundle ids terminated with `simctl terminate` (SpringBoard
	// launches some system apps at boot; they are not separate daemons).
	Apps []string `json:"apps,omitempty"`
}

// Preset is a named set of groups.
type Preset struct {
	Description string   `json:"description"`
	Groups      []string `json:"groups"`
}

// Profile is the parsed embedded profile.
type Profile struct {
	Version  int                            `json:"version"`
	Presets  map[string]Preset              `json:"presets"`
	Groups   map[string]string              `json:"groups"`
	Runtimes map[string]map[string]GroupDef `json:"runtimes"`
}

// Load parses the embedded profile.
func Load() (*Profile, error) { return parse(profilesJSON) }

func parse(b []byte) (*Profile, error) {
	var p Profile
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("slim profile: %w", err)
	}
	return &p, nil
}

// GroupNames returns all group names, sorted.
func (p *Profile) GroupNames() []string {
	out := make([]string, 0, len(p.Groups))
	for g := range p.Groups {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

var rtMajorRe = regexp.MustCompile(`SimRuntime\.iOS-(\d+)`)

// RuntimeMajor extracts the iOS major from a runtime id such as
// "com.apple.CoreSimulator.SimRuntime.iOS-27-2".
func RuntimeMajor(runtimeID string) (int, bool) {
	m := rtMajorRe.FindStringSubmatch(runtimeID)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, true
}

// runtimeKey picks the profile key for major: the exact major or the nearest
// lower one.
func (p *Profile) runtimeKey(major int) (string, bool) {
	best, bestN := "", -1
	for k := range p.Runtimes {
		n, err := strconv.Atoi(k)
		if err != nil || n > major {
			continue
		}
		if n > bestN {
			best, bestN = k, n
		}
	}
	return best, bestN >= 0
}

// Plan is a resolved slimming plan for one runtime.
type Plan struct {
	Preset     string   `json:"profile"`
	Version    int      `json:"profileVersion"`
	RuntimeKey string   `json:"runtimeKey"`
	Groups     []string `json:"groups"`
	Labels     []string `json:"labels"`
	Apps       []string `json:"apps,omitempty"`
	// Protected lists profile labels dropped by the hard keep list.
	Protected []string `json:"protected,omitempty"`
}

// InputError marks a request the caller can fix (unknown group, unsupported
// runtime); surfaces map it to a 400.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func inputErr(format string, a ...any) error { return &InputError{Msg: fmt.Sprintf(format, a...)} }

// Resolve builds the plan for a runtime id, a preset ("" = minimal-safe) and,
// for the custom preset (or when groups are given), explicit groups.
func (p *Profile) Resolve(runtimeID, preset string, groups []string) (Plan, error) {
	if preset == "" {
		preset = PresetMinimalSafe
	}
	if len(groups) > 0 {
		preset = PresetCustom
	}
	var names []string
	switch preset {
	case PresetCustom:
		if len(groups) == 0 {
			return Plan{}, inputErr("slim: custom preset needs --groups (one of %s)", strings.Join(p.GroupNames(), ", "))
		}
		for _, g := range groups {
			g = strings.TrimSpace(g)
			if _, ok := p.Groups[g]; !ok {
				return Plan{}, inputErr("slim: unknown group %q (have %s)", g, strings.Join(p.GroupNames(), ", "))
			}
			names = append(names, g)
		}
	default:
		pr, ok := p.Presets[preset]
		if !ok {
			return Plan{}, inputErr("slim: unknown profile %q (minimal-safe, aggressive, custom)", preset)
		}
		names = pr.Groups
	}
	major, ok := RuntimeMajor(runtimeID)
	if !ok {
		return Plan{}, inputErr("slim: only iOS simulators can be slimmed (runtime %q)", runtimeID)
	}
	key, ok := p.runtimeKey(major)
	if !ok {
		return Plan{}, inputErr("slim: no profile for iOS %d (oldest supported is a lower major than any installed profile)", major)
	}
	plan := Plan{Preset: preset, Version: p.Version, RuntimeKey: key}
	seenL, seenA, seenG := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, g := range names {
		if seenG[g] {
			continue
		}
		seenG[g] = true
		plan.Groups = append(plan.Groups, g)
		def := p.Runtimes[key][g]
		for _, l := range def.Labels {
			if seenL[l] {
				continue
			}
			seenL[l] = true
			if IsKept(l) {
				plan.Protected = append(plan.Protected, l)
				continue
			}
			plan.Labels = append(plan.Labels, l)
		}
		for _, a := range def.Apps {
			if !seenA[a] {
				seenA[a] = true
				plan.Apps = append(plan.Apps, a)
			}
		}
	}
	return plan, nil
}

// LabelGroups maps each label of the runtime's profile to the groups that
// target it (used by --discover).
func (p *Profile) LabelGroups(runtimeID string) map[string][]string {
	out := map[string][]string{}
	major, ok := RuntimeMajor(runtimeID)
	if !ok {
		return out
	}
	key, ok := p.runtimeKey(major)
	if !ok {
		return out
	}
	for g, def := range p.Runtimes[key] {
		for _, l := range def.Labels {
			out[l] = append(out[l], g)
		}
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}
