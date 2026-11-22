package slim

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const rt27 = "com.apple.CoreSimulator.SimRuntime.iOS-27-2"

type fakeSim struct {
	mu       sync.Mutex
	calls    [][]string
	loaded   []string
	disabled []string
	failOn   map[string]bool // exact "disable user/501/<label>" strings
	listErr  error
}

func (f *fakeSim) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	if args[0] == "terminate" {
		return nil, nil
	}
	// spawn <udid> launchctl <verb> ...
	verb := args[3]
	switch verb {
	case "manageruid":
		return []byte("501\n"), nil
	case "list":
		if f.listErr != nil {
			return nil, f.listErr
		}
		var b strings.Builder
		b.WriteString("PID\tStatus\tLabel\n")
		for _, l := range f.loaded {
			b.WriteString("-\t0\t" + l + "\n")
		}
		return []byte(b.String()), nil
	case "print-disabled":
		var b strings.Builder
		b.WriteString("disabled services = {\n")
		for _, l := range f.disabled {
			b.WriteString("\t\t\"" + l + "\" => disabled\n")
		}
		b.WriteString("\t\t\"com.apple.other\" => enabled\n}\n")
		return []byte(b.String()), nil
	}
	if f.failOn[strings.Join(args[3:], " ")] {
		return nil, errors.New("boom")
	}
	return nil, nil
}

func newEngine(t *testing.T, f *fakeSim) *Engine {
	t.Helper()
	e, err := NewEngine(f.run, NewStore(filepath.Join(t.TempDir(), "iossim-slim.json")))
	if err != nil {
		t.Fatal(err)
	}
	e.Parallel = 1
	return e
}

func TestProfileLoadsAndNeverTouchesKeepList(t *testing.T) {
	p, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for rt, groups := range p.Runtimes {
		for g, def := range groups {
			if _, ok := p.Groups[g]; !ok {
				t.Errorf("runtime %s: group %q not declared", rt, g)
			}
			for _, l := range def.Labels {
				if IsKept(l) {
					t.Errorf("runtime %s group %s: profile label %q is on the keep list", rt, g, l)
				}
			}
		}
	}
	for name, pr := range p.Presets {
		for _, g := range pr.Groups {
			if _, ok := p.Groups[g]; !ok {
				t.Errorf("preset %s: unknown group %s", name, g)
			}
		}
	}
	min := p.Presets[PresetMinimalSafe].Groups
	agg := p.Presets[PresetAggressive].Groups
	if len(agg) <= len(min) {
		t.Errorf("aggressive (%d groups) must extend minimal-safe (%d)", len(agg), len(min))
	}
}

func TestKeepList(t *testing.T) {
	for _, l := range []string{"com.apple.SpringBoard", "com.apple.backboardd", "com.apple.testmanagerd", "com.apple.runningboardd",
		"com.apple.configd_sim", "com.apple.nsurlsessiond", "com.apple.TextInput.kbd", "com.apple.accessibility.axremoted",
		"com.apple.pasteboard.pasted", "com.apple.lsd", "com.apple.mobile.installd", "com.apple.containermanagerd",
		"com.apple.cfprefsd.xpc.daemon", "com.apple.notifyd", "com.apple.apsd", "com.apple.locationd", "com.apple.nanoregistryd"} {
		if !IsKept(l) {
			t.Errorf("%s must be kept", l)
		}
	}
	for _, l := range []string{"com.apple.assistantd", "com.apple.chronod", "com.apple.nanoregistrylaunchd"} {
		if IsKept(l) {
			t.Errorf("%s must not be kept", l)
		}
	}
}

func TestRuntimeKeyFallback(t *testing.T) {
	p := &Profile{Version: 1, Presets: map[string]Preset{PresetMinimalSafe: {Groups: []string{"g"}}}, Groups: map[string]string{"g": ""},
		Runtimes: map[string]map[string]GroupDef{"26": {"g": {Labels: []string{"a"}}}, "27": {"g": {Labels: []string{"b"}}}}}
	cases := map[string]string{
		"com.apple.CoreSimulator.SimRuntime.iOS-27-2": "27",
		"com.apple.CoreSimulator.SimRuntime.iOS-28-0": "27", // newer major: nearest lower
		"com.apple.CoreSimulator.SimRuntime.iOS-26-4": "26",
	}
	for id, want := range cases {
		plan, err := p.Resolve(id, "", nil)
		if err != nil || plan.RuntimeKey != want {
			t.Errorf("%s: key %q err %v, want %s", id, plan.RuntimeKey, err, want)
		}
	}
	if _, err := p.Resolve("com.apple.CoreSimulator.SimRuntime.iOS-25-0", "", nil); err == nil {
		t.Error("iOS 25 has no profile: want error")
	}
	if _, err := p.Resolve("com.apple.CoreSimulator.SimRuntime.watchOS-11-0", "", nil); err == nil {
		t.Error("watchOS: want error")
	}
}

func TestResolvePresetsAndCustom(t *testing.T) {
	p, _ := Load()
	min, err := p.Resolve(rt27, "", nil)
	if err != nil || min.Preset != PresetMinimalSafe || len(min.Labels) == 0 {
		t.Fatalf("%+v %v", min, err)
	}
	agg, _ := p.Resolve(rt27, PresetAggressive, nil)
	if len(agg.Labels) <= len(min.Labels) {
		t.Fatalf("aggressive %d <= minimal %d", len(agg.Labels), len(min.Labels))
	}
	c, err := p.Resolve(rt27, "", []string{"siri"})
	if err != nil || c.Preset != PresetCustom || !reflect.DeepEqual(c.Groups, []string{"siri"}) {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := p.Resolve(rt27, PresetCustom, nil); err == nil {
		t.Error("custom without groups must fail")
	}
	if _, err := p.Resolve(rt27, "", []string{"nope"}); err == nil {
		t.Error("unknown group must fail")
	}
	if _, err := p.Resolve(rt27, "bogus", nil); err == nil {
		t.Error("unknown preset must fail")
	}
}

func TestResolveDropsKeptLabels(t *testing.T) {
	p := &Profile{Version: 1, Presets: map[string]Preset{PresetMinimalSafe: {Groups: []string{"g"}}}, Groups: map[string]string{"g": ""},
		Runtimes: map[string]map[string]GroupDef{"27": {"g": {Labels: []string{"com.apple.SpringBoard", "com.apple.assistantd", "com.apple.assistantd"}}}}}
	plan, err := p.Resolve(rt27, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Labels, []string{"com.apple.assistantd"}) || !reflect.DeepEqual(plan.Protected, []string{"com.apple.SpringBoard"}) {
		t.Fatalf("%+v", plan)
	}
}

func TestApplyExactSequence(t *testing.T) {
	f := &fakeSim{loaded: []string{"com.apple.assistantd", "com.apple.siriinferenced"}, disabled: []string{"com.apple.sirittsd"}}
	e := newEngine(t, f)
	e.Profile = &Profile{Version: 7, Presets: map[string]Preset{PresetMinimalSafe: {Groups: []string{"siri"}}}, Groups: map[string]string{"siri": ""},
		Runtimes: map[string]map[string]GroupDef{"27": {"siri": {
			Labels: []string{"com.apple.assistantd", "com.apple.siriinferenced", "com.apple.sirittsd", "com.apple.vanished", "com.apple.SpringBoard"},
			Apps:   []string{"com.apple.Spotlight"}}}}}
	res, err := e.Apply(context.Background(), "UDID", rt27, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"spawn", "UDID", "launchctl", "manageruid"},
		{"spawn", "UDID", "launchctl", "list"},
		{"spawn", "UDID", "launchctl", "print-disabled", "user/501"},
		{"spawn", "UDID", "launchctl", "disable", "user/501/com.apple.assistantd"},
		{"spawn", "UDID", "launchctl", "bootout", "user/501/com.apple.assistantd"},
		{"spawn", "UDID", "launchctl", "disable", "user/501/com.apple.siriinferenced"},
		{"spawn", "UDID", "launchctl", "bootout", "user/501/com.apple.siriinferenced"},
		// sirittsd is already disabled and not loaded: nothing to run.
		{"terminate", "UDID", "com.apple.Spotlight"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls:\n got %v\nwant %v", f.calls, want)
	}
	if !reflect.DeepEqual(res.Applied, []string{"com.apple.assistantd", "com.apple.siriinferenced", "com.apple.sirittsd"}) ||
		!reflect.DeepEqual(res.Missing, []string{"com.apple.vanished"}) || len(res.Failed) != 0 ||
		!reflect.DeepEqual(res.Protected, []string{"com.apple.SpringBoard"}) {
		t.Fatalf("result: %+v", res)
	}
	rec, ok := e.Store.Get("UDID")
	if !ok || rec.ProfileVersion != 7 || len(rec.Disabled) != 3 {
		t.Fatalf("record: %+v %v", rec, ok)
	}
}

func TestApplyNeverRunsLaunchctlOnKeptLabel(t *testing.T) {
	f := &fakeSim{loaded: []string{"com.apple.SpringBoard", "com.apple.testmanagerd", "com.apple.assistantd"}}
	e := newEngine(t, f)
	e.Profile = &Profile{Version: 1, Presets: map[string]Preset{PresetAggressive: {Groups: []string{"g"}}}, Groups: map[string]string{"g": ""},
		Runtimes: map[string]map[string]GroupDef{"27": {"g": {Labels: []string{"com.apple.SpringBoard", "com.apple.testmanagerd", "com.apple.assistantd"}}}}}
	if _, err := e.Apply(context.Background(), "U", rt27, Options{Preset: PresetAggressive}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		j := strings.Join(c, " ")
		if strings.Contains(j, "SpringBoard") || strings.Contains(j, "testmanagerd") {
			t.Fatalf("kept label touched: %s", j)
		}
	}
}

func TestApplyFailedAndDryRun(t *testing.T) {
	f := &fakeSim{loaded: []string{"a.x", "b.y"}, failOn: map[string]bool{"disable user/501/a.x": true}}
	e := newEngine(t, f)
	e.Profile = &Profile{Version: 1, Presets: map[string]Preset{PresetMinimalSafe: {Groups: []string{"g"}}}, Groups: map[string]string{"g": ""},
		Runtimes: map[string]map[string]GroupDef{"27": {"g": {Labels: []string{"a.x", "b.y"}}}}}
	dry, err := e.Apply(context.Background(), "U", rt27, Options{DryRun: true})
	if err != nil || !dry.DryRun || len(dry.Applied) != 2 {
		t.Fatalf("dry: %+v %v", dry, err)
	}
	for _, c := range f.calls {
		if c[3] == "disable" || c[3] == "bootout" {
			t.Fatalf("dry run changed state: %v", c)
		}
	}
	if _, ok := e.Store.Get("U"); ok {
		t.Fatal("dry run wrote a record")
	}
	res, err := e.Apply(context.Background(), "U", rt27, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Failed, []string{"a.x"}) || !reflect.DeepEqual(res.Applied, []string{"b.y"}) || res.Errors["a.x"] == "" {
		t.Fatalf("%+v", res)
	}
	// a failed disable must not be followed by a bootout of that label
	for _, c := range f.calls {
		if c[3] == "bootout" && c[4] == "user/501/a.x" {
			t.Fatal("bootout after failed disable")
		}
	}
}

func TestRevertEnablesRecordedLabels(t *testing.T) {
	f := &fakeSim{loaded: []string{"a.x", "b.y"}}
	e := newEngine(t, f)
	e.Profile = &Profile{Version: 1, Presets: map[string]Preset{PresetMinimalSafe: {Groups: []string{"g"}}}, Groups: map[string]string{"g": ""},
		Runtimes: map[string]map[string]GroupDef{"27": {"g": {Labels: []string{"a.x", "b.y"}}}}}
	if _, err := e.Apply(context.Background(), "U", rt27, Options{}); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	en, failed, err := e.Revert(context.Background(), "U")
	if err != nil || len(failed) != 0 || !reflect.DeepEqual(en, []string{"a.x", "b.y"}) {
		t.Fatalf("%v %v %v", en, failed, err)
	}
	want := [][]string{
		{"spawn", "U", "launchctl", "manageruid"},
		{"spawn", "U", "launchctl", "enable", "user/501/a.x"},
		{"spawn", "U", "launchctl", "enable", "user/501/b.y"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %v", f.calls)
	}
	if _, ok := e.Store.Get("U"); ok {
		t.Fatal("record not removed")
	}
	if _, _, err := e.Revert(context.Background(), "U"); !errors.Is(err, ErrNotSlimmed) {
		t.Fatalf("want ErrNotSlimmed, got %v", err)
	}
}

func TestStorePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x", "iossim-slim.json")
	s := NewStore(path)
	if err := s.Put(Record{UDID: "U", Profile: "minimal-safe", Disabled: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	r, ok := NewStore(path).Get("U")
	if !ok || r.Profile != "minimal-safe" || len(r.Disabled) != 1 {
		t.Fatalf("%+v %v", r, ok)
	}
}

func TestParseHelpers(t *testing.T) {
	l := ParseList([]byte("PID\tStatus\tLabel\n-\t0\tcom.a\n12\t0\tcom.b\n"))
	if !l["com.a"] || !l["com.b"] || len(l) != 2 {
		t.Fatalf("%v", l)
	}
	d := ParseDisabled([]byte("disabled services = {\n\t\t\"com.x\" => disabled\n\t\t\"com.y\" => enabled\n}"))
	if !d["com.x"] || d["com.y"] {
		t.Fatalf("%v", d)
	}
}
