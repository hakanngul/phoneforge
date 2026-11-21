package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"phoneforge/internal/device"
	"phoneforge/internal/events"
	"phoneforge/internal/iosbridge"
	"phoneforge/internal/uia2"
)

// bridgeStatus is the JSON shape of GET/POST /devices/{id}/bridge[/start|/stop].
type bridgeStatus struct {
	Supported bool    `json:"supported"`
	Running   bool    `json:"running"`
	Port      int     `json:"port"`
	SessionID string  `json:"sessionId"`
	Since     *string `json:"since"`
	LastError string  `json:"lastError"`
	Installed bool    `json:"installed"`
}

// bridgeFor returns the UiAutomator2 bridge for a local Android device.
func (a *api) bridgeFor(dev device.Device) (*uia2.Bridge, bool) {
	if a.bridges == nil {
		return nil, false
	}
	info := dev.Info()
	if info.Platform != device.PlatformAndroid || info.Serial == "" || strings.HasPrefix(info.ID, "remote-") {
		return nil, false
	}
	return a.bridges.For(info.Serial), true
}

func (a *api) bridgeState(ctx context.Context, b *uia2.Bridge) bridgeStatus {
	st := b.Status()
	out := bridgeStatus{Supported: true, Running: st.Running, Port: st.Port, SessionID: st.SessionID, LastError: st.LastError}
	if !st.Since.IsZero() {
		s := st.Since.UTC().Format(time.RFC3339)
		out.Since = &s
	}
	ictx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if ok, err := b.Installed(ictx); err == nil {
		out.Installed = ok
	}
	return out
}

func (a *api) bridgeGet(w http.ResponseWriter, r *http.Request, dev device.Device) {
	if udid, ok := a.udidFor(dev); ok {
		writeJSON(w, http.StatusOK, genericIOS(a.iosStatus(r.Context(), udid)))
		return
	}
	b, ok := a.bridgeFor(dev)
	if !ok {
		writeJSON(w, http.StatusOK, bridgeStatus{})
		return
	}
	writeJSON(w, http.StatusOK, a.bridgeState(r.Context(), b))
}

func (a *api) bridgeStart(w http.ResponseWriter, r *http.Request, dev device.Device) {
	if udid, ok := a.udidFor(dev); ok {
		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		defer cancel()
		if _, err := a.iosStart(ctx, dev.Info().Name, udid, iosbridge.Options{}); err != nil {
			iosStartErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, genericIOS(a.iosStatus(r.Context(), udid)))
		return
	}
	b, ok := a.bridgeFor(dev)
	if !ok {
		unsupported(w, "UiAutomator2 bridge")
		return
	}
	name := dev.Info().Name
	events.Info("UiAutomator2 bridge: starting on " + name)
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := b.Start(ctx); err != nil {
		events.Errorf("UiAutomator2 bridge failed on %s: %v", name, err)
		if errors.Is(err, uia2.ErrAssetsMissing) {
			writeErr(w, "BRIDGE_ASSETS_MISSING", http.StatusConflict, err.Error())
			return
		}
		writeErr(w, "BRIDGE_START_FAILED", http.StatusInternalServerError, err.Error())
		return
	}
	events.Success("UiAutomator2 bridge running on " + name)
	writeJSON(w, http.StatusOK, a.bridgeState(r.Context(), b))
}

func (a *api) bridgeStop(w http.ResponseWriter, r *http.Request, dev device.Device) {
	if udid, ok := a.udidFor(dev); ok {
		a.iosUserStop(udid)
		events.Info("WDA bridge stopped on " + dev.Info().Name)
		writeJSON(w, http.StatusOK, genericIOS(a.ios.M(udid).Status(udid)))
		return
	}
	b, ok := a.bridgeFor(dev)
	if !ok {
		unsupported(w, "UiAutomator2 bridge")
		return
	}
	name := dev.Info().Name
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := b.Stop(ctx); err != nil {
		events.Errorf("UiAutomator2 bridge stop on %s: %v", name, err)
	} else {
		events.Info("UiAutomator2 bridge stopped on " + name)
	}
	writeJSON(w, http.StatusOK, a.bridgeState(r.Context(), b))
}

type assetsStatus struct {
	OK        bool   `json:"ok"`
	ServerAPK string `json:"serverApk"`
	TestAPK   string `json:"testApk"`
	Dir       string `json:"dir"`
}

func (a *api) assetsState() assetsStatus {
	as := a.bridges.Assets()
	s, t, ok := as.Status()
	return assetsStatus{OK: ok, ServerAPK: s, TestAPK: t, Dir: as.Dir}
}

func (a *api) bridgeAssets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.assetsState())
}

// bridgeAssetsDownload fetches the UiAutomator2 APKs. Explicit user action only.
func (a *api) bridgeAssetsDownload(w http.ResponseWriter, r *http.Request) {
	events.Info("UiAutomator2 assets: downloading")
	if err := a.bridges.Assets().Download(r.Context()); err != nil {
		events.Errorf("UiAutomator2 assets download failed: %v", err)
		writeErr(w, "BRIDGE_ASSETS_DOWNLOAD_FAILED", http.StatusBadGateway, err.Error())
		return
	}
	events.Success("UiAutomator2 assets downloaded")
	writeJSON(w, http.StatusOK, a.assetsState())
}
