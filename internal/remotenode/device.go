package remotenode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pocketlab/internal/device"
)

// httpClient is shared by all remote calls.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// remoteDevice implements device.Device by proxying to a remote node's REST API.
type remoteDevice struct {
	node     Node
	remoteID string
	info     device.Info
}

var _ device.Device = (*remoteDevice)(nil)

// newRemoteDevice wraps a device.Info reported by node.
func newRemoteDevice(node Node, remote device.Info) *remoteDevice {
	info := remote
	info.ID = "remote-" + node.ID + "-" + remote.ID
	info.Name = remote.Name + " @" + node.Name
	return &remoteDevice{node: node, remoteID: remote.ID, info: info}
}

// Info returns the locally-namespaced device info.
func (d *remoteDevice) Info() device.Info { return d.info }

func (d *remoteDevice) path(suffix string) string {
	return "/api/v1/devices/" + url.PathEscape(d.remoteID) + suffix
}

// do performs a request against node and returns the body on 2xx.
func do(ctx context.Context, node Node, method, path string, body any) ([]byte, error) {
	return doClient(ctx, httpClient, node, method, path, body)
}

// doClient is do with an explicit http client (for long-running calls).
func doClient(ctx context.Context, hc *http.Client, node Node, method, path string, body any) ([]byte, error) {
	var rd io.Reader
	ctype := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
		ctype = "application/json"
	}
	return doRaw(ctx, hc, node, method, path, ctype, rd)
}

// doRaw performs a request with an arbitrary body and returns the response body on 2xx.
func doRaw(ctx context.Context, hc *http.Client, node Node, method, path, ctype string, rd io.Reader) ([]byte, error) {
	req, err := newReq(ctx, node, method, path, ctype, rd)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remote node %s: %w", node.Name, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound && strings.Contains(path, "/devices/") {
		return nil, fmt.Errorf("remote node %s: %w", node.Name, device.ErrNotFound)
	}
	if resp.StatusCode == http.StatusNotImplemented {
		return nil, fmt.Errorf("remote node %s: %s %s: %w", node.Name, method, path, device.ErrUnsupported)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("remote node %s: %s %s: status %d: %s", node.Name, method, path, resp.StatusCode, msg)
	}
	return data, nil
}

func newReq(ctx context.Context, node Node, method, path, ctype string, rd io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, node.Addr+path, rd)
	if err != nil {
		return nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if node.Token != "" {
		req.Header.Set("X-API-Key", node.Token)
	}
	return req, nil
}

func (d *remoteDevice) post(ctx context.Context, suffix string, body any) error {
	_, err := do(ctx, d.node, http.MethodPost, d.path(suffix), body)
	return err
}

// Screenshot GETs /screenshot and decodes the PNG size.
func (d *remoteDevice) Screenshot(ctx context.Context) (device.Screenshot, error) {
	b, err := do(ctx, d.node, http.MethodGet, d.path("/screenshot"), nil)
	if err != nil {
		return device.Screenshot{}, err
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return device.Screenshot{}, fmt.Errorf("remote screenshot is not a valid PNG: %w", err)
	}
	return device.Screenshot{PNG: b, Width: cfg.Width, Height: cfg.Height}, nil
}

// UITree GETs /ui-tree.
func (d *remoteDevice) UITree(ctx context.Context) (device.Element, error) {
	b, err := do(ctx, d.node, http.MethodGet, d.path("/ui-tree"), nil)
	if err != nil {
		return device.Element{}, err
	}
	var el device.Element
	if err := json.Unmarshal(b, &el); err != nil {
		return device.Element{}, err
	}
	return el, nil
}

// Tap POSTs /tap.
func (d *remoteDevice) Tap(ctx context.Context, x, y int) error {
	return d.post(ctx, "/tap", map[string]int{"x": x, "y": y})
}

// LongPress POSTs /long-press.
func (d *remoteDevice) LongPress(ctx context.Context, x, y int, ms int) error {
	return d.post(ctx, "/long-press", map[string]int{"x": x, "y": y, "ms": ms})
}

// Swipe POSTs /swipe.
func (d *remoteDevice) Swipe(ctx context.Context, x1, y1, x2, y2, ms int) error {
	return d.post(ctx, "/swipe", map[string]int{"x1": x1, "y1": y1, "x2": x2, "y2": y2, "durationMs": ms})
}

// TypeText POSTs /type.
func (d *remoteDevice) TypeText(ctx context.Context, text string) error {
	return d.post(ctx, "/type", map[string]string{"text": text})
}

// PressKey POSTs /press-key.
func (d *remoteDevice) PressKey(ctx context.Context, k device.Key) error {
	return d.post(ctx, "/press-key", map[string]string{"key": string(k)})
}

// ListApps GETs /apps.
func (d *remoteDevice) ListApps(ctx context.Context) ([]device.AppInfo, error) {
	b, err := do(ctx, d.node, http.MethodGet, d.path("/apps"), nil)
	if err != nil {
		return nil, err
	}
	var apps []device.AppInfo
	if err := json.Unmarshal(b, &apps); err != nil {
		return nil, err
	}
	return apps, nil
}

// LaunchApp POSTs /launch-app.
func (d *remoteDevice) LaunchApp(ctx context.Context, bundleID string) error {
	return d.post(ctx, "/launch-app", map[string]string{"bundleId": bundleID})
}

// StopApp POSTs /stop-app.
func (d *remoteDevice) StopApp(ctx context.Context, bundleID string) error {
	return d.post(ctx, "/stop-app", map[string]string{"bundleId": bundleID})
}
