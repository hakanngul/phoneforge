package slim

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"howett.net/plist"
)

func writePlist(t *testing.T, path string, v any) {
	t.Helper()
	b, err := plist.Marshal(v, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasGroup(rs []KeepReason, g string) bool {
	for _, r := range rs {
		if r.Group == g {
			return true
		}
	}
	return false
}

func aggressiveGroups(t *testing.T) (*Profile, []string) {
	t.Helper()
	p, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return p, p.Presets[PresetAggressive].Groups
}

func TestScanAppInfoPlistOnly(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Demo.app")
	writePlist(t, filepath.Join(app, "Info.plist"), map[string]any{
		"CFBundleIdentifier":                  "com.example.demo",
		"CFBundleName":                        "Demo",
		"CFBundleExecutable":                  "Demo", // missing on disk
		"UIBackgroundModes":                   []string{"location", "fetch"},
		"NSHealthShareUsageDescription":       "health",
		"NSLocationWhenInUseUsageDescription": "loc",
		"NSCameraUsageDescription":            "cam",
		"NSUserActivityTypes":                 []string{"x"},
	})
	writePlist(t, filepath.Join(app, "PlugIns", "Widget.appex", "Info.plist"), map[string]any{
		"CFBundleIdentifier": "com.example.demo.widget",
		"NSExtension":        map[string]any{"NSExtensionPointIdentifier": "com.apple.widgetkit-extension"},
	})
	info, err := ScanApp(app)
	if err != nil {
		t.Fatal(err)
	}
	if info.BundleID != "com.example.demo" || info.Name != "Demo" {
		t.Fatalf("id/name: %+v", info)
	}
	if got := strings.Join(info.UsageDescriptions, ","); got != "NSCameraUsageDescription,NSHealthShareUsageDescription,NSLocationWhenInUseUsageDescription" {
		t.Fatalf("usage: %s", got)
	}
	if got := strings.Join(info.BackgroundModes, ","); got != "fetch,location" {
		t.Fatalf("bg modes: %s", got)
	}
	if w := info.Sources["extension-point com.apple.widgetkit-extension"]; len(w) != 1 || w[0] != "PlugIns/Widget.appex" {
		t.Fatalf("sources: %v", info.Sources)
	}
	m, err := LoadAppMap()
	if err != nil {
		t.Fatal(err)
	}
	p, aggr := aggressiveGroups(t)
	reasons, informational := m.Needed(info, p.Groups)
	for _, g := range []string{"healthkit", "location", "widgets"} {
		if !hasGroup(reasons, g) {
			t.Errorf("group %s not kept: %v", g, reasons)
		}
	}
	if hasGroup(reasons, "siri") {
		t.Errorf("siri should not be kept")
	}
	found := false
	for _, r := range informational {
		if r.Signal == "background-mode fetch" {
			found = true
		}
	}
	if !found {
		t.Errorf("fetch not informational: %v", informational)
	}
	left := AppGroups(aggr, reasons)
	for _, g := range left {
		if g == "healthkit" || g == "location" || g == "widgets" {
			t.Errorf("AppGroups still has %s", g)
		}
	}
	if len(left) != len(aggr)-3 {
		t.Errorf("left %v of %v", left, aggr)
	}
}

func TestScanAppRejectsNonApp(t *testing.T) {
	d := t.TempDir()
	if _, err := ScanApp(d); err == nil || !strings.Contains(err.Error(), ".app") {
		t.Fatalf("want .app error, got %v", err)
	}
	f := filepath.Join(d, "x.app")
	os.WriteFile(f, []byte("x"), 0o644)
	if _, err := ScanApp(f); err == nil {
		t.Fatal("file accepted")
	}
	if _, err := ScanApp(filepath.Join(d, "missing.app")); err == nil {
		t.Fatal("missing accepted")
	}
}

func TestScanAppNonMachOExecutable(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Demo.app")
	writePlist(t, filepath.Join(app, "Info.plist"), map[string]any{"CFBundleIdentifier": "a.b", "CFBundleExecutable": "Demo"})
	os.WriteFile(filepath.Join(app, "Demo"), []byte("#!/bin/sh\n"), 0o755)
	if _, err := ScanApp(app); err != nil {
		t.Fatal(err)
	}
}

func buildBlob(t *testing.T, xml []byte, with7172 bool) []byte {
	t.Helper()
	be := binary.BigEndian
	ent := make([]byte, 8)
	be.PutUint32(ent[0:], magicEntitlements)
	be.PutUint32(ent[4:], uint32(8+len(xml)))
	ent = append(ent, xml...)
	der := []byte{0xfa, 0xde, 0x71, 0x72, 0, 0, 0, 12, 1, 2, 3, 4}
	n := 1
	if with7172 {
		n = 2
	}
	hdr := make([]byte, 12+8*n)
	be.PutUint32(hdr[0:], magicEmbeddedSignature)
	be.PutUint32(hdr[8:], uint32(n))
	off := uint32(len(hdr))
	be.PutUint32(hdr[12:], slotEntitlements)
	be.PutUint32(hdr[16:], off)
	body := append([]byte{}, ent...)
	if with7172 {
		be.PutUint32(hdr[20:], 0x7)
		be.PutUint32(hdr[24:], off+uint32(len(ent)))
		body = append(body, der...)
	}
	out := append(hdr, body...)
	be.PutUint32(out[4:], uint32(len(out)))
	return out
}

func TestParseEntitlementsBlob(t *testing.T) {
	xml, err := plist.Marshal(map[string]any{
		"com.apple.developer.healthkit": true,
		"aps-environment":               "development",
	}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	for _, with := range []bool{false, true} {
		got := parseEntitlementsBlob(buildBlob(t, xml, with))
		if got["aps-environment"] != "development" || got["com.apple.developer.healthkit"] != true {
			t.Fatalf("with7172=%v got %v", with, got)
		}
	}
	// Garbage / truncated input must not panic.
	good := buildBlob(t, xml, true)
	for i := 0; i < len(good); i += 3 {
		parseEntitlementsBlob(good[:i])
	}
	if parseEntitlementsBlob([]byte("nope")) != nil || parseEntitlementsBlob(nil) != nil {
		t.Fatal("expected nil")
	}
	if parseEntitlementsBlob(bytes.Repeat([]byte{0xff}, 64)) != nil {
		t.Fatal("expected nil")
	}
}

func run(t *testing.T, dir string, name string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func TestScanAppRealMachO(t *testing.T) {
	for _, tool := range []string{"xcrun", "codesign"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	dir := t.TempDir()
	sdkOut, err := run(t, dir, "xcrun", "--sdk", "iphonesimulator", "--show-sdk-path")
	if err != nil {
		t.Skipf("no iphonesimulator SDK: %v", err)
	}
	sdk := strings.TrimSpace(string(sdkOut))
	app := filepath.Join(dir, "Real.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(app, "Real")
	src := filepath.Join(dir, "main.swift")
	os.WriteFile(src, []byte("import HealthKit\nimport CloudKit\nprint(HKHealthStore.isHealthDataAvailable(), CKContainer.self)\n"), 0o644)
	_, serr := run(t, dir, "xcrun", "-sdk", "iphonesimulator", "swiftc", "-target", "arm64-apple-ios17.0-simulator",
		"main.swift", "-framework", "HealthKit", "-framework", "CloudKit", "-o", bin)
	if serr != nil {
		csrc := filepath.Join(dir, "main.m")
		os.WriteFile(csrc, []byte("#import <HealthKit/HealthKit.h>\n#import <CloudKit/CloudKit.h>\nint main(void){return [HKHealthStore isHealthDataAvailable]+(int)[CKContainer class]!=0;}\n"), 0o644)
		if out, cerr := run(t, dir, "xcrun", "clang", "-isysroot", sdk, "-target", "arm64-apple-ios17.0-simulator",
			"main.m", "-framework", "HealthKit", "-framework", "CloudKit", "-framework", "Foundation", "-o", bin); cerr != nil {
			t.Skipf("cannot build iOS simulator binary: swiftc: %v; clang: %v %s", serr, cerr, out)
		}
	}
	writePlist(t, filepath.Join(app, "Info.plist"), map[string]any{
		"CFBundleIdentifier": "com.example.real", "CFBundleName": "Real", "CFBundleExecutable": "Real",
	})
	entPath := filepath.Join(dir, "ent.plist")
	writePlist(t, entPath, map[string]any{
		"com.apple.developer.healthkit":       true,
		"com.apple.developer.icloud-services": []string{"CloudKit"},
		"aps-environment":                     "development",
	})
	if out, err := run(t, dir, "codesign", "-s", "-", "--entitlements", entPath, "--force", bin); err != nil {
		t.Skipf("codesign failed: %v %s", err, out)
	}
	// Hide codesign so the blob parser (not the fallback) must supply entitlements.
	t.Setenv("PATH", t.TempDir())
	info, err := ScanApp(app)
	if err != nil {
		t.Fatal(err)
	}
	has := func(n string) bool {
		for _, f := range info.Frameworks {
			if f == n {
				return true
			}
		}
		return false
	}
	if !has("HealthKit") || !has("CloudKit") {
		t.Fatalf("frameworks: %v", info.Frameworks)
	}
	if info.Entitlements["aps-environment"] != "development" || info.Entitlements["com.apple.developer.healthkit"] != true {
		t.Fatalf("entitlements: %v", info.Entitlements)
	}
	m, _ := LoadAppMap()
	p, _ := aggressiveGroups(t)
	reasons, informational := m.Needed(info, p.Groups)
	if !hasGroup(reasons, "healthkit") || !hasGroup(reasons, "icloud") {
		t.Fatalf("reasons: %v", reasons)
	}
	ok := false
	for _, r := range informational {
		if r.Signal == "entitlement aps-environment" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("aps-environment not informational: %v", informational)
	}
}

func TestAppMapGroupsExist(t *testing.T) {
	m, err := LoadAppMap()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if m.Version < 1 || len(m.Entries) == 0 {
		t.Fatalf("empty map: %+v", m.Version)
	}
	kinds := map[string]bool{KindFramework: true, KindEntitlement: true, KindBackgroundMode: true, KindUsage: true, KindExtensionPoint: true}
	for _, e := range m.Entries {
		if !kinds[e.Kind] {
			t.Errorf("unknown kind %q (%s)", e.Kind, e.Match)
		}
		if e.Match == "" || e.Reason == "" {
			t.Errorf("incomplete entry %+v", e)
		}
		for _, g := range e.Groups {
			if _, ok := p.Groups[g]; !ok {
				t.Errorf("entry %s %s maps to unknown group %q", e.Kind, e.Match, g)
			}
		}
	}
}

func TestAppGroupsBareApp(t *testing.T) {
	m, _ := LoadAppMap()
	p, aggr := aggressiveGroups(t)
	reasons, informational := m.Needed(AppInfo{}, p.Groups)
	if len(reasons) != 0 || len(informational) != 0 {
		t.Fatalf("bare app has reasons: %v %v", reasons, informational)
	}
	got := AppGroups(aggr, reasons)
	if strings.Join(got, ",") != strings.Join(aggr, ",") {
		t.Fatalf("got %v want %v", got, aggr)
	}
}

func TestNeededHealthKit(t *testing.T) {
	m, _ := LoadAppMap()
	p, aggr := aggressiveGroups(t)
	info := AppInfo{
		Frameworks:   []string{"HealthKit", "UIKit"},
		Entitlements: map[string]any{"aps-environment": "development", "com.apple.developer.healthkit": true},
		Sources: map[string][]string{
			"framework HealthKit": {"app"},
		},
	}
	reasons, informational := m.Needed(info, p.Groups)
	if !hasGroup(reasons, "healthkit") {
		t.Fatalf("no healthkit: %v", reasons)
	}
	var hk KeepReason
	for _, r := range reasons {
		if r.Group == "healthkit" && r.Signal == "framework HealthKit" {
			hk = r
		}
	}
	if !strings.Contains(hk.Reason, "HealthKit") || len(hk.Where) != 1 || hk.Where[0] != "app" {
		t.Fatalf("reason: %+v", hk)
	}
	if !strings.Contains(hk.String(), "healthkit: framework HealthKit") {
		t.Fatalf("String: %s", hk.String())
	}
	if len(informational) != 1 || informational[0].Signal != "entitlement aps-environment" {
		t.Fatalf("informational: %v", informational)
	}
	left := AppGroups(aggr, reasons)
	for _, g := range left {
		if g == "healthkit" {
			t.Fatal("healthkit still slimmed")
		}
	}
	// Unknown groups are dropped when knownGroups is given.
	reasons, _ = m.Needed(info, map[string]string{"siri": ""})
	if len(reasons) != 0 {
		t.Fatalf("unknown group leaked: %v", reasons)
	}
}
