package androiddrv

import (
	"testing"

	"phoneforge/internal/device"
)

func TestParseBounds(t *testing.T) {
	tests := []struct {
		in   string
		want device.Rect
		ok   bool
	}{
		{"[0,0][1080,2400]", device.Rect{X: 0, Y: 0, W: 1080, H: 2400}, true},
		{"[10,20][110,70]", device.Rect{X: 10, Y: 20, W: 100, H: 50}, true},
		{"", device.Rect{}, false},
		{"[1,2]", device.Rect{}, false},
		{"garbage", device.Rect{}, false},
	}
	for _, tc := range tests {
		got, ok := parseBounds(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseBounds(%q) = %v,%v want %v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

const sampleXML = `UI hierchary noise
<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<hierarchy rotation="0">
  <node index="0" text="" resource-id="" class="android.widget.FrameLayout" content-desc="" clickable="false" enabled="true" focused="false" bounds="[0,0][1080,2400]">
    <node index="0" text="Sign in" resource-id="com.x:id/btn" class="android.widget.Button" content-desc="login" clickable="true" enabled="true" focused="true" bounds="[100,200][300,260]" />
  </node>
</hierarchy>
UI hierchary dumped to: /dev/tty`

func TestParseUIXML(t *testing.T) {
	root, err := parseUIXML([]byte(sampleXML))
	if err != nil {
		t.Fatal(err)
	}
	if root.Role != "hierarchy" || len(root.Children) != 1 {
		t.Fatalf("bad root: %+v", root)
	}
	btn := root.Children[0].Children[0]
	if btn.Text != "Sign in" || btn.Label != "login" || btn.ResourceID != "com.x:id/btn" {
		t.Errorf("bad button: %+v", btn)
	}
	if btn.Bounds != (device.Rect{X: 100, Y: 200, W: 200, H: 60}) {
		t.Errorf("bad bounds: %+v", btn.Bounds)
	}
	if !btn.Clickable || !btn.Enabled || !btn.Focused {
		t.Errorf("bad flags: %+v", btn)
	}
}

func TestEscapeText(t *testing.T) {
	got, err := escapeText("a b&c")
	if err != nil || got != `a%sb\&c` {
		t.Errorf("got %q, %v", got, err)
	}
	if _, err := escapeText("é"); err == nil {
		t.Error("expected error for non-ASCII")
	}
}

func TestParseDevices(t *testing.T) {
	out := "List of devices attached\nemulator-5554\tdevice product:sdk model:sdk\nABC123\tunauthorized usb:1\n\n"
	es := parseDevices(out)
	if len(es) != 2 || es[0].serial != "emulator-5554" || es[1].state != "unauthorized" {
		t.Errorf("bad: %+v", es)
	}
	if sanitizeSerial("1.2.3.4:5555") != "1.2.3.4-5555" {
		t.Error("sanitize")
	}
}
