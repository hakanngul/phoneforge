package httpapi

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pocketlab/internal/device"
	"pocketlab/internal/events"
)

// maxAPKSize caps uploaded installers.
const maxAPKSize = 512 << 20

// mountExtras registers routes backed by optional device capabilities.
func (a *api) mountExtras(r chi.Router) {
	r.Get("/stream.h264", a.withDevice(a.streamH264))
	r.Post("/double-tap", a.withDevice(a.doubleTap))
	r.Post("/drag", a.withDevice(a.drag))
	r.Post("/pinch", a.withDevice(a.pinch))
	r.Post("/open-url", a.withDevice(a.openURL))
	r.Post("/dismiss-keyboard", a.withDevice(a.dismissKeyboard))
	r.Get("/clipboard", a.withDevice(a.getClipboard))
	r.Post("/clipboard", a.withDevice(a.setClipboard))
	r.Post("/install-app", a.withDevice(a.installApp))
	r.Delete("/apps/{bundleId}", a.withDevice(a.uninstallApp))
	r.Post("/permissions", a.withDevice(a.setPermission))
	r.Get("/logs", a.withDevice(a.streamLogs))
	r.Get("/recording", a.withDevice(a.recording))
}

// done writes {"ok":true} or maps err (the local name ok is shadowed by type assertions).
func done(w http.ResponseWriter, err error) { ok(w, err) }

func unsupported(w http.ResponseWriter, what string) {
	deviceErr(w, fmt.Errorf("%s: %w", what, device.ErrUnsupported))
}

// noWriteDeadline removes any server write deadline for long-lived responses.
func noWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
}

// flushWriter flushes after every write.
type flushWriter struct {
	w io.Writer
	f http.Flusher
}

func (fw flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if n > 0 {
		fw.f.Flush()
	}
	return n, err
}

func (a *api) streamH264(w http.ResponseWriter, r *http.Request, dev device.Device) {
	vs, ok := dev.(device.VideoStreamer)
	if !ok {
		unsupported(w, "video streaming")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, "INTERNAL", http.StatusInternalServerError, "streaming unsupported")
		return
	}
	noWriteDeadline(w)
	w.Header().Set("Content-Type", "video/h264")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	// Errors after the header is sent cannot be reported; client disconnect ends the stream.
	_ = vs.StreamH264(r.Context(), flushWriter{w, flusher})
}

func (a *api) doubleTap(w http.ResponseWriter, r *http.Request, dev device.Device) {
	g, ok := dev.(device.GestureExtras)
	if !ok {
		unsupported(w, "double-tap")
		return
	}
	var b struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	if !decode(w, r, &b) {
		return
	}
	done(w, g.DoubleTap(r.Context(), b.X, b.Y))
}

func (a *api) drag(w http.ResponseWriter, r *http.Request, dev device.Device) {
	g, ok := dev.(device.GestureExtras)
	if !ok {
		unsupported(w, "drag")
		return
	}
	var b struct {
		X1         int `json:"x1"`
		Y1         int `json:"y1"`
		X2         int `json:"x2"`
		Y2         int `json:"y2"`
		DurationMs int `json:"durationMs"`
	}
	if !decode(w, r, &b) {
		return
	}
	done(w, g.Drag(r.Context(), b.X1, b.Y1, b.X2, b.Y2, b.DurationMs))
}

func (a *api) pinch(w http.ResponseWriter, r *http.Request, dev device.Device) {
	g, ok := dev.(device.GestureExtras)
	if !ok {
		unsupported(w, "pinch")
		return
	}
	var b struct {
		X          int     `json:"x"`
		Y          int     `json:"y"`
		Scale      float64 `json:"scale"`
		DurationMs int     `json:"durationMs"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Scale <= 0 {
		writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "scale must be > 0")
		return
	}
	done(w, g.Pinch(r.Context(), b.X, b.Y, b.Scale, b.DurationMs))
}

func (a *api) openURL(w http.ResponseWriter, r *http.Request, dev device.Device) {
	u, ok := dev.(device.URLOpener)
	if !ok {
		unsupported(w, "open-url")
		return
	}
	var b struct {
		URL string `json:"url"`
	}
	if !decode(w, r, &b) {
		return
	}
	if strings.TrimSpace(b.URL) == "" {
		writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "url is required")
		return
	}
	done(w, u.OpenURL(r.Context(), b.URL))
}

func (a *api) dismissKeyboard(w http.ResponseWriter, r *http.Request, dev device.Device) {
	k, ok := dev.(device.KeyboardDismisser)
	if !ok {
		unsupported(w, "dismiss-keyboard")
		return
	}
	done(w, k.DismissKeyboard(r.Context()))
}

func (a *api) getClipboard(w http.ResponseWriter, r *http.Request, dev device.Device) {
	c, ok := dev.(device.Clipboard)
	if !ok {
		unsupported(w, "clipboard")
		return
	}
	text, err := c.GetClipboard(r.Context())
	if err != nil {
		deviceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

func (a *api) setClipboard(w http.ResponseWriter, r *http.Request, dev device.Device) {
	c, ok := dev.(device.Clipboard)
	if !ok {
		unsupported(w, "clipboard")
		return
	}
	var b struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &b) {
		return
	}
	done(w, c.SetClipboard(r.Context(), b.Text))
}

func (a *api) installApp(w http.ResponseWriter, r *http.Request, dev device.Device) {
	inst, ok := dev.(device.AppInstaller)
	if !ok {
		unsupported(w, "install-app")
		return
	}
	name := dev.Info().Name
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "application/json" {
		var b struct {
			Path string `json:"path"`
		}
		if !decode(w, r, &b) {
			return
		}
		if strings.TrimSpace(b.Path) == "" {
			writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "path is required")
			return
		}
		events.Info(fmt.Sprintf("install-app: installing %s on %s", b.Path, name))
		err := inst.InstallApp(r.Context(), b.Path)
		a.reportInstall(w, name, b.Path, err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAPKSize+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "expected multipart form with a 'file' field or JSON {path}: "+err.Error())
		return
	}
	pattern := "pocketlab-*.apk"
	if dev.Info().Platform == device.PlatformIOS {
		pattern = "pocketlab-*.ipa"
	}
	tmp, err := os.CreateTemp("", pattern)
	if err != nil {
		writeErr(w, "INTERNAL", http.StatusInternalServerError, err.Error())
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	got := false
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "reading upload: "+perr.Error())
			return
		}
		if part.FormName() != "file" {
			continue
		}
		n, cerr := io.Copy(tmp, io.LimitReader(part, maxAPKSize+1))
		if cerr != nil {
			writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "reading upload: "+cerr.Error())
			return
		}
		if n > maxAPKSize {
			writeErr(w, "BAD_REQUEST", http.StatusRequestEntityTooLarge, "file exceeds 512 MiB")
			return
		}
		got = true
		break
	}
	if !got {
		writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "multipart field 'file' is required")
		return
	}
	if err := tmp.Close(); err != nil {
		writeErr(w, "INTERNAL", http.StatusInternalServerError, err.Error())
		return
	}
	events.Info("install-app: installing uploaded package on " + name)
	a.reportInstall(w, name, "uploaded package", inst.InstallApp(r.Context(), tmp.Name()))
}

func (a *api) reportInstall(w http.ResponseWriter, name, what string, err error) {
	if err != nil {
		events.Errorf("install-app failed on %s: %v", name, err)
	} else {
		events.Success(fmt.Sprintf("install-app: installed %s on %s", what, name))
	}
	ok(w, err)
}

func (a *api) uninstallApp(w http.ResponseWriter, r *http.Request, dev device.Device) {
	inst, has := dev.(device.AppInstaller)
	if !has {
		unsupported(w, "uninstall-app")
		return
	}
	id, err := url.PathUnescape(chi.URLParam(r, "bundleId"))
	if err != nil || strings.TrimSpace(id) == "" {
		writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "bundleId is required")
		return
	}
	err = inst.UninstallApp(r.Context(), id)
	if err != nil {
		events.Errorf("uninstall-app %s failed on %s: %v", id, dev.Info().Name, err)
	} else {
		events.Success(fmt.Sprintf("uninstall-app: removed %s from %s", id, dev.Info().Name))
	}
	ok(w, err)
}

func (a *api) setPermission(w http.ResponseWriter, r *http.Request, dev device.Device) {
	pm, ok := dev.(device.PermissionManager)
	if !ok {
		unsupported(w, "permissions")
		return
	}
	var b struct {
		BundleID   string `json:"bundleId"`
		Permission string `json:"permission"`
		Grant      bool   `json:"grant"`
	}
	if !decode(w, r, &b) {
		return
	}
	if strings.TrimSpace(b.BundleID) == "" || strings.TrimSpace(b.Permission) == "" {
		writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "bundleId and permission are required")
		return
	}
	done(w, pm.SetPermission(r.Context(), b.BundleID, b.Permission, b.Grant))
}

// sseLineWriter turns each written log line into an SSE data frame.
type sseLineWriter struct {
	w   io.Writer
	f   http.Flusher
	buf []byte
}

func (s *sseLineWriter) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(s.buf[:i]), "\r")
		s.buf = s.buf[i+1:]
		if line == "" {
			continue
		}
		if _, err := fmt.Fprintf(s.w, "data: %s\n\n", line); err != nil {
			return len(p), err
		}
		s.f.Flush()
	}
	// Bound memory when a source never emits newlines.
	if len(s.buf) > 1<<20 {
		s.buf = s.buf[:0]
	}
	return len(p), nil
}

func (a *api) streamLogs(w http.ResponseWriter, r *http.Request, dev device.Device) {
	ls, ok := dev.(device.LogStreamer)
	if !ok {
		unsupported(w, "logs")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, "INTERNAL", http.StatusInternalServerError, "streaming unsupported")
		return
	}
	noWriteDeadline(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	_ = ls.StreamLogs(r.Context(), &sseLineWriter{w: w, f: flusher})
}

func (a *api) recording(w http.ResponseWriter, r *http.Request, dev device.Device) {
	rec, ok := dev.(device.ScreenRecorder)
	if !ok {
		unsupported(w, "recording")
		return
	}
	secs := 10
	if v := r.URL.Query().Get("seconds"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "seconds must be an integer")
			return
		}
		secs = n
	}
	noWriteDeadline(w)
	name := dev.Info().Name
	events.Info(fmt.Sprintf("recording %ds on %s", secs, name))
	data, err := rec.RecordScreen(r.Context(), secs)
	if err != nil {
		events.Errorf("recording failed on %s: %v", name, err)
		deviceErr(w, err)
		return
	}
	events.Success(fmt.Sprintf("recording finished on %s (%d bytes)", name, len(data)))
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Disposition", `attachment; filename="recording.mp4"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
