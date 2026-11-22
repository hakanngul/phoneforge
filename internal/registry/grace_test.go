package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"phoneforge/internal/device"
)

type scriptDisc struct {
	devs []device.Device
	err  error
}

func (s *scriptDisc) Discover(context.Context) ([]device.Device, error) { return s.devs, s.err }

func idsOf(t *testing.T, d Discoverer) map[string]bool {
	t.Helper()
	devs, err := d.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, dv := range devs {
		m[dv.Info().ID] = true
	}
	return m
}

func TestWithGraceKeepsFlappingDevice(t *testing.T) {
	now := time.Unix(1000, 0)
	in := &scriptDisc{devs: []device.Device{fakeDev{info: device.Info{ID: "ios-A"}}, fakeDev{info: device.Info{ID: "ios-B"}}}}
	g := withGraceClock(in, 10*time.Second, func() time.Time { return now })

	if got := idsOf(t, g); !got["ios-A"] || !got["ios-B"] {
		t.Fatalf("initial %v", got)
	}
	// B drops out: still reported inside the grace window.
	in.devs = []device.Device{fakeDev{info: device.Info{ID: "ios-A"}}}
	now = now.Add(4 * time.Second)
	if got := idsOf(t, g); !got["ios-B"] {
		t.Fatal("B must be kept during grace")
	}
	// B flaps back: no gap, and the window restarts from the last sighting.
	in.devs = []device.Device{fakeDev{info: device.Info{ID: "ios-A"}}, fakeDev{info: device.Info{ID: "ios-B"}}}
	now = now.Add(4 * time.Second)
	idsOf(t, g)
	in.devs = []device.Device{fakeDev{info: device.Info{ID: "ios-A"}}}
	now = now.Add(9 * time.Second)
	if got := idsOf(t, g); !got["ios-B"] {
		t.Fatal("B must still be kept 9s after last sighting")
	}
	now = now.Add(2 * time.Second) // 11s absent
	if got := idsOf(t, g); got["ios-B"] || !got["ios-A"] {
		t.Fatalf("B must be dropped after grace, got %v", got)
	}
}

func TestWithGraceNewDeviceImmediateAndErrorsPass(t *testing.T) {
	now := time.Unix(1000, 0)
	in := &scriptDisc{}
	g := withGraceClock(in, 10*time.Second, func() time.Time { return now })
	in.devs = []device.Device{fakeDev{info: device.Info{ID: "ios-N"}}}
	if !idsOf(t, g)["ios-N"] {
		t.Fatal("new device must appear immediately")
	}
	in.err = errors.New("usbmuxd down")
	if _, err := g.Discover(context.Background()); err == nil {
		t.Fatal("scan error must pass through")
	}
}

func TestWithGraceZeroIsPassthrough(t *testing.T) {
	in := &scriptDisc{}
	if WithGrace(in, 0) != Discoverer(in) {
		t.Fatal("zero grace must return the inner discoverer")
	}
}

func TestWithGraceIfOnlyGracesSelected(t *testing.T) {
	usb := fakeDev{device.Info{ID: "ios-usb"}}
	wifi := fakeDev{device.Info{ID: "ios-wifi"}}
	cur := []device.Device{usb, wifi}
	inner := discFunc(func() []device.Device { return cur })
	g := WithGraceIf(inner, time.Minute, func(d device.Device) bool { return d.Info().ID == "ios-wifi" })
	if got, _ := g.Discover(context.Background()); len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
	cur = nil
	got, _ := g.Discover(context.Background())
	if len(got) != 1 || got[0].Info().ID != "ios-wifi" {
		t.Fatalf("USB must vanish immediately, Wi-Fi be graced: %v", got)
	}
}

type discFunc func() []device.Device

func (f discFunc) Discover(context.Context) ([]device.Device, error) { return f(), nil }
