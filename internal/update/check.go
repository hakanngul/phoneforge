package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Default feed locations.
const (
	DefaultManifestURL = "https://github.com/hakanngul/pocketlab/releases/latest/download/update-manifest.json"
	DefaultReleasesAPI = "https://api.github.com/repos/hakanngul/pocketlab/releases?per_page=20"
	manifestAssetName  = "update-manifest.json"
	sigAssetName       = "update-manifest.json.sig"
	maxReleasesBytes   = 4 << 20
	maxSigBytes        = 4 << 10
)

// Config configures a Checker.
type Config struct {
	// ManifestURL overrides the feed location (dev/QA). Empty: the channel
	// default (stable: releases/latest/download, beta: the releases API).
	ManifestURL string
	// ReleasesAPI overrides the GitHub releases listing used by the beta channel.
	ReleasesAPI    string
	Channel        string // "stable" (default) or "beta"
	PublicKeys     []ed25519.PublicKey
	HTTPClient     *http.Client
	CurrentVersion string
	GOOS, GOARCH   string // default runtime.GOOS/GOARCH
	// OSVersion returns the running OS version for min_os checks (default: sw_vers on darwin).
	OSVersion func() string
	Now       func() time.Time
}

// Checker fetches and evaluates the update feed.
type Checker struct{ cfg Config }

// NewChecker applies defaults to cfg.
func NewChecker(cfg Config) *Checker {
	if cfg.Channel == "" {
		cfg.Channel = "stable"
	}
	if cfg.PublicKeys == nil {
		cfg.PublicKeys = PublicKeys()
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = NewHTTPClient(30 * time.Second)
	}
	if cfg.GOOS == "" {
		cfg.GOOS = runtime.GOOS
	}
	if cfg.GOARCH == "" {
		cfg.GOARCH = runtime.GOARCH
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.OSVersion == nil {
		cfg.OSVersion = systemOSVersion
	}
	return &Checker{cfg: cfg}
}

// Config returns the effective configuration.
func (c *Checker) Config() Config { return c.cfg }

// NewHTTPClient returns a client that refuses https->http redirect downgrades.
// timeout 0 means no overall timeout (use for downloads).
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("refusing https to http redirect")
		}
		return nil
	}}
}

// Result is the outcome of one check.
type Result struct {
	Available   bool
	Manifest    *Manifest
	Asset       Asset
	NotModified bool
	Reason      string // why Available is false (informational)
}

// Backoff returns the wait before the next check after n consecutive failures:
// min(24h, 1h*2^(n-1)) with +-10% jitter.
func Backoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	d := 24 * time.Hour
	if failures <= 5 {
		d = time.Hour << (failures - 1)
		if d > 24*time.Hour {
			d = 24 * time.Hour
		}
	}
	return jitter(d)
}

func jitter(d time.Duration) time.Duration {
	f := 0.9 + rand.Float64()*0.2
	return time.Duration(float64(d) * f)
}

// Check consults the feed and updates st (ETag, cache, LastCheck, Failures,
// LastError). The caller persists st.
func (c *Checker) Check(ctx context.Context, st *State) (Result, error) {
	st.LastCheck = c.cfg.Now()
	res, err := c.check(ctx, st)
	if err != nil {
		st.Failures++
		st.LastError = err.Error()
		return Result{}, err
	}
	st.Failures = 0
	st.LastError = ""
	return res, nil
}

func (c *Checker) check(ctx context.Context, st *State) (Result, error) {
	var data, sig []byte
	notModified := false
	newETag := ""
	beta := c.cfg.Channel == "beta" && (c.cfg.ManifestURL == "" || c.cfg.ReleasesAPI != "")
	if beta {
		var err error
		data, sig, err = c.fetchBeta(ctx)
		if err != nil {
			return Result{}, err
		}
	} else {
		url := c.cfg.ManifestURL
		if url == "" {
			url = DefaultManifestURL
		}
		hdr := map[string]string{}
		if st.ETag != "" && len(st.ManifestData) > 0 && len(st.ManifestSig) > 0 {
			hdr["If-None-Match"] = st.ETag
		}
		body, status, h, err := c.get(ctx, url, hdr, MaxManifestBytes)
		if err != nil {
			return Result{}, err
		}
		if status == http.StatusNotModified {
			notModified = true
			data, sig = st.ManifestData, st.ManifestSig
		} else {
			sb, _, _, err := c.get(ctx, url+".sig", nil, maxSigBytes)
			if err != nil {
				return Result{}, err
			}
			data, sig = body, sb
			newETag = h.Get("ETag")
		}
	}
	m, err := VerifyManifest(data, sig, c.cfg.PublicKeys)
	if err != nil {
		return Result{}, err
	}
	if !notModified {
		st.ManifestData, st.ManifestSig = data, sig
		st.ETag = newETag
	}
	res := c.Evaluate(m)
	res.NotModified = notModified
	return res, nil
}

// Evaluate decides whether the (already verified) manifest is an applicable update.
func (c *Checker) Evaluate(m *Manifest) Result {
	res := Result{Manifest: m}
	if CompareVersions(m.Version, c.cfg.CurrentVersion) <= 0 {
		res.Reason = "not newer than the running version"
		return res
	}
	if c.cfg.Channel != "beta" && m.Channel == "beta" {
		res.Reason = "beta release on the stable channel"
		return res
	}
	a, ok := m.AssetFor(c.cfg.GOOS, c.cfg.GOARCH, KindAppZip)
	if !ok {
		res.Reason = "no app download for " + c.cfg.GOOS + "/" + c.cfg.GOARCH
		return res
	}
	if m.MinOS != "" && c.cfg.GOOS == "darwin" {
		if cur := c.cfg.OSVersion(); cur != "" && compareDotted(cur, m.MinOS) < 0 {
			res.Reason = "requires macOS " + m.MinOS
			return res
		}
	}
	res.Available, res.Asset = true, a
	return res
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Draft   bool   `json:"draft"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (c *Checker) fetchBeta(ctx context.Context) (data, sig []byte, err error) {
	api := c.cfg.ReleasesAPI
	if api == "" {
		api = DefaultReleasesAPI
	}
	body, _, _, err := c.get(ctx, api, map[string]string{"Accept": "application/vnd.github+json"}, maxReleasesBytes)
	if err != nil {
		return nil, nil, err
	}
	var rels []ghRelease
	if err := json.Unmarshal(body, &rels); err != nil {
		return nil, nil, fmt.Errorf("releases listing: %w", err)
	}
	var best *ghRelease
	var mURL, sURL string
	for i := range rels {
		r := &rels[i]
		if r.Draft || !ValidVersion(r.TagName) {
			continue
		}
		var mu, su string
		for _, a := range r.Assets {
			switch a.Name {
			case manifestAssetName:
				mu = a.URL
			case sigAssetName:
				su = a.URL
			}
		}
		if mu == "" || su == "" || !allowedURL(mu) || !allowedURL(su) {
			continue
		}
		if best == nil || CompareVersions(r.TagName, best.TagName) > 0 {
			best, mURL, sURL = r, mu, su
		}
	}
	if best == nil {
		return nil, nil, errors.New("no release with an update manifest found")
	}
	if data, _, _, err = c.get(ctx, mURL, nil, MaxManifestBytes); err != nil {
		return nil, nil, err
	}
	if sig, _, _, err = c.get(ctx, sURL, nil, maxSigBytes); err != nil {
		return nil, nil, err
	}
	return data, sig, nil
}

// get performs a bounded GET. 200 and 304 return nil error; other statuses fail.
func (c *Checker) get(ctx context.Context, url string, hdr map[string]string, limit int64) ([]byte, int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("User-Agent", "Pocketlab-Updater")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, resp.StatusCode, resp.Header, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, resp.Header, fmt.Errorf("feed request failed: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, 0, nil, err
	}
	if int64(len(b)) > limit {
		return nil, 0, nil, errors.New("feed response too large")
	}
	return b, resp.StatusCode, resp.Header, nil
}

// compareDotted compares dotted numeric versions ("14.2" vs "13").
func compareDotted(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(strings.TrimSpace(pa[i]))
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(strings.TrimSpace(pb[i]))
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func systemOSVersion() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
