package iossim

import (
	"context"
	"testing"

	"phoneforge/internal/device"
	"phoneforge/internal/iosbridge"
)

const listJSON = `{"devices":{
 "com.apple.CoreSimulator.SimRuntime.iOS-27-2":[
  {"udid":"B","name":"iPhone Air","state":"Shutdown","isAvailable":true,"deviceTypeIdentifier":"com.apple.CoreSimulator.SimDeviceType.iPhone-Air"},
  {"udid":"A","name":"iPhone 17","state":"Booted","isAvailable":true,"deviceTypeIdentifier":"com.apple.CoreSimulator.SimDeviceType.iPhone-17"},
  {"udid":"X","name":"Broken","state":"Shutdown","isAvailable":false}],
 "com.apple.CoreSimulator.SimRuntime.watchOS-27-2":[
  {"udid":"W","name":"Watch","state":"Booted","isAvailable":true}]}}`

func TestParseDevices(t *testing.T) {
	sims, err := ParseDevices([]byte(listJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(sims) != 3 {
		t.Fatalf("got %d sims: %+v", len(sims), sims)
	}
	if sims[0].UDID != "A" || sims[1].UDID != "B" || sims[2].UDID != "W" {
		t.Fatalf("order: %+v", sims)
	}
	a := sims[0]
	if a.Runtime != "iOS 27.2" || a.Version != "27.2" || a.Platform != "iOS" || !a.Booted() || a.DeviceType != "iPhone-17" {
		t.Fatalf("bad sim: %+v", a)
	}
	if _, err := ParseDevices([]byte("nope")); err == nil {
		t.Fatal("want error")
	}
}

const appsOpenStep = `{
	"com.apple.mobilesafari" = {
		ApplicationType = System;
		CFBundleDisplayName = Safari;
		CFBundleName = MobileSafari;
	};
	"com.example.app" = {
		ApplicationType = User;
		CFBundleName = Example;
	};
}`

const appsXML = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>com.a.b</key><dict><key>CFBundleName</key><string>Bee</string><key>ApplicationType</key><string>User</string></dict></dict></plist>`

func TestParseApps(t *testing.T) {
	apps, err := ParseApps([]byte(appsOpenStep))
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 || apps[0].Name != "Example" || apps[1].BundleID != "com.apple.mobilesafari" || apps[1].Name != "Safari" || apps[1].Type != "System" {
		t.Fatalf("apps: %+v", apps)
	}
	apps, err = ParseApps([]byte(appsXML))
	if err != nil || len(apps) != 1 || apps[0].Name != "Bee" {
		t.Fatalf("xml: %+v %v", apps, err)
	}
}

func TestDiscoverBootedIOSOnly(t *testing.T) {
	c := New(t.TempDir())
	c.Run = func(ctx context.Context, args ...string) ([]byte, error) { return []byte(listJSON), nil }
	devs, err := NewDiscoverer(c).Discover(context.Background())
	if err != nil || len(devs) != 1 {
		t.Fatalf("devs=%v err=%v", devs, err)
	}
	i := devs[0].Info()
	if i.ID != "sim-A" || !i.Simulator || i.Platform != device.PlatformIOS || i.OSVersion != "27.2" {
		t.Fatalf("info: %+v", i)
	}
	if !c.IsSim("A") || c.IsSim("nope") {
		t.Fatal("IsSim")
	}
}

func TestPrepareOptsUniquePorts(t *testing.T) {
	c := New(t.TempDir())
	a := c.PrepareOpts("A", iosbridgeOpts())
	b := c.PrepareOpts("B", iosbridgeOpts())
	if a.WDAPort < 8200 || b.WDAPort == a.WDAPort || b.MJPEGPort == a.MJPEGPort || a.MJPEGPort < 9200 {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	if again := c.PrepareOpts("A", iosbridgeOpts()); again.WDAPort != a.WDAPort {
		t.Fatalf("not stable: %+v vs %+v", again, a)
	}
}

func iosbridgeOpts() iosbridge.Options { return iosbridge.Options{} }
