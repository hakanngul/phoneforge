package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// StateFile is the persisted updater state inside the state directory.
const StateFile = "update.json"

// Settings are the user-facing updater preferences.
type Settings struct {
	AutoCheck    bool   `json:"autoCheck"`
	Channel      string `json:"channel"`
	AutoDownload bool   `json:"autoDownload"`
}

// DefaultSettings returns auto-check on, stable channel, no auto-download.
func DefaultSettings() Settings { return Settings{AutoCheck: true, Channel: "stable"} }

// Pending describes an install that has not yet been confirmed healthy.
type Pending struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	AppPath    string    `json:"appPath"`
	BackupPath string    `json:"backupPath"`
	StartedAt  time.Time `json:"startedAt"`
	Launches   int       `json:"launches"`
	Healthy    bool      `json:"healthy"`
}

// Backup records where the previous app was kept after an install.
type Backup struct {
	Path string `json:"path"`
	From string `json:"from"` // version stored in the backup
	To   string `json:"to"`   // version that replaced it
}

// State is persisted at <dir>/update.json.
type State struct {
	Settings     Settings  `json:"settings"`
	SkipVersion  string    `json:"skipVersion,omitempty"`
	RemindUntil  time.Time `json:"remindUntil,omitempty"`
	LastCheck    time.Time `json:"lastCheck,omitempty"`
	LastError    string    `json:"lastError,omitempty"`
	Failures     int       `json:"failures,omitempty"`
	ETag         string    `json:"etag,omitempty"`
	ManifestData []byte    `json:"manifest,omitempty"`
	ManifestSig  []byte    `json:"manifestSig,omitempty"`
	Pending      *Pending  `json:"pending,omitempty"`
	Backup       *Backup   `json:"backup,omitempty"` // previous app kept for manual rollback
}

// LoadState reads <dir>/update.json; a missing or corrupt file yields defaults.
func LoadState(dir string) *State {
	st := &State{Settings: DefaultSettings()}
	b, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil {
		return st
	}
	var loaded State
	loaded.Settings = DefaultSettings()
	if json.Unmarshal(b, &loaded) != nil {
		return st
	}
	if loaded.Settings.Channel != "stable" && loaded.Settings.Channel != "beta" {
		loaded.Settings.Channel = "stable"
	}
	return &loaded
}

// Save writes the state atomically (tmp + rename, 0600).
func (s *State) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, StateFile+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, StateFile))
}
