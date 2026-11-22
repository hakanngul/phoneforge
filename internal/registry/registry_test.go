package registry

import (
	"context"
	"testing"
	"time"

	"pocketlab/internal/device"
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

type flakyDisc struct {
	devs  []device.Device
	err   error
	block chan struct{}
}

func (f *flakyDisc) Discover(ctx context.Context) ([]device.Device, error) {
	if f.block != nil {
		<-f.block
	}
	return f.devs, f.err
}

func TestRefreshIsolatesFailingDiscoverer(t *testing.T) {
	good := &flakyDisc{devs: []device.Device{fakeDev{device.Info{ID: "android-1", Name: "A"}}}}
	ios := &flakyDisc{devs: []device.Device{fakeDev{device.Info{ID: "ios-1", Name: "I", Platform: device.PlatformIOS}}}}
	r := New()
	r.AddDiscoverer(good)
	r.AddDiscoverer(ios)
	if err := r.Refresh(context.Background()); err != nil || len(r.List()) != 2 {
		t.Fatalf("initial: %v %v", err, r.List())
	}
	// iOS backend fails: keep its last devices, Android updates still apply.
	ios.err = context.DeadlineExceeded
	good.devs = nil
	if err := r.Refresh(context.Background()); err == nil {
		t.Fatal("want error reported")
	}
	got := r.List()
	if len(got) != 1 || got[0].ID != "ios-1" {
		t.Fatalf("got %v", got)
	}
	// Recovery with an empty list removes the device.
	ios.err, ios.devs = nil, nil
	_ = r.Refresh(context.Background())
	if len(r.List()) != 0 {
		t.Fatalf("got %v", r.List())
	}
}

func TestRefreshHungDiscovererTimesOut(t *testing.T) {
	old := DiscoverTimeoutForTest(100 * time.Millisecond)
	defer DiscoverTimeoutForTest(old)
	hung := &flakyDisc{block: make(chan struct{})}
	defer close(hung.block)
	good := &flakyDisc{devs: []device.Device{fakeDev{device.Info{ID: "android-1"}}}}
	r := New()
	r.AddDiscoverer(hung)
	r.AddDiscoverer(good)
	start := time.Now()
	err := r.Refresh(context.Background())
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v took %v", err, time.Since(start))
	}
	if l := r.List(); len(l) != 1 || l[0].ID != "android-1" {
		t.Fatalf("got %v", l)
	}
}
