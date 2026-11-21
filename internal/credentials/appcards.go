package credentials

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// MaskedValue replaces secret field values in anything returned to callers.
const MaskedValue = "***"

// Field describes one named field of an App Card.
type Field struct {
	Name   string `json:"name"`
	Secret bool   `json:"secret"`
}

// AppCard is a per-app login profile. Values holds NON-secret values only; the
// values of Secret fields live in the SecretStore (Keychain) under the name
// "appcard:<id>:<field>". Cards returned by List/Get carry MaskedValue for
// secret fields.
type AppCard struct {
	ID       string            `json:"id"`
	BundleID string            `json:"bundleId"`
	Label    string            `json:"label"`
	Fields   []Field           `json:"fields"`
	Values   map[string]string `json:"values,omitempty"`
}

// CardStore persists App Card metadata (never secret values) to a JSON file.
type CardStore struct {
	mu    sync.Mutex
	path  string
	sec   *SecretStore
	cards map[string]AppCard
}

// SecretName is the SecretStore name of a card's secret field.
func SecretName(cardID, field string) string { return "appcard:" + cardID + ":" + field }

// NewCardStore opens (or creates) a CardStore persisted at path.
func NewCardStore(path string, sec *SecretStore) (*CardStore, error) {
	cs := &CardStore{path: path, sec: sec, cards: map[string]AppCard{}}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		var list []AppCard
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("credentials: bad app card file: %w", err)
		}
		for _, c := range list {
			cs.cards[c.ID] = c
		}
	}
	return cs, nil
}

func (cs *CardStore) saveLocked() error {
	list := make([]AppCard, 0, len(cs.cards))
	for _, c := range cs.cards {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return writeJSONAtomic(cs.path, list)
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func validID(id string) bool {
	return id != "" && !strings.ContainsAny(id, ":/ \t\r\n")
}

func masked(c AppCard) AppCard {
	out := c
	out.Fields = append([]Field(nil), c.Fields...)
	out.Values = map[string]string{}
	for k, v := range c.Values {
		out.Values[k] = v
	}
	for _, f := range c.Fields {
		if f.Secret {
			out.Values[f.Name] = MaskedValue
		}
	}
	return out
}

// Add validates and stores a card. secretValues maps secret field names to
// their values (stored in the Keychain); card.Values may contain non-secret
// values only. An empty card.ID is generated. The stored/returned card is masked.
func (cs *CardStore) Add(card AppCard, secretValues map[string]string) (AppCard, error) {
	if card.BundleID == "" {
		return AppCard{}, errors.New("credentials: bundleId required")
	}
	if card.ID == "" {
		id, err := newID()
		if err != nil {
			return AppCard{}, err
		}
		card.ID = id
	} else if !validID(card.ID) {
		return AppCard{}, errors.New("credentials: invalid card id")
	}
	secretFields := map[string]bool{}
	seen := map[string]bool{}
	for _, f := range card.Fields {
		if f.Name == "" || strings.Contains(f.Name, ":") || seen[f.Name] {
			return AppCard{}, errors.New("credentials: invalid or duplicate field name")
		}
		seen[f.Name] = true
		if f.Secret {
			secretFields[f.Name] = true
		}
	}
	for k := range secretValues {
		if !secretFields[k] {
			return AppCard{}, fmt.Errorf("credentials: %q is not a secret field", k)
		}
	}
	plain := map[string]string{}
	for k, v := range card.Values {
		if secretFields[k] {
			continue // never persist a secret in metadata
		}
		plain[k] = v
	}
	card.Values = plain

	cs.mu.Lock()
	defer cs.mu.Unlock()
	if _, exists := cs.cards[card.ID]; exists {
		return AppCard{}, errors.New("credentials: card id already exists")
	}
	var written []string
	rollback := func() {
		for _, n := range written {
			_ = cs.sec.Delete(n)
		}
	}
	for k, v := range secretValues {
		n := SecretName(card.ID, k)
		if err := cs.sec.Set(n, v); err != nil {
			rollback()
			return AppCard{}, err
		}
		written = append(written, n)
	}
	cs.cards[card.ID] = card
	if err := cs.saveLocked(); err != nil {
		delete(cs.cards, card.ID)
		rollback()
		return AppCard{}, err
	}
	return masked(card), nil
}

// List returns all cards sorted by ID, with secret values masked.
func (cs *CardStore) List() []AppCard {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	out := make([]AppCard, 0, len(cs.cards))
	for _, c := range cs.cards {
		out = append(out, masked(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get returns one card (masked).
func (cs *CardStore) Get(id string) (AppCard, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	c, ok := cs.cards[id]
	if !ok {
		return AppCard{}, ErrNotFound
	}
	return masked(c), nil
}

// Delete removes a card and its secret values from the Keychain.
func (cs *CardStore) Delete(id string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	c, ok := cs.cards[id]
	if !ok {
		return ErrNotFound
	}
	for _, f := range c.Fields {
		if f.Secret {
			if err := cs.sec.Delete(SecretName(id, f.Name)); err != nil {
				return err
			}
		}
	}
	delete(cs.cards, id)
	return cs.saveLocked()
}

// Reveal returns the value of a card field: the Keychain value for secret
// fields, the stored value otherwise. Server-side use only; never expose it
// over HTTP.
func (cs *CardStore) Reveal(id, field string) (string, error) {
	cs.mu.Lock()
	c, ok := cs.cards[id]
	cs.mu.Unlock()
	if !ok {
		return "", ErrNotFound
	}
	for _, f := range c.Fields {
		if f.Name != field {
			continue
		}
		if f.Secret {
			return cs.sec.Reveal(SecretName(id, field))
		}
		return c.Values[field], nil
	}
	return "", ErrNotFound
}
