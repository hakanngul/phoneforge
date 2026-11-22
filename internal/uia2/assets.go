// Package uia2 bridges Pocketlab to the open-source Appium UiAutomator2
// server (Apache-2.0, github.com/appium/appium-uiautomator2-server) running
// on an Android device. The server APKs are never bundled: the user must
// trigger Assets.Download explicitly.
package uia2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	releasesAPI   = "https://api.github.com/repos/appium/appium-uiautomator2-server/releases/latest"
	serverGlob    = "appium-uiautomator2-server-v*.apk"
	testGlob      = "appium-uiautomator2-server-debug-androidTest.apk"
	downloadLimit = 200 << 20
)

// Assets locates and downloads the two UiAutomator2 server APKs.
type Assets struct {
	// Dir is the directory holding the APKs, e.g. ~/.pocketlab/uia2.
	Dir string

	apiURL string       // test override of the releases API URL
	client *http.Client // test override
}

func (a *Assets) httpClient() *http.Client {
	if a.client != nil {
		return a.client
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func newest(pattern string) string {
	matches, _ := filepath.Glob(pattern)
	best, bestT := "", time.Time{}
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil || fi.IsDir() {
			continue
		}
		if best == "" || fi.ModTime().After(bestT) {
			best, bestT = m, fi.ModTime()
		}
	}
	return best
}

// Status returns the paths of the server and test APKs and whether both exist.
func (a *Assets) Status() (serverAPK, testAPK string, ok bool) {
	if a == nil || a.Dir == "" {
		return "", "", false
	}
	serverAPK = newest(filepath.Join(a.Dir, serverGlob))
	testAPK = newest(filepath.Join(a.Dir, testGlob))
	return serverAPK, testAPK, serverAPK != "" && testAPK != ""
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Download fetches the latest release APKs from GitHub into Dir. It must only
// be called from an explicit user action.
func (a *Assets) Download(ctx context.Context) error {
	if a == nil || a.Dir == "" {
		return errors.New("uia2: assets dir not set")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	api := a.apiURL
	if api == "" {
		api = releasesAPI
	}
	body, err := a.get(ctx, api, 4<<20, "application/vnd.github+json")
	if err != nil {
		return fmt.Errorf("uia2: release lookup: %w", err)
	}
	var rel ghRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return fmt.Errorf("uia2: parse release: %w", err)
	}
	var serverName, serverURL, testName, testURL string
	for _, as := range rel.Assets {
		switch {
		case strings.HasSuffix(as.Name, "-debug-androidTest.apk"):
			testName, testURL = as.Name, as.URL
		case strings.HasPrefix(as.Name, "appium-uiautomator2-server-v") && strings.HasSuffix(as.Name, ".apk"):
			serverName, serverURL = as.Name, as.URL
		}
	}
	if serverURL == "" || testURL == "" {
		return fmt.Errorf("uia2: release %s lacks expected APK assets", rel.TagName)
	}
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		return err
	}
	// Normalise the test APK name so Status globbing is stable.
	_ = testName
	if err := a.fetchAPK(ctx, serverURL, filepath.Join(a.Dir, serverName)); err != nil {
		return err
	}
	if err := a.fetchAPK(ctx, testURL, filepath.Join(a.Dir, "appium-uiautomator2-server-debug-androidTest.apk")); err != nil {
		return err
	}
	// Drop older server APKs so Status is unambiguous.
	old, _ := filepath.Glob(filepath.Join(a.Dir, serverGlob))
	for _, o := range old {
		if filepath.Base(o) != serverName {
			_ = os.Remove(o)
		}
	}
	return nil
}

func (a *Assets) get(ctx context.Context, rawURL string, limit int64, accept string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" && a.apiURL == "" { // tests may use http via apiURL override
		return nil, fmt.Errorf("refusing non-https url %q", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", "pocketlab")
	resp, err := a.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", u.Host, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response from %s exceeds %d bytes", u.Host, limit)
	}
	return b, nil
}

func (a *Assets) fetchAPK(ctx context.Context, rawURL, dest string) error {
	b, err := a.get(ctx, rawURL, downloadLimit, "application/octet-stream")
	if err != nil {
		return fmt.Errorf("uia2: download %s: %w", filepath.Base(dest), err)
	}
	if !bytes.HasPrefix(b, []byte("PK\x03\x04")) {
		return fmt.Errorf("uia2: %s is not a zip/apk (bad magic)", filepath.Base(dest))
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".dl-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName)
		return fmt.Errorf("uia2: write %s: %v %v", filepath.Base(dest), werr, cerr)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
