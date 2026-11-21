package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// SecretService is the Keychain service under which named secrets are stored
// (account = secret name).
const SecretService = ServicePrefix + ".secret"

// writeJSONAtomic writes v as JSON to path with 0600 permissions.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cred-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// SecretStore manages named secrets stored in a Backend (the Keychain by
// default). A small JSON index file keeps only the names; values are never
// written to disk and Names/Has never expose them.
type SecretStore struct {
	mu    sync.Mutex
	path  string
	b     Backend
	names map[string]struct{}
}

type secretIndex struct {
	Names []string `json:"names"`
}

// New opens (or creates) a SecretStore with its name index at indexPath, using
// DefaultBackend (macOS Keychain).
func New(indexPath string) (*SecretStore, error) {
	return NewWithBackend(indexPath, DefaultBackend)
}

// NewWithBackend is like New with an explicit Backend.
func NewWithBackend(indexPath string, b Backend) (*SecretStore, error) {
	s := &SecretStore{path: indexPath, b: b, names: map[string]struct{}{}}
	data, err := os.ReadFile(indexPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		var idx secretIndex
		if err := json.Unmarshal(data, &idx); err != nil {
			return nil, fmt.Errorf("credentials: bad secret index: %w", err)
		}
		for _, n := range idx.Names {
			s.names[n] = struct{}{}
		}
	}
	return s, nil
}

func (s *SecretStore) saveLocked() error {
	return writeJSONAtomic(s.path, secretIndex{Names: s.namesLocked()})
}

func (s *SecretStore) namesLocked() []string {
	out := make([]string, 0, len(s.names))
	for n := range s.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Set stores value under name (creating or updating it).
func (s *SecretStore) Set(name, value string) error {
	if name == "" {
		return errors.New("credentials: empty secret name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.b.Set(SecretService, name, value); err != nil {
		return err
	}
	if _, ok := s.names[name]; ok {
		return nil
	}
	s.names[name] = struct{}{}
	return s.saveLocked()
}

// Has reports whether a secret with that name is registered.
func (s *SecretStore) Has(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.names[name]
	return ok
}

// Delete removes the secret and its index entry. Deleting an unknown name is
// not an error.
func (s *SecretStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.b.Delete(SecretService, name); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if _, ok := s.names[name]; !ok {
		return nil
	}
	delete(s.names, name)
	return s.saveLocked()
}

// Names returns the sorted secret names. It never exposes values.
func (s *SecretStore) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.namesLocked()
}

// Reveal returns the secret value. It is for server-side use only (for example
// typing a password into a device) and must never be wired to an HTTP response.
func (s *SecretStore) Reveal(name string) (string, error) {
	s.mu.Lock()
	_, ok := s.names[name]
	s.mu.Unlock()
	if !ok {
		return "", ErrNotFound
	}
	return s.b.Get(SecretService, name)
}
