package androiddrv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

const compressedXML = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<hierarchy rotation="0"><node index="0" text="" resource-id="" class="android.widget.FrameLayout" content-desc="" clickable="false" enabled="true" focused="false" bounds="[0,0][1080,2400]"><node index="0" text="OK" resource-id="" class="android.widget.Button" content-desc="" clickable="true" enabled="true" focused="false" bounds="[0,0][10,10]" /></node></hierarchy>
UI hierchary dumped to: /dev/stdout`

func TestParseUIXMLStdoutNoise(t *testing.T) {
	root, err := parseUIXML([]byte(compressedXML))
	if err != nil || len(root.Children) != 1 || root.Children[0].Children[0].Text != "OK" {
		t.Fatalf("got %+v, %v", root, err)
	}
}

// fakeADB writes a shell script standing in for adb. stdoutOK controls whether
// `exec-out uiautomator dump ... /dev/stdout` prints a hierarchy; every call is
// appended to the returned log file.
func fakeADB(t *testing.T, stdoutOK bool) (adb, log string) {
	t.Helper()
	dir := t.TempDir()
	xml := filepath.Join(dir, "dump.xml")
	if err := os.WriteFile(xml, []byte(sampleXML), 0o644); err != nil {
		t.Fatal(err)
	}
	log = filepath.Join(dir, "calls.log")
	stdout := `printf '%s' ""`
	if stdoutOK {
		stdout = "cat " + xml
	}
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\ncase \"$*\" in\n" +
		"*exec-out*uiautomator*/dev/stdout*) " + stdout + " ;;\n" +
		"*exec-out*cat*) cat " + xml + " ;;\n" +
		"esac\nexit 0\n"
	adb = filepath.Join(dir, "adb")
	if err := os.WriteFile(adb, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return adb, log
}

func TestUITreeStdoutFastPath(t *testing.T) {
	adb, log := fakeADB(t, true)
	d := &androidDevice{adbPath: adb, serial: "FAST1"}
	root, err := d.UITree(context.Background())
	if err != nil || len(root.Children) != 1 {
		t.Fatalf("got %+v, %v", root, err)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "pf_dump") || !strings.Contains(string(calls), "--compressed") {
		t.Fatalf("expected a single compressed stdout dump, got:\n%s", calls)
	}
}

func TestUITreeFallsBackToFile(t *testing.T) {
	adb, log := fakeADB(t, false)
	d := &androidDevice{adbPath: adb, serial: "SLOW1"}
	t.Cleanup(func() { noStdoutDump.Delete("SLOW1") })
	for i := 0; i < 2; i++ {
		if root, err := d.UITree(context.Background()); err != nil || len(root.Children) != 1 {
			t.Fatalf("call %d: got %+v, %v", i, root, err)
		}
	}
	calls, _ := os.ReadFile(log)
	if n := strings.Count(string(calls), "/dev/stdout"); n != 1 {
		t.Fatalf("stdout dump should be tried once then remembered, tried %d times:\n%s", n, calls)
	}
	if n := strings.Count(string(calls), "pf_dump"); n < 4 {
		t.Fatalf("expected file dump + cat on both calls:\n%s", calls)
	}
}
