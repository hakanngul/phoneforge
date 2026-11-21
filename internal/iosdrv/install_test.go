package iosdrv

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielpaulus/go-ios/ios"
)

func TestInstallValidationAndSeams(t *testing.T) {
	dir := t.TempDir()
	ipa := filepath.Join(dir, "x.ipa")
	app := filepath.Join(dir, "X.app")
	txt := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(ipa, []byte("z"), 0o600)
	_ = os.WriteFile(txt, []byte("z"), 0o600)
	_ = os.Mkdir(app, 0o700)

	var got []string
	oi, ou := sendInstall, sendUninstall
	sendInstall = func(e ios.DeviceEntry, p string) error { got = append(got, "i:"+filepath.Base(p)); return nil }
	sendUninstall = func(e ios.DeviceEntry, b string) error { got = append(got, "u:"+b); return nil }
	defer func() { sendInstall, sendUninstall = oi, ou }()

	d := &iosDevice{}
	ctx := context.Background()
	if err := d.InstallApp(ctx, ipa); err != nil {
		t.Fatal(err)
	}
	if err := d.InstallApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err := d.InstallApp(ctx, txt); err == nil {
		t.Fatal("txt must be rejected")
	}
	if err := d.InstallApp(ctx, filepath.Join(dir, "missing.ipa")); err == nil {
		t.Fatal("missing must be rejected")
	}
	if err := d.UninstallApp(ctx, "com.example.app"); err != nil {
		t.Fatal(err)
	}
	if err := d.UninstallApp(ctx, "bad id; rm"); err == nil {
		t.Fatal("bad bundle id must be rejected")
	}
	want := []string{"i:x.ipa", "i:X.app", "u:com.example.app"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("calls %v", got)
	}
}
