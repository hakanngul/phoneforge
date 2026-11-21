package uia2

import (
	"context"
	"sync"
)

// Manager holds the shared assets and one Bridge per device serial.
type Manager struct {
	adbPath string
	assets  *Assets

	mu      sync.Mutex
	bridges map[string]*Bridge
}

// NewManager creates a manager; assetsDir is where the APKs live.
func NewManager(adbPath, assetsDir string) *Manager {
	return &Manager{adbPath: adbPath, assets: &Assets{Dir: assetsDir}, bridges: map[string]*Bridge{}}
}

// For returns (creating if needed) the bridge for serial.
func (m *Manager) For(serial string) *Bridge {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bridges[serial]
	if !ok {
		b = New(m.adbPath, serial, m.assets)
		m.bridges[serial] = b
	}
	return b
}

// Assets returns the shared assets handle.
func (m *Manager) Assets() *Assets { return m.assets }

// StopAll stops every bridge, ignoring individual errors.
func (m *Manager) StopAll(ctx context.Context) {
	m.mu.Lock()
	bs := make([]*Bridge, 0, len(m.bridges))
	for _, b := range m.bridges {
		bs = append(bs, b)
	}
	m.mu.Unlock()
	for _, b := range bs {
		_ = b.Stop(ctx)
	}
}
