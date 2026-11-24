package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pocketlab/internal/update"
)

func setup(t *testing.T) (dir, seedB64, pubB64 string) {
	t.Helper()
	dir = t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	for name, body := range map[string]string{
		"Pocketlab-0.4.2-macos-arm64.zip":    "mac-zip",
		"Pocketlab-0.4.2-macos-arm64.dmg":    "mac-dmg",
		"Pocketlab-0.4.2-windows-amd64.zip":  "win",
		"pocketlab_0.4.2_linux_amd64.tar.gz": "cli",
		"SHA256SUMS.txt":                     "ignored",
		"WebDriverAgent-1.0.tar.gz":          "ignored",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, base64.StdEncoding.EncodeToString(priv.Seed()), base64.StdEncoding.EncodeToString(pub)
}

func TestMakeAndVerify(t *testing.T) {
	dir, seed, pub := setup(t)
	env := func(k string) string {
		if k == "UPDATE_ED25519_PRIVATE_KEY" {
			return seed
		}
		return ""
	}
	var out, errb bytes.Buffer
	args := []string{"-version", "v0.4.2", "-dir", dir, "-base-url", "https://example.com/dl/v0.4.2"}
	if code := run(args, &out, &errb, env); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if strings.Contains(out.String()+errb.String(), seed) {
		t.Fatal("key leaked in output")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "update-manifest.json"))
	sig, _ := os.ReadFile(filepath.Join(dir, "update-manifest.json.sig"))
	pk, _ := update.ParsePublicKey(pub)
	m, err := update.VerifyManifest(data, sig, []ed25519.PublicKey{pk})
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "0.4.2" || m.Channel != "stable" || len(m.Assets) != 4 {
		t.Fatalf("manifest: %+v", m)
	}
	a, ok := m.AssetFor("darwin", "arm64", update.KindAppZip)
	if !ok || a.URL != "https://example.com/dl/v0.4.2/Pocketlab-0.4.2-macos-arm64.zip" || a.Size != 7 {
		t.Fatalf("asset: %+v", a)
	}
	if _, ok := m.AssetFor("darwin", "arm64", update.KindDMG); !ok {
		t.Fatal("no dmg")
	}
	if _, ok := m.AssetFor("windows", "amd64", update.KindAppZip); !ok {
		t.Fatal("no windows zip")
	}
	if _, ok := m.AssetFor("linux", "amd64", update.KindCLI); !ok {
		t.Fatal("no cli")
	}

	// verify mode with the right key and artifact hashes
	out.Reset()
	if code := run([]string{"-verify", "-dir", dir, "-pub", pub}, &out, &errb, env); code != 0 {
		t.Fatalf("verify exit %d: %s", code, errb.String())
	}
	// embedded keys do not match the throwaway key
	if code := run([]string{"-verify", "-dir", dir}, &out, &errb, env); code == 0 {
		t.Fatal("verify with the embedded keys should fail for a throwaway key")
	}
	// tampered artifact
	_ = os.WriteFile(filepath.Join(dir, "pocketlab_0.4.2_linux_amd64.tar.gz"), []byte("evil"), 0o644)
	if code := run([]string{"-verify", "-dir", dir, "-pub", pub}, &out, &errb, env); code == 0 {
		t.Fatal("verify should catch a modified artifact")
	}
}

func TestBetaDefaultAndErrors(t *testing.T) {
	dir, seed, _ := setup(t)
	// rename artifacts to a prerelease version
	for _, n := range []string{"Pocketlab-0.4.2-macos-arm64.zip"} {
		_ = os.Rename(filepath.Join(dir, n), filepath.Join(dir, "Pocketlab-0.5.0-beta.1-macos-arm64.zip"))
	}
	var out, errb bytes.Buffer
	env := func(string) string { return seed }
	// mismatched names for the version -> error
	if code := run([]string{"-version", "0.5.0-beta.1", "-dir", dir, "-base-url", "https://x.test/"}, &out, &errb, env); code == 0 {
		t.Fatal("mismatched artifact versions should fail")
	}
	_ = os.Remove(filepath.Join(dir, "Pocketlab-0.4.2-macos-arm64.dmg"))
	_ = os.Remove(filepath.Join(dir, "Pocketlab-0.4.2-windows-amd64.zip"))
	_ = os.Remove(filepath.Join(dir, "pocketlab_0.4.2_linux_amd64.tar.gz"))
	errb.Reset()
	if code := run([]string{"-version", "0.5.0-beta.1", "-dir", dir, "-base-url", "https://x.test/"}, &out, &errb, env); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	data, _ := os.ReadFile(filepath.Join(dir, "update-manifest.json"))
	if !strings.Contains(string(data), `"channel": "beta"`) {
		t.Fatalf("expected beta channel:\n%s", data)
	}

	// key errors never echo key material
	errb.Reset()
	bad := "c2VjcmV0LWtleS1tYXRlcmlhbA==" // wrong length
	code := run([]string{"-version", "0.5.0-beta.1", "-dir", dir, "-base-url", "https://x.test/"}, &out, &errb, func(string) string { return bad })
	if code == 0 || strings.Contains(errb.String(), bad) {
		t.Fatalf("bad key: code %d, stderr %q", code, errb.String())
	}
	errb.Reset()
	if code := run([]string{"-version", "0.5.0-beta.1", "-dir", dir, "-base-url", "https://x.test/"}, &out, &errb, func(string) string { return "" }); code == 0 {
		t.Fatal("missing key should fail")
	}
	// 64-byte private key form is accepted
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	k, err := loadKey("", "K", func(string) string { return base64.StdEncoding.EncodeToString(priv) })
	if err != nil || !k.Equal(priv) {
		t.Fatalf("64-byte key: %v", err)
	}
}
