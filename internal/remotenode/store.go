// Package remotenode lets a PhoneForge node control devices that are attached
// to other PhoneForge nodes, addressed by URL (LAN, Tailscale, etc.).
package remotenode

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Node is a remote PhoneForge node. Addr is the base URL without /api/v1.
type Node struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Addr    string    `json:"addr"`
	Token   string    `json:"token,omitempty"`
	AddedAt time.Time `json:"addedAt"`
}

// ErrNodeNotFound is returned when a node id is unknown.
var ErrNodeNotFound = errors.New("node not found")

// Store persists nodes as JSON on disk. It is safe for concurrent use.
type Store struct {
	mu    sync.RWMutex
	path  string
	nodes []Node
}

// New opens (or lazily creates) a store backed by the JSON file at path.
func New(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.nodes); err != nil {
		return nil, fmt.Errorf("remotenode: parse %s: %w", path, err)
	}
	return s, nil
}

// List returns a copy of all nodes.
func (s *Store) List() []Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Node(nil), s.nodes...)
}

// Get returns the node with the given id.
func (s *Store) Get(id string) (Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// normalizeAddr validates addr and strips trailing slashes and /api/v1.
func normalizeAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", errors.New("addr is required")
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil {
		return "", fmt.Errorf("invalid addr: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("addr must be an http(s) URL with a host")
	}
	p := strings.TrimRight(u.Path, "/")
	p = strings.TrimSuffix(p, "/api/v1")
	return u.Scheme + "://" + u.Host + strings.TrimRight(p, "/"), nil
}

// Add registers a node. Addr is validated and deduplicated; adding an
// existing addr returns an error.
func (s *Store) Add(name, addr, token string) (Node, error) {
	norm, err := normalizeAddr(addr)
	if err != nil {
		return Node{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.nodes {
		if strings.EqualFold(n.Addr, norm) {
			return Node{}, fmt.Errorf("node with addr %s already exists", norm)
		}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		u, _ := url.Parse(norm)
		name = u.Host
	}
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Node{}, err
	}
	n := Node{ID: hex.EncodeToString(raw[:]), Name: name, Addr: norm, Token: token, AddedAt: time.Now().UTC()}
	s.nodes = append(s.nodes, n)
	if err := s.saveLocked(); err != nil {
		s.nodes = s.nodes[:len(s.nodes)-1]
		return Node{}, err
	}
	return n, nil
}

// Remove deletes a node by id.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, n := range s.nodes {
		if n.ID == id {
			prev := s.nodes
			s.nodes = append(append([]Node(nil), prev[:i]...), prev[i+1:]...)
			if err := s.saveLocked(); err != nil {
				s.nodes = prev
				return err
			}
			return nil
		}
	}
	return ErrNodeNotFound
}

// saveLocked writes the store atomically; caller holds s.mu.
func (s *Store) saveLocked() error {
	nodes := s.nodes
	if nodes == nil {
		nodes = []Node{}
	}
	b, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
