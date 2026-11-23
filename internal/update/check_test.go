package update

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

func TestCheckValidUpdate(t *testing.T) {
	f := newFeed(t, "0.4.2")
	st := &State{Settings: DefaultSettings()}
	res, err := f.checker("0.4.0").Check(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Available || res.Manifest.Version != "0.4.2" || res.Asset.Kind != KindAppZip {
		t.Fatalf("result: %+v", res)
	}
	if st.ETag != `"e1"` || len(st.ManifestData) == 0 || len(st.ManifestSig) == 0 || st.Failures != 0 || st.LastCheck.IsZero() {
		t.Fatalf("state not updated: %+v", st)
	}
}

func TestCheckBadSignatureAndWrongKey(t *testing.T) {
	f := newFeed(t, "0.4.2")
	other := newFeed(t, "0.4.2") // different key pair
	f.mu.Lock()
	f.sig = other.sig
	f.mu.Unlock()
	st := &State{}
	if _, err := f.checker("0.4.0").Check(context.Background(), st); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("bad signature: %v", err)
	}
	if st.Failures != 1 || st.LastError == "" || st.ETag != "" || len(st.ManifestData) != 0 {
		t.Fatalf("a rejected manifest must not be cached: %+v", st)
	}

	g := newFeed(t, "0.4.2")
	cfg := g.config("0.4.0")
	cfg.PublicKeys = []ed25519.PublicKey{other.pub}
	if _, err := NewChecker(cfg).Check(context.Background(), &State{}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestCheckNotAvailable(t *testing.T) {
	cases := []struct {
		name    string
		current string
		mutate  func(*Manifest)
	}{
		{"same version", "0.4.2", nil},
		{"downgrade", "0.5.0", nil},
		{"prerelease of current", "0.4.2", func(m *Manifest) { m.Version = "0.4.2-rc.1" }},
		{"beta on stable channel", "0.4.0", func(m *Manifest) { m.Channel = "beta" }},
		{"no asset for arch", "0.4.0", func(m *Manifest) { m.Assets[0].Arch = "amd64" }},
		{"min_os too new", "0.4.0", func(m *Manifest) { m.MinOS = "15.0" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFeed(t, "0.4.2")
			m := f.manifest("0.4.2", "stable")
			if c.mutate != nil {
				c.mutate(&m)
			}
			f.setManifest(m)
			res, err := f.checker(c.current).Check(context.Background(), &State{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Available || res.Reason == "" {
				t.Fatalf("should not be available: %+v", res)
			}
		})
	}
	// control: min_os satisfied
	f := newFeed(t, "0.4.2")
	m := f.manifest("0.4.2", "stable")
	m.MinOS = "14.2"
	f.setManifest(m)
	if res, _ := f.checker("0.4.0").Check(context.Background(), &State{}); !res.Available {
		t.Fatal("min_os equal to the running OS should be available")
	}
}

func TestCheckFailuresAndBackoff(t *testing.T) {
	f := newFeed(t, "0.4.2")
	st := &State{}
	c := f.checker("0.4.0")
	for i, code := range []int{404, 503} {
		f.mu.Lock()
		f.status = code
		f.mu.Unlock()
		if _, err := c.Check(context.Background(), st); err == nil {
			t.Fatalf("status %d should fail", code)
		}
		if st.Failures != i+1 || st.LastError == "" {
			t.Fatalf("after %d: failures=%d err=%q", code, st.Failures, st.LastError)
		}
	}
	f.mu.Lock()
	f.status = 0
	f.mu.Unlock()
	if _, err := c.Check(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if st.Failures != 0 || st.LastError != "" {
		t.Fatalf("success must reset failures: %+v", st)
	}
	// network failure
	f.srv.Close()
	if _, err := c.Check(context.Background(), st); err == nil || st.Failures != 1 {
		t.Fatalf("network error: %v failures=%d", err, st.Failures)
	}

	within := func(n int, base time.Duration) {
		for i := 0; i < 50; i++ {
			d := Backoff(n)
			if d < base*89/100 || d > base*111/100 {
				t.Fatalf("Backoff(%d)=%v not within 10%% of %v", n, d, base)
			}
		}
	}
	within(1, time.Hour)
	within(2, 2*time.Hour)
	within(3, 4*time.Hour)
	within(5, 16*time.Hour)
	within(6, 24*time.Hour)
	within(50, 24*time.Hour)
}

func TestCheckETagReusesCache(t *testing.T) {
	f := newFeed(t, "0.4.2")
	c := f.checker("0.4.0")
	st := &State{}
	if _, err := c.Check(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	sigReqs := f.sigReqs.Load()
	res, err := c.Check(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if !res.NotModified || !res.Available || res.Manifest.Version != "0.4.2" {
		t.Fatalf("304 path: %+v", res)
	}
	if f.sigReqs.Load() != sigReqs {
		t.Fatal("a 304 must not refetch the signature")
	}
	// the cache is re-verified: a poisoned cache is rejected
	st.ManifestData = append([]byte(" "), st.ManifestData...)
	if _, err := c.Check(context.Background(), st); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("poisoned cache: %v", err)
	}
	// new content (new etag) is fetched
	f.mu.Lock()
	f.etag = `"e2"`
	f.mu.Unlock()
	f.setManifest(f.manifest("0.4.3", "stable"))
	st2 := &State{}
	_, _ = c.Check(context.Background(), st2)
	st2.ETag = `"e1"` // stale etag with cache
	res, err = c.Check(context.Background(), st2)
	if err != nil || res.NotModified || res.Manifest.Version != "0.4.3" {
		t.Fatalf("changed feed: %+v %v", res, err)
	}
}

func TestCheckBetaChannel(t *testing.T) {
	f := newFeed(t, "0.4.1")
	f.addRelease("v0.4.1", false, false, f.manifest("0.4.1", "stable"))
	f.addRelease("v0.5.0-beta.1", false, true, f.manifest("0.5.0-beta.1", "beta"))
	f.addRelease("v0.5.0-beta.2", false, true, f.manifest("0.5.0-beta.2", "beta"))
	f.addRelease("v0.9.0", true, false, f.manifest("0.9.0", "stable")) // draft: ignored

	cfg := f.config("0.4.0")
	cfg.ManifestURL = ""
	cfg.ReleasesAPI = f.srv.URL + "/releases"
	cfg.Channel = "beta"
	res, err := NewChecker(cfg).Check(context.Background(), &State{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Available || res.Manifest.Version != "0.5.0-beta.2" {
		t.Fatalf("beta should pick the newest prerelease: %+v", res)
	}

	// stable never consults the releases API and ignores the beta
	stable := f.config("0.4.0")
	f.apiReqs.Store(0)
	res, err = NewChecker(stable).Check(context.Background(), &State{})
	if err != nil || !res.Available || res.Manifest.Version != "0.4.1" || f.apiReqs.Load() != 0 {
		t.Fatalf("stable: %+v %v api=%d", res, err, f.apiReqs.Load())
	}
	// stable channel rejects a beta manifest even if it is served as the feed
	f.setManifest(f.manifest("0.6.0-beta.1", "beta"))
	res, _ = NewChecker(stable).Check(context.Background(), &State{})
	if res.Available {
		t.Fatal("stable must not offer a beta manifest")
	}
}

func TestCheckResponseTooLarge(t *testing.T) {
	f := newFeed(t, "0.4.2")
	f.mu.Lock()
	f.data = make([]byte, MaxManifestBytes+10)
	f.mu.Unlock()
	if _, err := f.checker("0.4.0").Check(context.Background(), &State{}); err == nil {
		t.Fatal("oversized manifest must be rejected")
	}
}

func TestCheckRefetchesOnceWhenSignatureMismatches(t *testing.T) {
	// a release published between the manifest and .sig requests: first pair mismatches
	f := newFeed(t, "0.4.2")
	f.mu.Lock()
	f.badSigs = 1
	f.mu.Unlock()
	st := &State{}
	res, err := f.checker("0.4.0").Check(context.Background(), st)
	if err != nil || !res.Available {
		t.Fatalf("one stale signature must be retried: %+v %v", res, err)
	}
	if f.sigReqs.Load() != 2 {
		t.Fatalf("sig requests: %d", f.sigReqs.Load())
	}
	// a persistent mismatch still fails (only one retry)
	g := newFeed(t, "0.4.2")
	g.mu.Lock()
	g.badSigs = 5
	g.mu.Unlock()
	if _, err := g.checker("0.4.0").Check(context.Background(), &State{}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("%v", err)
	}
	if g.sigReqs.Load() != 2 {
		t.Fatalf("sig requests: %d", g.sigReqs.Load())
	}
}
