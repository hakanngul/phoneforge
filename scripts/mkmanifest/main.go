// Command mkmanifest builds and signs the auto-update feed for a release:
//
//	go run ./scripts/mkmanifest -version 0.4.2 -dir artifacts -base-url https://github.com/OWNER/REPO/releases/download/v0.4.2/
//	go run ./scripts/mkmanifest -verify -dir artifacts          (verify manifest + signature + artifact hashes)
//
// The private key is one line of standard base64 (32-byte ed25519 seed, or the
// 64-byte private key), read from -key-file or from the environment variable
// named by -key-env (default UPDATE_ED25519_PRIVATE_KEY). It is never printed.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"pocketlab/internal/update"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

var (
	reMacApp = regexp.MustCompile(`^Pocketlab-(.+)-macos-(amd64|arm64)\.zip$`)
	reMacDMG = regexp.MustCompile(`^Pocketlab-(.+)-macos-(amd64|arm64)\.dmg$`)
	reWinApp = regexp.MustCompile(`^Pocketlab-(.+)-windows-(amd64|arm64)\.zip$`)
	reCLI    = regexp.MustCompile(`^pocketlab_(.+)_([a-z0-9]+)_([a-z0-9]+)\.(?:tar\.gz|zip)$`)
)

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("mkmanifest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		version   = fs.String("version", "", "release version (without a leading v)")
		channel   = fs.String("channel", "", "stable or beta (default: beta when the version has a '-')")
		dir       = fs.String("dir", "", "directory holding the release artifacts")
		baseURL   = fs.String("base-url", "", "download base URL, ends with the tag directory")
		notesURL  = fs.String("notes-url", "", "release notes page URL")
		notesFile = fs.String("notes-file", "", "markdown release notes file (optional)")
		minOS     = fs.String("min-os", "13.0", "minimum macOS version")
		out       = fs.String("out", "", "manifest path (default <dir>/update-manifest.json)")
		keyFile   = fs.String("key-file", "", "file holding the base64 private key")
		keyEnv    = fs.String("key-env", "UPDATE_ED25519_PRIVATE_KEY", "environment variable holding the base64 private key")
		verify    = fs.Bool("verify", false, "verify an existing manifest + .sig instead of creating one")
		pub       = fs.String("pub", "", "base64 public key to verify with (default: the embedded keys)")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	manifestPath := *out
	if manifestPath == "" && *dir != "" {
		manifestPath = filepath.Join(*dir, "update-manifest.json")
	}
	if manifestPath == "" {
		fmt.Fprintln(stderr, "mkmanifest: -dir or -out is required")
		return 2
	}
	if *verify {
		if err := verifyFiles(manifestPath, *dir, *pub, stdout); err != nil {
			fmt.Fprintln(stderr, "mkmanifest: verify failed:", err)
			return 1
		}
		return 0
	}

	if *version == "" || *dir == "" || *baseURL == "" {
		fmt.Fprintln(stderr, "mkmanifest: -version, -dir and -base-url are required")
		return 2
	}
	*version = strings.TrimPrefix(*version, "v")
	if !update.ValidVersion(*version) {
		fmt.Fprintf(stderr, "mkmanifest: %q is not a valid version\n", *version)
		return 2
	}
	ch := *channel
	if ch == "" {
		ch = "stable"
		if strings.Contains(*version, "-") {
			ch = "beta"
		}
	}
	if ch != "stable" && ch != "beta" {
		fmt.Fprintln(stderr, "mkmanifest: -channel must be stable or beta")
		return 2
	}
	priv, err := loadKey(*keyFile, *keyEnv, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "mkmanifest:", err)
		return 1
	}
	m := update.Manifest{Version: *version, Channel: ch, Published: time.Now().UTC().Format(time.RFC3339), MinOS: *minOS, NotesURL: *notesURL}
	if *notesFile != "" {
		b, err := os.ReadFile(*notesFile)
		if err != nil {
			fmt.Fprintln(stderr, "mkmanifest: reading notes:", err)
			return 1
		}
		m.NotesMD = string(b)
	}
	m.Assets, err = collectAssets(*dir, *version, *baseURL)
	if err != nil {
		fmt.Fprintln(stderr, "mkmanifest:", err)
		return 1
	}
	if len(m.Assets) == 0 {
		fmt.Fprintf(stderr, "mkmanifest: no release artifacts for %s found in %s\n", *version, *dir)
		return 1
	}
	if err := m.Validate(); err != nil {
		fmt.Fprintln(stderr, "mkmanifest:", err)
		return 1
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "mkmanifest:", err)
		return 1
	}
	data = append(data, '\n')
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)) + "\n"
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		fmt.Fprintln(stderr, "mkmanifest:", err)
		return 1
	}
	if err := os.WriteFile(manifestPath+".sig", []byte(sig), 0o644); err != nil {
		fmt.Fprintln(stderr, "mkmanifest:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (+ .sig): version %s, channel %s, %d assets, signed with key %s\n",
		manifestPath, m.Version, m.Channel, len(m.Assets), keyID(priv.Public().(ed25519.PublicKey)))
	return 0
}

// keyID is a short public fingerprint, safe to print.
func keyID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:4])
}

// loadKey reads the private key without ever echoing it in errors.
func loadKey(file, env string, getenv func(string) string) (ed25519.PrivateKey, error) {
	var raw string
	switch {
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, errors.New("cannot read the key file")
		}
		raw = string(b)
	default:
		raw = getenv(env)
		if strings.TrimSpace(raw) == "" {
			return nil, fmt.Errorf("no signing key: set %s or pass -key-file", env)
		}
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("the signing key is not valid base64")
	}
	switch len(b) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(b), nil
	}
	return nil, errors.New("the signing key has the wrong length (want a base64 32-byte seed)")
}

func collectAssets(dir, version, baseURL string) ([]update.Asset, error) {
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var assets []update.Asset
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		a, ok, err := classify(name, version)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		size, sum, err := hashFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		a.Name, a.Size, a.SHA256 = name, size, sum
		a.URL = baseURL + url.PathEscape(name)
		assets = append(assets, a)
	}
	sort.Slice(assets, func(i, j int) bool {
		x, y := assets[i], assets[j]
		if x.OS != y.OS {
			return x.OS < y.OS
		}
		if x.Arch != y.Arch {
			return x.Arch < y.Arch
		}
		if x.Kind != y.Kind {
			return x.Kind < y.Kind
		}
		return x.Name < y.Name
	})
	return assets, nil
}

func classify(name, version string) (update.Asset, bool, error) {
	check := func(v string) error {
		if v != version {
			return fmt.Errorf("%s: version in the file name (%s) does not match -version %s", name, v, version)
		}
		return nil
	}
	if m := reMacApp.FindStringSubmatch(name); m != nil {
		return update.Asset{OS: "darwin", Arch: m[2], Kind: update.KindAppZip}, true, check(m[1])
	}
	if m := reMacDMG.FindStringSubmatch(name); m != nil {
		return update.Asset{OS: "darwin", Arch: m[2], Kind: update.KindDMG}, true, check(m[1])
	}
	if m := reWinApp.FindStringSubmatch(name); m != nil {
		return update.Asset{OS: "windows", Arch: m[2], Kind: update.KindAppZip}, true, check(m[1])
	}
	if m := reCLI.FindStringSubmatch(name); m != nil {
		return update.Asset{OS: m[2], Arch: m[3], Kind: update.KindCLI}, true, check(m[1])
	}
	return update.Asset{}, false, nil
}

func hashFile(p string) (int64, string, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

func verifyFiles(manifestPath, dir, pub string, stdout io.Writer) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(manifestPath + ".sig")
	if err != nil {
		return err
	}
	keys := update.PublicKeys()
	if pub != "" {
		k, err := update.ParsePublicKey(strings.TrimSpace(pub))
		if err != nil {
			return err
		}
		keys = []ed25519.PublicKey{k}
	}
	m, err := update.VerifyManifest(data, sig, keys)
	if err != nil {
		return err
	}
	if dir != "" {
		for _, a := range m.Assets {
			size, sum, err := hashFile(filepath.Join(dir, a.Name))
			if err != nil {
				return fmt.Errorf("asset %s: %w", a.Name, err)
			}
			if size != a.Size || sum != a.SHA256 {
				return fmt.Errorf("asset %s does not match the manifest", a.Name)
			}
		}
	}
	fmt.Fprintf(stdout, "OK: %s version %s channel %s, %d assets\n", manifestPath, m.Version, m.Channel, len(m.Assets))
	return nil
}
