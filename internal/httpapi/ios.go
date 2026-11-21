package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"phoneforge/internal/avcapture"
	"phoneforge/internal/device"
	"phoneforge/internal/events"
	"phoneforge/internal/iosbridge"
	"phoneforge/internal/iossim"
	"phoneforge/internal/wda"
)

// firstFrameWidth / firstFrameQuality shape the immediate screenshot frame the
// /mjpeg proxy emits before WDA's own stream delivers anything.
const (
	firstFrameWidth   = 600
	firstFrameQuality = 55
)

// IOSControl ties the iosbridge manager (WDA runner lifecycle) to the wda
// client wrapper and the REST endpoints. One wda.Client is kept per udid and
// re-pointed at the bridge's current WDA URL, so wrappers rebuilt on every
// device rescan always see the live runner.
type IOSControl struct {
	Mgr *iosbridge.Manager
	// Install installs the WDA runner (appPath "" = default build output).
	// Nil uses iosbridge.InstallRunner.
	Install func(udid, appPath string) error
	// Sim, when set, makes the routes also serve simulator devices.
	Sim *iossim.Control
	// AV, when set, lets /mjpeg prefer macOS native USB screen capture for
	// real iPhones (falling back to the WDA MJPEG stream).
	AV AVBackend

	gate   *avGate
	gateMu sync.Once

	mu      sync.Mutex
	clients map[string]*wda.Client
	sup     *iosbridge.Supervisor
}

// NewIOSControl returns an IOSControl over mgr.
func NewIOSControl(mgr *iosbridge.Manager) *IOSControl {
	return &IOSControl{Mgr: mgr, clients: map[string]*wda.Client{}}
}

// Client returns the (cached) WDA client for udid.
func (c *IOSControl) Client(udid string) *wda.Client {
	if c.isSim(udid) {
		return c.Sim.Client(udid)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cl := c.clients[udid]
	if cl == nil {
		cl = wda.New("")
		fps, sc, q := wda.MJPEGDefaults()
		cl.SessionSettings = map[string]any{
			"mjpegServerFramerate":         fps,
			"mjpegScalingFactor":           sc,
			"mjpegServerScreenshotQuality": q,
		}
		c.clients[udid] = cl
	}
	return cl
}

// Active reports whether the runner for udid is running, first pointing the
// client at its current WDA URL.
func (c *IOSControl) Active(udid string) func() bool {
	return func() bool {
		st := c.M(udid).Status(udid)
		if st.State != iosbridge.StateRunning || st.WDAURL == "" {
			return false
		}
		c.Client(udid).SetBaseURL(st.WDAURL)
		return true
	}
}

// Wrap routes a local iOS device through WDA while the runner is running.
// Other devices are returned unchanged.
func (c *IOSControl) Wrap(dev device.Device) device.Device {
	info := dev.Info()
	if info.Platform != device.PlatformIOS || info.Serial == "" || strings.HasPrefix(info.ID, "remote-") {
		return dev
	}
	return wda.Wrap(dev, c.Client(info.Serial), c.Active(info.Serial))
}

// udidFor returns the udid of a local iOS device.
func (a *api) udidFor(dev device.Device) (string, bool) {
	if a.ios == nil {
		return "", false
	}
	info := dev.Info()
	if info.Platform != device.PlatformIOS || info.Serial == "" || strings.HasPrefix(info.ID, "remote-") {
		return "", false
	}
	return info.Serial, true
}

func notIOS(w http.ResponseWriter) {
	writeErr(w, "NOT_IOS", http.StatusNotFound, "WebDriverAgent bridge is only available on local iOS devices")
}

// StartTuned starts the runner for udid and applies the MJPEG settings. It is
// the start path shared by explicit API calls and the auto-restart supervisor.
func (c *IOSControl) StartTuned(ctx context.Context, name, udid string, o iosbridge.Options) (iosbridge.Status, error) {
	events.Info("WDA bridge: starting on " + name)
	st, err := c.M(udid).Start(ctx, udid, c.prepare(udid, o))
	if err != nil {
		events.Errorf("WDA bridge failed on %s: %v", name, err)
		return st, err
	}
	cl := c.Client(udid)
	cl.SetBaseURL(st.WDAURL)
	mctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	fps, sc, q := wda.MJPEGDefaults()
	if merr := cl.SetMJPEGSettings(mctx, fps, sc, q); merr != nil {
		events.Info("WDA bridge: could not set MJPEG settings: " + merr.Error())
	}
	events.Success("WDA bridge running on " + name)
	return st, nil
}

// Supervise wires an auto-(re)start supervisor over the manager. The returned
// Supervisor must be fed discovery results (Seen) and run (Run) by the caller.
func (c *IOSControl) Supervise() *iosbridge.Supervisor {
	sup := iosbridge.NewSupervisor(c.Mgr, func(ctx context.Context, udid string) (iosbridge.Status, error) {
		sctx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		return c.StartTuned(sctx, udid, udid, iosbridge.Options{})
	})
	sup.Log = events.Info
	c.mu.Lock()
	c.sup = sup
	c.mu.Unlock()
	return sup
}

func (c *IOSControl) supervisor() *iosbridge.Supervisor {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sup
}

// iosStart is an explicit user start: it records the desired state (so WDA
// comes back after a restart or replug), clears any give-up, then starts.
func (a *api) iosStart(ctx context.Context, name, udid string, o iosbridge.Options) (iosbridge.Status, error) {
	if err := a.ios.M(udid).SetDesired(udid, true); err != nil {
		events.Errorf("WDA bridge: could not persist desired state: %v", err)
	}
	if sup := a.ios.supervisor(); sup != nil {
		sup.ClearBlocks(udid)
	}
	_, err := a.ios.StartTuned(ctx, name, udid, o)
	if errors.Is(err, iosbridge.ErrNotInstalled) || errors.Is(err, iosbridge.ErrTunnelUnsupported) {
		_ = a.ios.M(udid).SetDesired(udid, false) // cannot be satisfied; do not retry on every launch
	}
	return a.ios.M(udid).Status(udid), err
}

// iosUserStop is an explicit user stop; it clears the desired state.
func (a *api) iosUserStop(udid string) {
	if err := a.ios.M(udid).UserStop(udid); err != nil {
		events.Errorf("WDA bridge: could not persist desired state: %v", err)
	}
}

func iosStartErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, iosbridge.ErrBusy):
		writeErr(w, "IOSBRIDGE_BUSY", http.StatusConflict, err.Error())
	case errors.Is(err, iosbridge.ErrNotInstalled):
		writeErr(w, "IOSBRIDGE_NOT_INSTALLED", http.StatusConflict, err.Error())
	case errors.Is(err, iosbridge.ErrTunnelUnsupported):
		writeErr(w, "IOSBRIDGE_TUNNEL_ROOT", http.StatusConflict, err.Error())
	default:
		writeErr(w, "IOSBRIDGE_START_FAILED", http.StatusInternalServerError, err.Error())
	}
}

func (a *api) iosStatus(ctx context.Context, udid string) iosbridge.Status {
	rctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return a.ios.M(udid).Refresh(rctx, udid)
}

// iosGenericStatus is the /bridge shape for iOS: the Android fields plus WDA's.
type iosGenericStatus struct {
	bridgeStatus
	Platform       string `json:"platform"`
	State          string `json:"state"`
	WDAURL         string `json:"wdaUrl"`
	MJPEGURL       string `json:"mjpegUrl"`
	RunnerBundleID string `json:"runnerBundleId"`
}

func genericIOS(st iosbridge.Status) iosGenericStatus {
	return iosGenericStatus{
		bridgeStatus: bridgeStatus{Supported: true, Running: st.State == iosbridge.StateRunning, LastError: st.Error, Installed: st.Installed},
		Platform:     "ios", State: string(st.State), WDAURL: st.WDAURL, MJPEGURL: st.MJPEGURL, RunnerBundleID: st.RunnerBundleID,
	}
}

func decodeOpts(w http.ResponseWriter, r *http.Request) (iosbridge.Options, bool) {
	var o iosbridge.Options
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&o); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, "BAD_REQUEST", http.StatusBadRequest, err.Error())
			return o, false
		}
	}
	return o, true
}

// iosBridgeStart: POST /devices/{id}/iosbridge/start (also used by /bridge/start).
func (a *api) iosBridgeStart(w http.ResponseWriter, r *http.Request, dev device.Device) {
	udid, ok := a.udidFor(dev)
	if !ok {
		notIOS(w)
		return
	}
	o, ok := decodeOpts(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	st, err := a.iosStart(ctx, dev.Info().Name, udid, o)
	if err != nil {
		iosStartErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (a *api) iosBridgeStatus(w http.ResponseWriter, r *http.Request, dev device.Device) {
	udid, ok := a.udidFor(dev)
	if !ok {
		notIOS(w)
		return
	}
	writeJSON(w, http.StatusOK, a.iosStatus(r.Context(), udid))
}

func (a *api) iosBridgeStop(w http.ResponseWriter, r *http.Request, dev device.Device) {
	udid, ok := a.udidFor(dev)
	if !ok {
		notIOS(w)
		return
	}
	a.iosUserStop(udid)
	events.Info("WDA bridge stopped on " + dev.Info().Name)
	writeJSON(w, http.StatusOK, a.ios.M(udid).Status(udid))
}

func (a *api) iosBridgeLogs(w http.ResponseWriter, r *http.Request, dev device.Device) {
	udid, ok := a.udidFor(dev)
	if !ok {
		notIOS(w)
		return
	}
	a.ios.M(udid).HandleLogs(w, r, udid)
}

// iosBridgeInstall: POST /devices/{id}/iosbridge/install. Installs the locally
// built WDA runner onto the phone. Only runs on an explicit request.
func (a *api) iosBridgeInstall(w http.ResponseWriter, r *http.Request, dev device.Device) {
	udid, ok := a.udidFor(dev)
	if !ok {
		notIOS(w)
		return
	}
	if a.ios.isSim(udid) {
		writeErr(w, "IOSBRIDGE_SIM_NO_INSTALL", http.StatusConflict, "simulators need no helper install: build WDA with scripts/build-wda.sh --sim and use Start WDA")
		return
	}
	install := a.ios.Install
	if install == nil {
		install = iosbridge.InstallRunner
	}
	name := dev.Info().Name
	events.Info("WDA helper: installing on " + name)
	done := make(chan error, 1)
	go func() { done <- install(udid, "") }()
	var err error
	select {
	case err = <-done:
	case <-time.After(3 * time.Minute):
		err = errors.New("install timed out")
	case <-r.Context().Done():
		err = r.Context().Err()
	}
	if err != nil {
		events.Errorf("WDA helper install failed on %s: %v", name, err)
		if errors.Is(err, iosbridge.ErrRunnerNotBuilt) {
			writeErr(w, "IOSBRIDGE_RUNNER_NOT_BUILT", http.StatusConflict, "WebDriverAgent runner has not been built yet: run scripts/build-wda.sh, then try again")
			return
		}
		writeErr(w, "IOSBRIDGE_INSTALL_FAILED", http.StatusInternalServerError, err.Error())
		return
	}
	events.Success("WDA helper installed on " + name)
	writeJSON(w, http.StatusOK, a.iosStatus(r.Context(), udid))
}

// mjpegStream: GET /devices/{id}/mjpeg re-emits frames as a clean
// multipart/x-mixed-replace stream the browser can show in an <img>. For real
// iPhones the macOS native capture path (avcapture) is preferred when the
// device is offered by it; otherwise (or with ?source=wda) WDA's MJPEG stream
// is proxied. ?source=av forces native capture and fails instead of falling
// back. The X-Stream-Source header reports the source actually used, and
// X-Stream-Fallback explains why native capture was skipped.
func (a *api) mjpegStream(w http.ResponseWriter, r *http.Request, dev device.Device) {
	udid, ok := a.udidFor(dev)
	if !ok {
		unsupported(w, "MJPEG streaming")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, "INTERNAL", http.StatusInternalServerError, "streaming unsupported")
		return
	}
	source := strings.ToLower(r.URL.Query().Get("source"))
	if source != "" && source != "wda" && source != "av" {
		writeErr(w, "BAD_REQUEST", http.StatusBadRequest, "source must be wda or av")
		return
	}
	fallback := ""
	tryAV := source == "av" || (source == "" && a.avEnabled(udid))
	if source == "" && tryAV {
		// Auto: never block the stream on av. Only use it when it is known
		// good; otherwise serve WDA now and probe in the background.
		info := dev.Info()
		if !a.ios.avGate().knownGood(udid) {
			tryAV = false
			fallback = "native capture not verified yet"
			if e := a.ios.avGate().lastError(udid); e != "" {
				fallback = e
			}
			a.ios.avGate().maybeProbe(a.ios.AV, udid, []string{info.Name, info.Model}, nil)
		}
	}
	if tryAV && a.avEnabled(udid) {
		info := dev.Info()
		o := avOptions(r)
		sctx := r.Context()
		if source == "" { // known-good av must still start fast, else fall back to WDA
			var cancel context.CancelFunc
			sctx, cancel = context.WithTimeout(sctx, avKnownGoodWait)
			defer cancel()
		}
		sub, err := a.ios.AV.Subscribe(sctx, udid, o, info.Name, info.Model)
		if err == nil {
			defer sub.Close()
			a.ios.avGate().markGood(udid)
			w.Header().Set("X-Stream-Source", "av")
			serveMJPEG(w, r, flusher, nil,
				func(ctx context.Context, fn func([]byte) error) error {
					for {
						select {
						case <-ctx.Done():
							return ctx.Err()
						case f, ok := <-sub.Frames:
							if !ok {
								e := sub.Err()
								if e == nil {
									e = errors.New("capture ended")
								}
								a.ios.avGate().markBad(udid, e.Error())
								return e
							}
							if err := fn(f); err != nil {
								return err
							}
						}
					}
				})
			return
		}
		if r.Context().Err() != nil {
			return
		}
		fallback = err.Error()
		a.ios.avGate().markBad(udid, fallback)
		if inv, ok := a.ios.AV.(avInvalidator); ok {
			inv.Invalidate()
		}
		events.Info("avcapture unavailable for " + info.Name + ": " + fallback)
		if source == "av" {
			writeErr(w, "AVCAPTURE_UNAVAILABLE", http.StatusBadGateway, fallback)
			return
		}
	} else if source == "av" {
		writeErr(w, "AVCAPTURE_UNAVAILABLE", http.StatusBadGateway, "native capture is not enabled for this device")
		return
	}
	st := a.ios.M(udid).Status(udid)
	if st.State != iosbridge.StateRunning || st.MJPEGURL == "" {
		msg := "WDA bridge is not running"
		if fallback != "" {
			msg += " (native capture unavailable: " + fallback + ")"
		}
		writeErr(w, "IOSBRIDGE_NOT_RUNNING", http.StatusConflict, msg)
		return
	}
	w.Header().Set("X-Stream-Source", "wda")
	if fallback != "" {
		w.Header().Set("X-Stream-Fallback", fallback)
	}
	cl := a.ios.Client(udid)
	serveMJPEG(w, r, flusher,
		func(ctx context.Context) ([]byte, error) {
			return cl.ScreenshotJPEG(ctx, firstFrameWidth, firstFrameQuality)
		},
		func(ctx context.Context, fn func([]byte) error) error { return wda.MJPEGFrames(ctx, st.MJPEGURL, fn) })
}

// avGate returns the lazily created per-device native-capture gate.
func (c *IOSControl) avGate() *avGate {
	c.gateMu.Do(func() {
		if c.gate == nil {
			c.gate = newAVGate()
		}
	})
	return c.gate
}

// avEnabled reports whether native capture may be tried for udid (real
// devices only).
func (a *api) avEnabled(udid string) bool {
	return a.ios != nil && a.ios.AV != nil && !a.ios.isSim(udid)
}

// avOptions reads optional ?fps= &scale= &quality= overrides. The capture is
// shared, so the first viewer's values apply until it stops.
func avOptions(r *http.Request) avcapture.Options {
	var o avcapture.Options
	q := r.URL.Query()
	if v, err := strconv.Atoi(q.Get("fps")); err == nil {
		o.FPS = v
	}
	if v, err := strconv.ParseFloat(q.Get("scale"), 64); err == nil {
		o.Scale = v
	}
	if v, err := strconv.ParseFloat(q.Get("quality"), 64); err == nil {
		o.Quality = v
	}
	return o
}

// mjpegInfo: GET /devices/{id}/mjpeg/info reports which source /mjpeg would use.
func (a *api) mjpegInfo(w http.ResponseWriter, r *http.Request, dev device.Device) {
	udid, ok := a.udidFor(dev)
	if !ok {
		unsupported(w, "MJPEG streaming")
		return
	}
	out := map[string]any{"source": "wda"}
	av := map[string]any{"enabled": a.avEnabled(udid)}
	if a.avEnabled(udid) {
		info := dev.Info()
		g := a.ios.avGate()
		good := g.knownGood(udid)
		if !good {
			g.maybeProbe(a.ios.AV, udid, []string{info.Name, info.Model}, nil)
		}
		err := a.ios.AV.Check(r.Context(), udid, info.Name, info.Model)
		av["verified"] = good
		switch {
		case err != nil:
			av["available"] = false
			av["reason"] = err.Error()
		case !good:
			// Listed, but no frame has been seen yet: /mjpeg serves WDA until a probe succeeds.
			av["available"] = false
			reason := "native capture not verified yet (background probe pending)"
			if e := g.lastError(udid); e != "" {
				reason = e
			}
			av["reason"] = reason
		default:
			av["available"] = true
			out["source"] = "av"
		}
	}
	out["av"] = av
	st := a.ios.M(udid).Status(udid)
	out["wda"] = map[string]any{"running": st.State == iosbridge.StateRunning && st.MJPEGURL != ""}
	writeJSON(w, http.StatusOK, out)
}

// serveMJPEG writes a multipart/x-mixed-replace stream. The first frame comes
// from first (a one-off screenshot, started immediately) unless the real
// stream delivers a frame sooner; afterwards every frame comes from stream.
// Each part is closed with the next boundary right away so browsers paint it
// without waiting for the following frame.
// wantRawMJPEG reports whether the client asked for the opaque-content-type
// variant of the stream (?format=raw or Accept: application/x-pf-mjpeg).
func wantRawMJPEG(r *http.Request) bool {
	if strings.EqualFold(r.URL.Query().Get("format"), "raw") {
		return true
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/x-pf-mjpeg")
}

func serveMJPEG(w http.ResponseWriter, r *http.Request, flusher http.Flusher,
	first func(ctx context.Context) ([]byte, error),
	stream func(ctx context.Context, fn func([]byte) error) error) {
	const boundary = "pfframe"
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	h := w.Header()
	if wantRawMJPEG(r) {
		// WebKit special-cases multipart/x-mixed-replace and never exposes it to
		// fetch() streams; same framing, opaque content type, boundary in a header.
		h.Set("Content-Type", "application/octet-stream")
		h.Set("X-Boundary", boundary)
		h.Set("Access-Control-Expose-Headers", "X-Boundary, X-Stream-Source, X-Stream-Fallback")
	} else {
		h.Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
	}
	h.Set("Cache-Control", "no-store")

	var (
		mu       sync.Mutex
		wrote    bool
		streamed bool
		preamble bool
		done     bool
	)
	emit := func(jpeg []byte, fromStream bool) error {
		mu.Lock()
		defer mu.Unlock()
		if done {
			return context.Canceled
		}
		if fromStream {
			streamed = true
		} else if streamed {
			return nil // the real stream already won
		}
		if !preamble {
			preamble = true
			if _, err := fmt.Fprintf(w, "--%s\r\n", boundary); err != nil {
				return err
			}
		}
		wrote = true
		if _, err := fmt.Fprintf(w, "Content-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(jpeg)); err != nil {
			return err
		}
		if _, err := w.Write(jpeg); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "\r\n--%s\r\n", boundary); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	if first != nil {
		go func() {
			fctx, fcancel := context.WithTimeout(ctx, 15*time.Second)
			defer fcancel()
			if jpeg, err := first(fctx); err == nil && len(jpeg) > 0 {
				_ = emit(jpeg, false)
			}
		}()
	}
	err := stream(ctx, func(jpeg []byte) error { return emit(jpeg, true) })
	cancel()
	mu.Lock()
	defer mu.Unlock()
	done = true
	if err != nil && !wrote && r.Context().Err() == nil {
		h.Del("Content-Type")
		writeErr(w, "IOSBRIDGE_MJPEG_FAILED", http.StatusBadGateway, err.Error())
	}
}
