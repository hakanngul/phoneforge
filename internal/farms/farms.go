// Package farms stores cloud device-farm integration settings (BrowserStack,
// Sauce Labs, AWS Device Farm) and lists the devices each provider offers.
//
// Scope and honesty notes:
//   - Credentials are kept in a Secrets store (macOS Keychain in production),
//     never in the state file and never returned by Status or the HTTP routes.
//   - Real device listing is implemented for BrowserStack and Sauce Labs.
//   - AWS Device Farm listing is a stub (needs SigV4 / the AWS SDK).
//   - Live streaming / remote control of a cloud device is NOT implemented.
package farms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Provider identifies a cloud device farm.
type Provider string

// Supported providers.
const (
	BrowserStack  Provider = "browserstack"
	SauceLabs     Provider = "saucelabs"
	AWSDeviceFarm Provider = "awsdevicefarm"
)

// AllProviders lists every supported provider in stable order.
var AllProviders = []Provider{BrowserStack, SauceLabs, AWSDeviceFarm}

// Secrets is the minimal secret store the Manager needs. It is satisfied by a
// thin adapter over the Keychain-backed credentials store.
type Secrets interface {
	Set(name, value string) error
	Reveal(name string) (string, error)
	Has(name string) bool
	Delete(name string) error
}

// Config is the public, secret-free view of a provider's configuration.
type Config struct {
	Provider       Provider `json:"provider"`
	Enabled        bool     `json:"enabled"`
	HasCredentials bool     `json:"hasCredentials"`
}

// CloudDevice is a device offered by a cloud farm.
type CloudDevice struct {
	Provider  Provider `json:"provider"`
	Name      string   `json:"name"`
	OS        string   `json:"os"`
	OSVersion string   `json:"osVersion"`
	DeviceID  string   `json:"deviceId"`
	Real      bool     `json:"real"`
}

// ErrUnknownProvider is returned for a provider name that is not supported.
var ErrUnknownProvider = errors.New("farms: unknown provider")

// ErrMissingCredentials is returned when a provider has no stored credentials.
var ErrMissingCredentials = errors.New("farms: credentials not configured")

// credFields maps provider -> required credential fields. Each is stored in
// Secrets as "farm:<provider>:<field>".
var credFields = map[Provider][]string{
	BrowserStack:  {"user", "key"},
	SauceLabs:     {"user", "key"},
	AWSDeviceFarm: {"access_key_id", "secret_access_key"},
}

// optionalFields are stored when supplied but not required.
var optionalFields = map[Provider][]string{
	AWSDeviceFarm: {"region"},
}

// fieldAliases normalizes common client spellings to canonical field names.
var fieldAliases = map[string]string{
	"username":          "user",
	"access_key":        "key",
	"accessKey":         "key",
	"accessKeyId":       "access_key_id",
	"secretAccessKey":   "secret_access_key",
	"secret_access_key": "secret_access_key",
}

// Valid reports whether p is a supported provider.
func (p Provider) Valid() bool {
	_, ok := credFields[p]
	return ok
}

// SecretName returns the Secrets name for a provider credential field.
func SecretName(p Provider, field string) string {
	return "farm:" + string(p) + ":" + field
}

// Manager holds farm configuration. It is safe for concurrent use.
type Manager struct {
	sec       Secrets
	statePath string

	mu      sync.Mutex
	enabled map[Provider]bool
}

// NewManager creates a Manager. statePath is a JSON file recording which
// providers are enabled (no secrets); it may be empty to disable persistence.
func NewManager(sec Secrets, statePath string) *Manager {
	m := &Manager{sec: sec, statePath: statePath, enabled: map[Provider]bool{}}
	m.load()
	return m
}

func (m *Manager) load() {
	if m.statePath == "" {
		return
	}
	b, err := os.ReadFile(m.statePath)
	if err != nil {
		return
	}
	var st struct {
		Enabled []Provider `json:"enabled"`
	}
	if json.Unmarshal(b, &st) != nil {
		return
	}
	for _, p := range st.Enabled {
		if p.Valid() {
			m.enabled[p] = true
		}
	}
}

// save persists enabled providers. Caller holds m.mu.
func (m *Manager) save() error {
	if m.statePath == "" {
		return nil
	}
	var st struct {
		Enabled []Provider `json:"enabled"`
	}
	st.Enabled = []Provider{}
	for p, on := range m.enabled {
		if on {
			st.Enabled = append(st.Enabled, p)
		}
	}
	sort.Slice(st.Enabled, func(i, j int) bool { return st.Enabled[i] < st.Enabled[j] })
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o700); err != nil {
		return err
	}
	tmp := m.statePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.statePath)
}

// Configure stores the credentials for provider in Secrets and enables it.
// Accepted fields: browserstack/saucelabs "user","key" (aliases "username",
// "access_key"/"accessKey"); awsdevicefarm "access_key_id",
// "secret_access_key" and optional "region".
func (m *Manager) Configure(provider Provider, creds map[string]string) error {
	if !provider.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownProvider, provider)
	}
	norm := map[string]string{}
	for k, v := range creds {
		if a, ok := fieldAliases[k]; ok {
			k = a
		}
		norm[k] = v
	}
	for _, f := range credFields[provider] {
		if norm[f] == "" {
			return fmt.Errorf("farms: %s requires credential field %q", provider, f)
		}
	}
	fields := append(append([]string{}, credFields[provider]...), optionalFields[provider]...)
	for _, f := range fields {
		v := norm[f]
		if v == "" {
			continue
		}
		if err := m.sec.Set(SecretName(provider, f), v); err != nil {
			return fmt.Errorf("farms: store %s credential: %w", provider, err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled[provider] = true
	return m.save()
}

// Disable marks the provider disabled and deletes its stored credentials.
func (m *Manager) Disable(provider Provider) error {
	if !provider.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownProvider, provider)
	}
	fields := append(append([]string{}, credFields[provider]...), optionalFields[provider]...)
	for _, f := range fields {
		n := SecretName(provider, f)
		if m.sec.Has(n) {
			if err := m.sec.Delete(n); err != nil {
				return fmt.Errorf("farms: delete %s credential: %w", provider, err)
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.enabled, provider)
	return m.save()
}

func (m *Manager) hasCreds(p Provider) bool {
	for _, f := range credFields[p] {
		if !m.sec.Has(SecretName(p, f)) {
			return false
		}
	}
	return true
}

// Status returns the configuration of every provider. It never contains
// secret values.
func (m *Manager) Status() []Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Config, 0, len(AllProviders))
	for _, p := range AllProviders {
		out = append(out, Config{Provider: p, Enabled: m.enabled[p], HasCredentials: m.hasCreds(p)})
	}
	return out
}

// reveal fetches a stored credential, returning ErrMissingCredentials if absent.
func (m *Manager) reveal(p Provider, field string) (string, error) {
	v, err := m.sec.Reveal(SecretName(p, field))
	if err != nil || v == "" {
		return "", fmt.Errorf("%w for %s (set it via PUT /%s)", ErrMissingCredentials, p, p)
	}
	return v, nil
}

// ListDevices lists the devices the provider offers to the configured account.
func (m *Manager) ListDevices(ctx context.Context, provider Provider) ([]CloudDevice, error) {
	switch provider {
	case BrowserStack:
		return m.listBrowserStack(ctx)
	case SauceLabs:
		return m.listSauceLabs(ctx)
	case AWSDeviceFarm:
		return m.listAWSDeviceFarm(ctx)
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, provider)
}
