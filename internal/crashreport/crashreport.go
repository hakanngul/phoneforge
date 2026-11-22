// Package crashreport lists, reads and deletes crash reports from connected
// devices: iOS (.ips/.crash through the crash-report copy service) and Android
// (logcat crash buffer, DropBox entries, tombstones when readable).
package crashreport

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"pocketlab/internal/device"
)

// ErrNotFound means no report with the requested name exists.
var ErrNotFound = errors.New("crashreport: report not found")

// Report describes one crash report. Text is only filled for single fetches.
type Report struct {
	Name          string    `json:"name"`
	Platform      string    `json:"platform"`
	Source        string    `json:"source"` // ips | crash | logcat | dropbox | tombstone
	App           string    `json:"app,omitempty"`
	BundleID      string    `json:"bundleId,omitempty"`
	Process       string    `json:"process,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
	ExceptionType string    `json:"exceptionType,omitempty"`
	Signal        string    `json:"signal,omitempty"`
	OSVersion     string    `json:"osVersion,omitempty"`
	Kind          string    `json:"kind,omitempty"` // crash | jetsam | anr | other
	Size          int       `json:"size"`
}

// Query filters a listing.
type Query struct {
	App   string    // bundle id / package / process substring (case-insensitive)
	Since time.Time // zero = no lower bound
	Limit int       // 0 = no limit
}

// Provider is the platform-specific backend.
type Provider interface {
	List(ctx context.Context) ([]Report, error)
	Get(ctx context.Context, name string) (string, error)
	Delete(ctx context.Context, name string) error
}

// Filter applies q to reports, sorts newest first and applies the limit.
func Filter(in []Report, q Query) []Report {
	app := strings.ToLower(strings.TrimSpace(q.App))
	out := make([]Report, 0, len(in))
	for _, r := range in {
		if app != "" && !strings.Contains(strings.ToLower(r.BundleID), app) &&
			!strings.Contains(strings.ToLower(r.Process), app) &&
			!strings.Contains(strings.ToLower(r.App), app) {
			continue
		}
		if !q.Since.IsZero() && r.Timestamp.Before(q.Since) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out
}

// Service picks the provider for a device.
type Service struct {
	ADB string // adb binary; empty = "adb"
	// Override lets tests inject a provider; when set it is used for every device.
	Override func(info device.Info) (Provider, error)
	// SimulatorDir overrides ~/Library/Logs/DiagnosticReports (tests).
	SimulatorDir string
}

// ProviderFor returns the backend for the device, or device.ErrUnsupported.
func (s *Service) ProviderFor(info device.Info) (Provider, error) {
	if s != nil && s.Override != nil {
		return s.Override(info)
	}
	if info.Simulator {
		dir := ""
		if s != nil {
			dir = s.SimulatorDir
		}
		return newSimProvider(dir, info.Serial), nil
	}
	switch info.Platform {
	case device.PlatformIOS:
		return newIOSProvider(info.Serial), nil
	case device.PlatformAndroid:
		adb := "adb"
		if s != nil && s.ADB != "" {
			adb = s.ADB
		}
		return newAndroidProvider(adb, info.Serial), nil
	}
	return nil, fmt.Errorf("crash reports are not available for %s devices: %w", info.Platform, device.ErrUnsupported)
}
