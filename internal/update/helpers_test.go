package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newKey(t testing.TB) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sign(priv ed25519.PrivateKey, data []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)) + "\n")
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type release struct {
	tag        string
	draft      bool
	prerelease bool
	data, sig  []byte
}

// feed is a fake release server: manifest + sig, releases API, archive downloads.
type feed struct {
	t       *testing.T
	pub     ed25519.PublicKey
	priv    ed25519.PrivateKey
	srv     *httptest.Server
	archive []byte

	mu       sync.Mutex
	data     []byte
	sig      []byte
	etag     string
	status   int // forced status for the manifest (0 = normal)
	releases []release
	ranges   []string
	abortAt  int  // abort the archive transfer after this many bytes (once)
	noRange  bool // ignore Range requests (always 200)
	extra    []byte

	reqs    atomic.Int32
	sigReqs atomic.Int32
	apiReqs atomic.Int32
}

func newFeed(t *testing.T, version string) *feed {
	t.Helper()
	pub, priv := newKey(t)
	f := &feed{t: t, pub: pub, priv: priv, etag: `"e1"`}
	f.archive = bytes.Repeat([]byte("pocketlab-archive-"), 20000) // ~360 KB
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	f.setManifest(f.manifest(version, "stable"))
	return f
}

func (f *feed) manifest(version, channel string) Manifest {
	name := "Pocketlab-" + version + "-macos-arm64.zip"
	return Manifest{
		Version: version, Channel: channel, Published: "2026-11-23T10:00:00Z", MinOS: "13.0",
		NotesMD: "notes for " + version, NotesURL: "https://example.com/notes/" + version,
		Assets: []Asset{
			{OS: "darwin", Arch: "arm64", Kind: KindAppZip, Name: name, URL: f.srv.URL + "/dl/" + name, Size: int64(len(f.archive)), SHA256: sha(f.archive)},
			{OS: "darwin", Arch: "arm64", Kind: KindDMG, Name: "Pocketlab-" + version + "-macos-arm64.dmg", URL: f.srv.URL + "/dl/x.dmg", Size: 10, SHA256: sha([]byte("d"))},
		},
	}
}

func (f *feed) signed(m Manifest) (data, sig []byte) {
	data, _ = json.MarshalIndent(m, "", "  ")
	return data, sign(f.priv, data)
}

func (f *feed) setManifest(m Manifest) {
	d, s := f.signed(m)
	f.mu.Lock()
	f.data, f.sig = d, s
	f.mu.Unlock()
}

func (f *feed) addRelease(tag string, draft, pre bool, m Manifest) {
	d, s := f.signed(m)
	f.mu.Lock()
	f.releases = append(f.releases, release{tag, draft, pre, d, s})
	f.mu.Unlock()
}

func (f *feed) url() string { return f.srv.URL + "/update-manifest.json" }

func (f *feed) asset() Asset {
	m := f.manifest("9.9.9", "stable")
	return m.Assets[0]
}

func (f *feed) serve(w http.ResponseWriter, r *http.Request) {
	f.reqs.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case p == "/update-manifest.json":
		if f.status != 0 {
			http.Error(w, "forced", f.status)
			return
		}
		if r.Header.Get("If-None-Match") == f.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", f.etag)
		_, _ = w.Write(f.data)
	case p == "/update-manifest.json.sig":
		f.sigReqs.Add(1)
		_, _ = w.Write(f.sig)
	case p == "/releases":
		f.apiReqs.Add(1)
		var out []map[string]any
		for _, rel := range f.releases {
			out = append(out, map[string]any{"tag_name": rel.tag, "draft": rel.draft, "prerelease": rel.prerelease,
				"assets": []map[string]string{
					{"name": "update-manifest.json", "browser_download_url": f.srv.URL + "/rel/" + rel.tag + "/update-manifest.json"},
					{"name": "update-manifest.json.sig", "browser_download_url": f.srv.URL + "/rel/" + rel.tag + "/update-manifest.json.sig"},
				}})
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(p, "/rel/"):
		parts := strings.Split(strings.TrimPrefix(p, "/rel/"), "/")
		for _, rel := range f.releases {
			if rel.tag == parts[0] {
				if parts[1] == "update-manifest.json" {
					_, _ = w.Write(rel.data)
				} else {
					_, _ = w.Write(rel.sig)
				}
				return
			}
		}
		http.NotFound(w, r)
	case strings.HasPrefix(p, "/dl/"):
		f.ranges = append(f.ranges, r.Header.Get("Range"))
		body := append(append([]byte{}, f.archive...), f.extra...)
		if f.abortAt > 0 {
			n := f.abortAt
			f.abortAt = 0
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body[:n])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		if f.noRange {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body)
			return
		}
		http.ServeContent(w, r, "a.zip", time.Time{}, bytes.NewReader(body))
	default:
		http.NotFound(w, r)
	}
}

func (f *feed) checker(current string) *Checker {
	return NewChecker(f.config(current))
}

func (f *feed) config(current string) Config {
	return Config{ManifestURL: f.url(), PublicKeys: []ed25519.PublicKey{f.pub}, CurrentVersion: current,
		GOOS: "darwin", GOARCH: "arm64", OSVersion: func() string { return "14.2" }}
}
