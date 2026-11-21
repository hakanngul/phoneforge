package registry

import (
	"context"
	"testing"

	"phoneforge/internal/device"
)

type fakeDev struct{ info device.Info }

func (f fakeDev) Info() device.Info { return f.info }
func (fakeDev) Screenshot(context.Context) (device.Screenshot, error) {
	return device.Screenshot{}, nil
}
func (fakeDev) UITree(context.Context) (device.Element, error)       { return device.Element{}, nil }
func (fakeDev) Tap(context.Context, int, int) error                  { return nil }
func (fakeDev) LongPress(context.Context, int, int, int) error       { return nil }
func (fakeDev) Swipe(context.Context, int, int, int, int, int) error { return nil }
func (fakeDev) TypeText(context.Context, string) error               { return nil }
func (fakeDev) PressKey(context.Context, device.Key) error           { return nil }
func (fakeDev) ListApps(context.Context) ([]device.AppInfo, error)   { return nil, nil }
func (fakeDev) LaunchApp(context.Context, string) error              { return nil }
func (fakeDev) StopApp(context.Context, string) error                { return nil }

type fakeDisc []device.Device

func (f fakeDisc) Discover(context.Context) ([]device.Device, error) { return f, nil }

func TestListStableOrder(t *testing.T) {
	mk := func(id, name string, p device.Platform) device.Device {
		return fakeDev{device.Info{ID: id, Name: name, Platform: p}}
	}
	want := []string{"android-b", "android-c", "ios-a", "ios-z", "remote-n-1"}
	// Try several input orders; output must always be identical.
	inputs := [][]device.Device{
		{mk("remote-n-1", "Aaa", device.PlatformAndroid), mk("ios-z", "Zed", device.PlatformIOS), mk("android-c", "Pixel", device.PlatformAndroid), mk("ios-a", "Ann", device.PlatformIOS), mk("android-b", "Galaxy", device.PlatformAndroid)},
		{mk("android-b", "Galaxy", device.PlatformAndroid), mk("ios-a", "Ann", device.PlatformIOS), mk("android-c", "Pixel", device.PlatformAndroid), mk("ios-z", "Zed", device.PlatformIOS), mk("remote-n-1", "Aaa", device.PlatformAndroid)},
	}
	for _, in := range inputs {
		r := New()
		r.AddDiscoverer(fakeDisc(in))
		if err := r.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 5; n++ { // map iteration is random; repeat
			got := r.List()
			for i, id := range want {
				if got[i].ID != id {
					t.Fatalf("pos %d: got %s want %s (%v)", i, got[i].ID, id, got)
				}
			}
		}
	}
}
