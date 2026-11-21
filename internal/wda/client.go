// Package wda is an HTTP client for WebDriverAgent (the appium/WebDriverAgent
// protocol) running on an iOS device or simulator. All coordinates are logical
// points, matching what WDA reports.
package wda

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/png" // register PNG decoder for DecodeConfig
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"phoneforge/internal/device"
)

// Client talks to one WebDriverAgent instance.
type Client struct {
	bmu  sync.RWMutex
	base string
	http *http.Client

	mu    sync.Mutex
	sid   string
	w, h  int     // logical window size in points
	scale float64 // pixels per point of the last screenshot (default 1)

	// ExcludedAttributes are passed to /source to speed up large trees.
	ExcludedAttributes []string

	// SessionSettings are appium settings applied (best effort) every time a
	// new WDA session is created, e.g. MJPEG framerate/scaling/quality.
	SessionSettings map[string]any
}

// New returns a client for the WDA server at baseURL (e.g. http://127.0.0.1:8100).
func New(baseURL string) *Client {
	return &Client{
		base:               strings.TrimRight(baseURL, "/"),
		http:               &http.Client{Timeout: 60 * time.Second},
		scale:              1,
		ExcludedAttributes: []string{"visible"},
	}
}

// BaseURL returns the server base URL.
func (c *Client) BaseURL() string {
	c.bmu.RLock()
	defer c.bmu.RUnlock()
	return c.base
}

// SetBaseURL points the client at a new server URL (e.g. after a runner
// restart on a different forwarded port). A changed URL drops the cached
// session; an unchanged one is a no-op.
func (c *Client) SetBaseURL(u string) {
	u = strings.TrimRight(u, "/")
	c.bmu.Lock()
	changed := c.base != u
	c.base = u
	c.bmu.Unlock()
	if changed {
		c.ResetSession()
	}
}

// Scale returns the pixel-per-point ratio observed on the last screenshot.
func (c *Client) Scale() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scale
}

// StatusInfo is the parsed GET /status response.
type StatusInfo struct {
	Ready     bool
	SessionID string
	OS        string
	Raw       json.RawMessage
}

// Status calls GET /status. It returns an error when WDA is unreachable or
// reports not ready.
func (c *Client) Status(ctx context.Context) (StatusInfo, error) {
	data, err := c.doRaw(ctx, http.MethodGet, c.BaseURL()+"/status", nil)
	if err != nil {
		return StatusInfo{}, err
	}
	var env struct {
		SessionID string          `json:"sessionId"`
		Value     json.RawMessage `json:"value"`
	}
	_ = json.Unmarshal(data, &env)
	var v struct {
		Ready     *bool  `json:"ready"`
		State     string `json:"state"`
		SessionID string `json:"sessionId"`
		OS        struct {
			Version string `json:"version"`
		} `json:"os"`
	}
	_ = json.Unmarshal(env.Value, &v)
	info := StatusInfo{Ready: true, SessionID: env.SessionID, OS: v.OS.Version, Raw: env.Value}
	if v.SessionID != "" {
		info.SessionID = v.SessionID
	}
	if v.Ready != nil {
		info.Ready = *v.Ready
	}
	if !info.Ready {
		return info, errors.New("wda: server not ready")
	}
	return info, nil
}

// wdaError is an error response from WDA.
type wdaError struct {
	Status int
	Code   string
	Msg    string
}

func (e *wdaError) Error() string {
	return fmt.Sprintf("wda: HTTP %d: %s: %s", e.Status, e.Code, e.Msg)
}

func (e *wdaError) invalidSession() bool {
	s := strings.ToLower(e.Code + " " + e.Msg)
	return strings.Contains(s, "invalid session") || strings.Contains(s, "session does not exist") ||
		strings.Contains(s, "no such session")
}

// doRaw performs a request and returns the full response body.
func (c *Client) doRaw(ctx context.Context, method, fullURL string, in any) ([]byte, error) {
	var rd io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, rd)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wda: %s %s: %w", method, fullURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if err != nil {
		return nil, fmt.Errorf("wda: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		we := &wdaError{Status: resp.StatusCode}
		var env struct {
			Value struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			} `json:"value"`
		}
		if json.Unmarshal(data, &env) == nil {
			we.Code, we.Msg = env.Value.Error, env.Value.Message
		}
		if we.Msg == "" {
			we.Msg = strings.TrimSpace(string(data))
			if len(we.Msg) > 300 {
				we.Msg = we.Msg[:300]
			}
		}
		return nil, we
	}
	// WDA sometimes returns 200 with an error payload.
	var env struct {
		Value struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		} `json:"value"`
	}
	if json.Unmarshal(data, &env) == nil && env.Value.Error != "" {
		return nil, &wdaError{Status: resp.StatusCode, Code: env.Value.Error, Msg: env.Value.Message}
	}
	return data, nil
}

// valueOf extracts the W3C "value" member (or the whole body if absent).
func valueOf(data []byte) json.RawMessage {
	var env struct {
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(data, &env) == nil && env.Value != nil {
		return env.Value
	}
	return data
}

// EnsureSession creates a session if none exists and returns its id.
func (c *Client) EnsureSession(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ensureLocked(ctx)
}

func (c *Client) ensureLocked(ctx context.Context) (string, error) {
	if c.sid != "" {
		return c.sid, nil
	}
	body := map[string]any{"capabilities": map[string]any{"alwaysMatch": map[string]any{}}}
	data, err := c.doRaw(ctx, http.MethodPost, c.BaseURL()+"/session", body)
	if err != nil {
		return "", err
	}
	var top struct {
		SessionID string `json:"sessionId"`
		Value     struct {
			SessionID string `json:"sessionId"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return "", fmt.Errorf("wda: decode session: %w", err)
	}
	sid := top.Value.SessionID
	if sid == "" {
		sid = top.SessionID
	}
	if sid == "" {
		return "", errors.New("wda: session response had no sessionId")
	}
	c.sid = sid
	c.w, c.h = 0, 0
	if len(c.SessionSettings) > 0 {
		// Best effort: a failure here must not fail the session itself.
		_, _ = c.doRaw(ctx, http.MethodPost, c.BaseURL()+"/session/"+sid+"/appium/settings", map[string]any{"settings": c.SessionSettings})
	}
	return sid, nil
}

// ResetSession forgets the current session so the next call creates a new one.
func (c *Client) ResetSession() {
	c.mu.Lock()
	c.sid = ""
	c.mu.Unlock()
}

// do runs a session-scoped request. "{sid}" in path is replaced with the
// session id; on an invalid-session error the session is recreated and the
// request retried once. Returns the "value" member.
func (c *Client) do(ctx context.Context, method, path string, in any) (json.RawMessage, error) {
	for attempt := 0; ; attempt++ {
		c.mu.Lock()
		sid, err := c.ensureLocked(ctx)
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		data, err := c.doRaw(ctx, method, c.BaseURL()+strings.ReplaceAll(path, "{sid}", sid), in)
		if err == nil {
			return valueOf(data), nil
		}
		var we *wdaError
		if attempt == 0 && errors.As(err, &we) && we.invalidSession() {
			c.mu.Lock()
			if c.sid == sid {
				c.sid = ""
			}
			c.mu.Unlock()
			continue
		}
		return nil, err
	}
}

// doGlobal runs a request that does not need a session.
func (c *Client) doGlobal(ctx context.Context, method, path string, in any) (json.RawMessage, error) {
	data, err := c.doRaw(ctx, method, c.BaseURL()+path, in)
	if err != nil {
		return nil, err
	}
	return valueOf(data), nil
}

// WindowSize returns the logical screen size in points (cached per session).
func (c *Client) WindowSize(ctx context.Context) (w, h int, err error) {
	c.mu.Lock()
	if c.w > 0 && c.h > 0 && c.sid != "" {
		w, h = c.w, c.h
		c.mu.Unlock()
		return w, h, nil
	}
	c.mu.Unlock()
	v, err := c.do(ctx, http.MethodGet, "/session/{sid}/window/size", nil)
	if err != nil {
		return 0, 0, err
	}
	var s struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}
	if err := json.Unmarshal(v, &s); err != nil {
		return 0, 0, fmt.Errorf("wda: decode window size: %w", err)
	}
	w, h = int(s.Width+0.5), int(s.Height+0.5)
	c.mu.Lock()
	c.w, c.h = w, h
	c.mu.Unlock()
	return w, h, nil
}

// Screenshot captures the screen. The PNG is returned as WDA produced it
// (possibly 2x/3x); Width/Height are logical points.
func (c *Client) Screenshot(ctx context.Context) (device.Screenshot, error) {
	data, err := c.doGlobal(ctx, http.MethodGet, "/screenshot", nil)
	if err != nil {
		return device.Screenshot{}, err
	}
	var b64 string
	if err := json.Unmarshal(data, &b64); err != nil {
		return device.Screenshot{}, fmt.Errorf("wda: screenshot is not a string: %w", err)
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		// WDA may wrap lines in base64.
		png, err = base64.StdEncoding.DecodeString(strings.NewReplacer("\n", "", "\r", "").Replace(b64))
		if err != nil {
			return device.Screenshot{}, fmt.Errorf("wda: decode screenshot: %w", err)
		}
	}
	cfg, _, derr := image.DecodeConfig(bytes.NewReader(png))
	w, h, werr := c.WindowSize(ctx)
	if werr != nil {
		if derr != nil {
			return device.Screenshot{}, werr
		}
		w, h = cfg.Width, cfg.Height // fall back to pixel size
	}
	if derr == nil && w > 0 {
		c.mu.Lock()
		c.scale = float64(cfg.Width) / float64(w)
		c.mu.Unlock()
	}
	return device.Screenshot{PNG: png, Width: w, Height: h}, nil
}

// SetSettings posts appium settings (e.g. mjpegServerFramerate).
func (c *Client) SetSettings(ctx context.Context, settings map[string]any) error {
	_, err := c.do(ctx, http.MethodPost, "/session/{sid}/appium/settings", map[string]any{"settings": settings})
	return err
}

// SetMJPEGSettings configures WDA's MJPEG server. Zero values are skipped.
// scaling is a percentage (1-100), quality is JPEG quality (1-100).
func (c *Client) SetMJPEGSettings(ctx context.Context, framerate, scaling, quality int) error {
	s := map[string]any{}
	if framerate > 0 {
		s["mjpegServerFramerate"] = framerate
	}
	if scaling > 0 {
		s["mjpegScalingFactor"] = scaling
	}
	if quality > 0 {
		s["mjpegServerScreenshotQuality"] = quality
	}
	if len(s) == 0 {
		return nil
	}
	return c.SetSettings(ctx, s)
}
