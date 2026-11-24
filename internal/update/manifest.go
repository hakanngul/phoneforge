package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Errors returned by the update core.
var (
	ErrBadKey          = errors.New("update: invalid public key")
	ErrBadSignature    = errors.New("update: manifest signature does not verify")
	ErrBadManifest     = errors.New("update: invalid manifest")
	ErrUnsupported     = errors.New("update: not supported on this platform")
	ErrTranslocated    = errors.New("update: app is running from a translocated (read-only) location; move it to Applications first")
	ErrNotWritable     = errors.New("update: the app's folder is not writable")
	ErrSizeMismatch    = errors.New("update: downloaded size does not match the manifest")
	ErrChecksum        = errors.New("update: SHA-256 does not match the manifest")
	ErrNoAsset         = errors.New("update: no matching download for this platform")
	ErrBundleVerify    = errors.New("update: bundle verification failed")
	ErrDisabled        = errors.New("update: disabled")
	ErrNothingToUpdate = errors.New("update: no update available")
)

// Asset kinds.
const (
	KindAppZip = "app-zip"
	KindDMG    = "dmg"
	KindCLI    = "cli"
)

// Manifest is the signed update feed document.
type Manifest struct {
	Version   string  `json:"version"`
	Channel   string  `json:"channel"`
	Published string  `json:"published,omitempty"`
	MinOS     string  `json:"min_os,omitempty"`
	NotesMD   string  `json:"notes_md,omitempty"`
	NotesURL  string  `json:"notes_url,omitempty"`
	Assets    []Asset `json:"assets"`
}

// Asset is one downloadable file of a release.
type Asset struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// MaxManifestBytes bounds the manifest and signature downloads.
const MaxManifestBytes = 1 << 20

// ParseSignature decodes a .sig file (base64 of the raw 64-byte signature).
func ParseSignature(sig []byte) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(b) != ed25519.SignatureSize {
		return nil, ErrBadSignature
	}
	return b, nil
}

// VerifyManifest checks that sig is a valid ed25519 signature of the exact
// bytes in data under ANY of keys, then parses and validates the manifest.
func VerifyManifest(data, sig []byte, keys []ed25519.PublicKey) (*Manifest, error) {
	raw, err := ParseSignature(sig)
	if err != nil {
		return nil, err
	}
	ok := false
	for _, k := range keys {
		if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, data, raw) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, ErrBadSignature
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadManifest, err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate performs the basic structural checks.
func (m *Manifest) Validate() error {
	if !ValidVersion(m.Version) {
		return fmt.Errorf("%w: bad version %q", ErrBadManifest, m.Version)
	}
	if m.Channel != "" && m.Channel != "stable" && m.Channel != "beta" {
		return fmt.Errorf("%w: bad channel %q", ErrBadManifest, m.Channel)
	}
	for i, a := range m.Assets {
		if a.OS == "" || a.Arch == "" || a.Kind == "" || a.Name == "" {
			return fmt.Errorf("%w: asset %d is incomplete", ErrBadManifest, i)
		}
		if strings.ContainsAny(a.Name, `/\`) || a.Name == "." || a.Name == ".." {
			return fmt.Errorf("%w: asset %d has an unsafe name", ErrBadManifest, i)
		}
		if !allowedURL(a.URL) {
			return fmt.Errorf("%w: asset %d has a non-https URL", ErrBadManifest, i)
		}
		if a.Size <= 0 {
			return fmt.Errorf("%w: asset %d has no size", ErrBadManifest, i)
		}
		if b, err := hex.DecodeString(a.SHA256); err != nil || len(b) != 32 {
			return fmt.Errorf("%w: asset %d has a bad sha256", ErrBadManifest, i)
		}
	}
	return nil
}

// allowedURL accepts https URLs, and http only for loopback hosts (dev/tests).
func allowedURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		if h == "localhost" {
			return true
		}
		ip := net.ParseIP(h)
		return ip != nil && ip.IsLoopback()
	}
	return false
}

// AssetFor returns the asset of the given kind for goos/goarch.
func (m *Manifest) AssetFor(goos, goarch, kind string) (Asset, bool) {
	for _, a := range m.Assets {
		if a.OS == goos && a.Arch == goarch && a.Kind == kind {
			return a, true
		}
	}
	return Asset{}, false
}
