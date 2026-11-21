// Package credentials implements the PhoneForge secrets manager and "App
// Cards" (per-app login profiles).
//
// Secret values live only in the macOS Keychain (via the `security` CLI) and
// are never written to the JSON metadata files nor returned by the HTTP API.
package credentials

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ServicePrefix is the fixed prefix of every Keychain service name used by
// PhoneForge.
const ServicePrefix = "phoneforge"

// keychainTimeout bounds every call to the `security` CLI.
const keychainTimeout = 10 * time.Second

// ErrNotFound is returned when a secret does not exist in the backend.
var ErrNotFound = errors.New("credentials: secret not found")

// Backend abstracts secret storage so tests need no real Keychain.
type Backend interface {
	Set(service, account, value string) error
	Get(service, account string) (string, error)
	Delete(service, account string) error
}

// DefaultBackend is the Backend used by the package-level Set/Get/Delete and by
// New: the macOS Keychain through the `security` CLI.
var DefaultBackend Backend = SecurityBackend{}

// Set stores (or updates) a secret in the Keychain.
func Set(service, account, secret string) error { return DefaultBackend.Set(service, account, secret) }

// Get returns a secret from the Keychain, or ErrNotFound.
func Get(service, account string) (string, error) { return DefaultBackend.Get(service, account) }

// Delete removes a secret from the Keychain, or returns ErrNotFound.
func Delete(service, account string) error { return DefaultBackend.Delete(service, account) }

// SecurityBackend implements Backend with the macOS `security` CLI (no cgo).
//
// Note: add-generic-password receives the secret as a command-line argument
// (-w), so it is briefly visible to local processes listing argv. Errors
// returned from this type never include the secret.
type SecurityBackend struct{}

func runSecurity(args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), keychainTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", "", errors.New("credentials: keychain command timed out")
	}
	return out.String(), errb.String(), err
}

func notFound(err error, stderr string) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 44 {
		return true
	}
	return strings.Contains(stderr, "could not be found")
}

// Set implements Backend using `add-generic-password -U`.
func (SecurityBackend) Set(service, account, secret string) error {
	_, stderr, err := runSecurity("add-generic-password", "-U", "-s", service, "-a", account, "-w", secret)
	if err != nil {
		return errors.New("credentials: keychain set failed: " + strings.TrimSpace(stderr))
	}
	return nil
}

// Get implements Backend using `find-generic-password -w`.
func (SecurityBackend) Get(service, account string) (string, error) {
	out, stderr, err := runSecurity("find-generic-password", "-s", service, "-a", account, "-w")
	if err != nil {
		if notFound(err, stderr) {
			return "", ErrNotFound
		}
		return "", errors.New("credentials: keychain get failed: " + strings.TrimSpace(stderr))
	}
	return strings.TrimRight(out, "\r\n"), nil
}

// Delete implements Backend using `delete-generic-password`.
func (SecurityBackend) Delete(service, account string) error {
	_, stderr, err := runSecurity("delete-generic-password", "-s", service, "-a", account)
	if err != nil {
		if notFound(err, stderr) {
			return ErrNotFound
		}
		return errors.New("credentials: keychain delete failed: " + strings.TrimSpace(stderr))
	}
	return nil
}

// MemoryBackend is an in-memory Backend for tests.
type MemoryBackend struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemoryBackend returns an empty in-memory Backend.
func NewMemoryBackend() *MemoryBackend { return &MemoryBackend{m: map[string]string{}} }

func memKey(s, a string) string { return s + "\x00" + a }

// Set implements Backend.
func (b *MemoryBackend) Set(s, a, v string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[memKey(s, a)] = v
	return nil
}

// Get implements Backend.
func (b *MemoryBackend) Get(s, a string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.m[memKey(s, a)]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// Delete implements Backend.
func (b *MemoryBackend) Delete(s, a string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.m[memKey(s, a)]; !ok {
		return ErrNotFound
	}
	delete(b.m, memKey(s, a))
	return nil
}
