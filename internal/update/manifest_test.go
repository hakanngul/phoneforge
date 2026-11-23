package update

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedKeys(t *testing.T) {
	ks := PublicKeys()
	if len(ks) != 1 || len(ks[0]) != ed25519.PublicKeySize {
		t.Fatalf("want exactly the current key, got %d", len(ks))
	}
}

func TestVerifyManifest(t *testing.T) {
	f := newFeed(t, "0.4.2")
	m := f.manifest("0.4.2", "stable")
	data, sig := f.signed(m)

	got, err := VerifyManifest(data, sig, []ed25519.PublicKey{f.pub})
	if err != nil || got.Version != "0.4.2" {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	// any key in the list may verify (rotation)
	other, _ := newKey(t)
	if _, err := VerifyManifest(data, sig, []ed25519.PublicKey{other, f.pub}); err != nil {
		t.Fatalf("rotation: %v", err)
	}
	// wrong key
	if _, err := VerifyManifest(data, sig, []ed25519.PublicKey{other}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong key: %v", err)
	}
	// tampered bytes
	if _, err := VerifyManifest(append([]byte(" "), data...), sig, []ed25519.PublicKey{f.pub}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered: %v", err)
	}
	// garbage signature
	for _, s := range []string{"", "!!!", "AAAA"} {
		if _, err := VerifyManifest(data, []byte(s), []ed25519.PublicKey{f.pub}); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("sig %q: %v", s, err)
		}
	}
	// no keys
	if _, err := VerifyManifest(data, sig, nil); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("no keys: %v", err)
	}
}

func TestManifestValidation(t *testing.T) {
	f := newFeed(t, "0.4.2")
	base := f.manifest("0.4.2", "stable")
	mutate := func(fn func(m *Manifest)) error {
		m := base
		m.Assets = append([]Asset(nil), base.Assets...)
		fn(&m)
		data, sig := f.signed(m)
		_, err := VerifyManifest(data, sig, []ed25519.PublicKey{f.pub})
		return err
	}
	bad := map[string]func(m *Manifest){
		"version":     func(m *Manifest) { m.Version = "four" },
		"channel":     func(m *Manifest) { m.Channel = "nightly" },
		"http remote": func(m *Manifest) { m.Assets[0].URL = "http://example.com/a.zip" },
		"file url":    func(m *Manifest) { m.Assets[0].URL = "file:///etc/passwd" },
		"sha short":   func(m *Manifest) { m.Assets[0].SHA256 = "abcd" },
		"sha not hex": func(m *Manifest) { m.Assets[0].SHA256 = string(make([]byte, 64)) },
		"size zero":   func(m *Manifest) { m.Assets[0].Size = 0 },
		"unsafe name": func(m *Manifest) { m.Assets[0].Name = "../evil.zip" },
		"empty kind":  func(m *Manifest) { m.Assets[0].Kind = "" },
	}
	for name, fn := range bad {
		if err := mutate(fn); !errors.Is(err, ErrBadManifest) {
			t.Errorf("%s: want ErrBadManifest, got %v", name, err)
		}
	}
	// loopback http is allowed (dev), https anywhere is allowed
	if err := mutate(func(m *Manifest) { m.Assets[0].URL = "http://127.0.0.1:9/a.zip" }); err != nil {
		t.Errorf("loopback http: %v", err)
	}
	if err := mutate(func(m *Manifest) { m.Assets[0].URL = "http://localhost:9/a.zip" }); err != nil {
		t.Errorf("localhost http: %v", err)
	}
	if err := mutate(func(m *Manifest) { m.Assets[0].URL = "https://github.com/x/y.zip" }); err != nil {
		t.Errorf("https: %v", err)
	}
	// signed non-JSON
	data := []byte("not json")
	if _, err := VerifyManifest(data, sign(f.priv, data), []ed25519.PublicKey{f.pub}); !errors.Is(err, ErrBadManifest) {
		t.Errorf("non-json: %v", err)
	}
}

func TestAssetFor(t *testing.T) {
	f := newFeed(t, "0.4.2")
	m := f.manifest("0.4.2", "stable")
	if a, ok := m.AssetFor("darwin", "arm64", KindAppZip); !ok || a.Kind != KindAppZip {
		t.Fatal("app-zip not found")
	}
	if _, ok := m.AssetFor("darwin", "amd64", KindAppZip); ok {
		t.Fatal("amd64 should not match")
	}
	if _, ok := m.AssetFor("windows", "arm64", KindAppZip); ok {
		t.Fatal("windows should not match")
	}
}

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := LoadState(dir)
	if !st.Settings.AutoCheck || st.Settings.Channel != "stable" || st.Settings.AutoDownload {
		t.Fatalf("defaults: %+v", st.Settings)
	}
	st.SkipVersion, st.Failures, st.ETag = "1.2.3", 2, `"x"`
	st.Pending = &Pending{From: "1.0.0", To: "1.2.3", Launches: 1}
	if err := st.Save(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, StateFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file: %v %v", fi, err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("temp files left behind: %v", ents)
	}
	got := LoadState(dir)
	if got.SkipVersion != "1.2.3" || got.Failures != 2 || got.ETag != `"x"` || got.Pending == nil || got.Pending.Launches != 1 {
		t.Fatalf("round trip: %+v", got)
	}
	// corrupt and partial files fall back to defaults
	_ = os.WriteFile(filepath.Join(dir, StateFile), []byte("{nope"), 0o600)
	if s := LoadState(dir); !s.Settings.AutoCheck {
		t.Fatal("corrupt state should yield defaults")
	}
	b, _ := json.Marshal(map[string]any{"skipVersion": "9.9.9"})
	_ = os.WriteFile(filepath.Join(dir, StateFile), b, 0o600)
	if s := LoadState(dir); !s.Settings.AutoCheck || s.Settings.Channel != "stable" || s.SkipVersion != "9.9.9" {
		t.Fatalf("partial state: %+v", s)
	}
}
