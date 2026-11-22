package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// UserSettingsFile is the file name of the small per-user settings store that
// lives in the Pocketlab state directory (~/.pocketlab by default).
const UserSettingsFile = "settings.json"

// Keys used in the user settings file.
const (
	// KeyWDATeamID is the Apple development Team ID used to sign WebDriverAgent.
	KeyWDATeamID = "wdaTeamId"
)

var settingsMu sync.Mutex

func readSettings(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if len(b) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ReadSetting returns the string stored under key in the settings file at
// path ("" when the file or key is missing or unreadable).
func ReadSetting(path, key string) string {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	m, err := readSettings(path)
	if err != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// WriteSetting stores value under key in the settings file at path, keeping
// every other key. An empty value removes the key. The write is atomic.
func WriteSetting(path, key, value string) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	m, err := readSettings(path)
	if err != nil {
		m = map[string]any{} // unreadable file: start fresh rather than fail the user
	}
	if value == "" {
		delete(m, key)
	} else {
		m[key] = value
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
