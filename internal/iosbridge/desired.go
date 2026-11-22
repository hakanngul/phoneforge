package iosbridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// stateFile is the JSON document persisted by desiredStore.
type stateFile struct {
	// Desired maps udid -> true for devices whose WDA bridge the user started
	// (and has not explicitly stopped since).
	Desired map[string]bool `json:"desired"`
}

// desiredStore remembers which devices should have their WDA bridge running
// across server restarts (~/.pocketlab/iosbridge-state.json). An empty path
// keeps the state in memory only.
type desiredStore struct {
	mu   sync.Mutex
	path string
	set  map[string]bool
}

func newDesiredStore(path string) *desiredStore {
	d := &desiredStore{path: path, set: map[string]bool{}}
	d.load()
	return d
}

func (d *desiredStore) load() {
	if d.path == "" {
		return
	}
	b, err := os.ReadFile(d.path)
	if err != nil {
		return
	}
	var f stateFile
	if json.Unmarshal(b, &f) != nil {
		return
	}
	for id, on := range f.Desired {
		if on && id != "" {
			d.set[id] = true
		}
	}
}

func (d *desiredStore) get(udid string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.set[udid]
}

func (d *desiredStore) list() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.set))
	for id := range d.set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// put sets the flag and persists. A no-op change does not touch the disk.
func (d *desiredStore) put(udid string, on bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.set[udid] == on {
		return nil
	}
	if on {
		d.set[udid] = true
	} else {
		delete(d.set, udid)
	}
	return d.saveLocked()
}

func (d *desiredStore) saveLocked() error {
	if d.path == "" {
		return nil
	}
	f := stateFile{Desired: map[string]bool{}}
	for id := range d.set {
		f.Desired[id] = true
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return err
	}
	tmp := d.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, d.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// SetStatePath loads the desired-state file at path and persists every later
// change there. Call once at startup, before Supervisor.Run.
func (m *Manager) SetStatePath(path string) {
	m.mu.Lock()
	m.desired = newDesiredStore(path)
	m.mu.Unlock()
}

func (m *Manager) store() *desiredStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.desired
}

// Desired reports whether the user wants WDA kept running on udid.
func (m *Manager) Desired(udid string) bool { return m.store().get(udid) }

// DesiredDevices lists the udids whose desired state is on.
func (m *Manager) DesiredDevices() []string { return m.store().list() }

// SetDesired records (and persists) the desired state for udid.
func (m *Manager) SetDesired(udid string, on bool) error {
	if udid == "" {
		return errors.New("iosbridge: empty udid")
	}
	return m.store().put(udid, on)
}

// UserStop is an explicit Stop: it clears the desired state, then stops the
// runner. (Plain Stop/StopAll, used on server shutdown, keep the state so the
// bridge comes back on the next start.)
func (m *Manager) UserStop(udid string) error {
	err := m.SetDesired(udid, false)
	_ = m.Stop(udid)
	return err
}
