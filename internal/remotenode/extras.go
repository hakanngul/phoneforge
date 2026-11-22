package remotenode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pocketlab/internal/device"
)

// Long-lived calls must not be cut off by httpClient's 60s timeout.
var (
	streamClient = &http.Client{}
	longClient   = &http.Client{Timeout: 10 * time.Minute}
)

var (
	_ device.VideoStreamer     = (*remoteDevice)(nil)
	_ device.GestureExtras     = (*remoteDevice)(nil)
	_ device.URLOpener         = (*remoteDevice)(nil)
	_ device.Clipboard         = (*remoteDevice)(nil)
	_ device.AppInstaller      = (*remoteDevice)(nil)
	_ device.PermissionManager = (*remoteDevice)(nil)
	_ device.LogStreamer       = (*remoteDevice)(nil)
	_ device.ScreenRecorder    = (*remoteDevice)(nil)
	_ device.KeyboardDismisser = (*remoteDevice)(nil)
)

// open starts a streaming GET and returns the response body (2xx only).
func (d *remoteDevice) open(ctx context.Context, suffix string) (io.ReadCloser, error) {
	req, err := newReq(ctx, d.node, http.MethodGet, d.path(suffix), "", nil)
	if err != nil {
		return nil, err
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("remote node %s: %w", d.node.Name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		if resp.StatusCode == http.StatusNotImplemented {
			return nil, fmt.Errorf("remote node %s: GET %s: %w", d.node.Name, suffix, device.ErrUnsupported)
		}
		return nil, fmt.Errorf("remote node %s: GET %s: status %d: %s", d.node.Name, suffix, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp.Body, nil
}

// StreamH264 copies the remote /stream.h264 to w until ctx is done.
func (d *remoteDevice) StreamH264(ctx context.Context, w io.Writer) error {
	body, err := d.open(ctx, "/stream.h264")
	if err != nil {
		return err
	}
	defer body.Close()
	buf := make([]byte, 32<<10)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if rerr != nil {
			if rerr == io.EOF || ctx.Err() != nil {
				return nil
			}
			return rerr
		}
	}
}

// StreamLogs reads the remote SSE /logs and writes plain lines to w.
func (d *remoteDevice) StreamLogs(ctx context.Context, w io.Writer) error {
	body, err := d.open(ctx, "/logs")
	if err != nil {
		return err
	}
	defer body.Close()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		line = strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return sc.Err()
}

// DoubleTap POSTs /double-tap.
func (d *remoteDevice) DoubleTap(ctx context.Context, x, y int) error {
	return d.post(ctx, "/double-tap", map[string]int{"x": x, "y": y})
}

// Drag POSTs /drag.
func (d *remoteDevice) Drag(ctx context.Context, x1, y1, x2, y2, ms int) error {
	return d.post(ctx, "/drag", map[string]int{"x1": x1, "y1": y1, "x2": x2, "y2": y2, "durationMs": ms})
}

// Pinch POSTs /pinch.
func (d *remoteDevice) Pinch(ctx context.Context, cx, cy int, scale float64, ms int) error {
	return d.post(ctx, "/pinch", map[string]any{"x": cx, "y": cy, "scale": scale, "durationMs": ms})
}

// OpenURL POSTs /open-url.
func (d *remoteDevice) OpenURL(ctx context.Context, u string) error {
	return d.post(ctx, "/open-url", map[string]string{"url": u})
}

// DismissKeyboard POSTs /dismiss-keyboard.
func (d *remoteDevice) DismissKeyboard(ctx context.Context) error {
	return d.post(ctx, "/dismiss-keyboard", map[string]string{})
}

// SetClipboard POSTs /clipboard.
func (d *remoteDevice) SetClipboard(ctx context.Context, text string) error {
	return d.post(ctx, "/clipboard", map[string]string{"text": text})
}

// GetClipboard GETs /clipboard.
func (d *remoteDevice) GetClipboard(ctx context.Context) (string, error) {
	b, err := do(ctx, d.node, http.MethodGet, d.path("/clipboard"), nil)
	if err != nil {
		return "", err
	}
	var r struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", err
	}
	return r.Text, nil
}

// InstallApp uploads the local file to the remote node's /install-app.
func (d *remoteDevice) InstallApp(ctx context.Context, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("file", filepath.Base(localPath))
		if err == nil {
			_, err = io.Copy(part, f)
		}
		if err == nil {
			err = mw.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	_, err = doRaw(ctx, longClient, d.node, http.MethodPost, d.path("/install-app"), mw.FormDataContentType(), pr)
	_ = pr.CloseWithError(err)
	return err
}

// UninstallApp sends DELETE /apps/{bundleId}.
func (d *remoteDevice) UninstallApp(ctx context.Context, bundleID string) error {
	_, err := do(ctx, d.node, http.MethodDelete, d.path("/apps/"+url.PathEscape(bundleID)), nil)
	return err
}

// SetPermission POSTs /permissions.
func (d *remoteDevice) SetPermission(ctx context.Context, bundleID, permission string, grant bool) error {
	return d.post(ctx, "/permissions", map[string]any{"bundleId": bundleID, "permission": permission, "grant": grant})
}

// RecordScreen GETs /recording?seconds=N and returns the MP4 bytes.
func (d *remoteDevice) RecordScreen(ctx context.Context, seconds int) ([]byte, error) {
	body, err := d.open(ctx, "/recording?seconds="+strconv.Itoa(seconds))
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(io.LimitReader(body, 512<<20))
}
