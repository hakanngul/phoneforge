package webauto

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// Session manages devtools forwards and attached pages for one device serial.
type Session struct {
	serial string
	run    Runner

	mu       sync.Mutex
	forwards map[string]int // socket -> local port
	pages    map[string]*Page
}

// NewSession creates a Session for serial using run for adb calls.
func NewSession(serial string, run Runner) *Session {
	return &Session{serial: serial, run: run, forwards: map[string]int{}, pages: map[string]*Page{}}
}

func (s *Session) adb(ctx context.Context, args ...string) ([]byte, error) {
	return s.run(ctx, append([]string{"-s", s.serial}, args...)...)
}

// Sockets lists devtools abstract sockets on the device.
func (s *Session) Sockets(ctx context.Context) ([]string, error) {
	out, err := s.adb(ctx, "shell", "cat", "/proc/net/unix")
	if err != nil {
		return nil, err
	}
	return ParseUnixSockets(out), nil
}

func (s *Session) forward(ctx context.Context, socket string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.forwards[socket]; ok {
		return p, nil
	}
	port, err := freePort()
	if err != nil {
		return 0, err
	}
	if _, err := s.adb(ctx, "forward", "tcp:"+itoa(port), "localabstract:"+socket); err != nil {
		return 0, err
	}
	s.forwards[socket] = port
	return port, nil
}

// Targets lists targets across all devtools sockets.
func (s *Session) Targets(ctx context.Context) ([]Target, error) {
	socks, err := s.Sockets(ctx)
	if err != nil {
		return nil, err
	}
	var all []Target
	for _, sock := range socks {
		port, err := s.forward(ctx, sock)
		if err != nil {
			continue
		}
		body, err := httpDo(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
		if err != nil {
			continue
		}
		ts, err := ParseTargets(body)
		if err != nil {
			continue
		}
		for _, t := range ts {
			t.Socket, t.port = sock, port
			all = append(all, t)
		}
	}
	return all, nil
}

// DefaultTarget returns the first "page" target.
func DefaultTarget(ts []Target) (Target, bool) {
	for _, t := range ts {
		if t.Type == "page" {
			return t, true
		}
	}
	return Target{}, false
}

// rewriteWS points a device-reported ws URL at the local forwarded port.
func rewriteWS(raw string, port int) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.Scheme = "ws"
	u.Host = fmt.Sprintf("127.0.0.1:%d", port)
	return u.String(), nil
}

// Attach connects to targetID (empty = first page target). Pages are cached.
func (s *Session) Attach(ctx context.Context, targetID string) (*Page, error) {
	ts, err := s.Targets(ctx)
	if err != nil {
		return nil, err
	}
	var t Target
	if targetID == "" {
		var ok bool
		if t, ok = DefaultTarget(ts); !ok {
			return nil, errors.New("no page targets found (is Chrome/WebView debuggable and open?)")
		}
	} else {
		found := false
		for _, c := range ts {
			if c.ID == targetID {
				t, found = c, true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("target %q not found", targetID)
		}
	}
	s.mu.Lock()
	if p, ok := s.pages[t.ID]; ok && !p.Closed() {
		s.mu.Unlock()
		return p, nil
	}
	s.mu.Unlock()
	if t.WebSocketDebuggerURL == "" {
		return nil, errors.New("target has no webSocketDebuggerUrl (another debugger attached?)")
	}
	wsURL, err := rewriteWS(t.WebSocketDebuggerURL, t.port)
	if err != nil {
		return nil, err
	}
	c, err := Dial(ctx, wsURL)
	if err != nil {
		return nil, err
	}
	p := NewPage(t, c)
	s.mu.Lock()
	s.pages[t.ID] = p
	s.mu.Unlock()
	return p, nil
}

// NewTab opens a new tab at u via /json/new (PUT) on the chrome socket.
func (s *Session) NewTab(ctx context.Context, u string) (Target, error) {
	socks, err := s.Sockets(ctx)
	if err != nil {
		return Target{}, err
	}
	sock := ""
	for _, c := range socks {
		if strings.HasSuffix(c, "chrome_devtools_remote") {
			sock = c
			break
		}
	}
	if sock == "" {
		return Target{}, errors.New("no chrome devtools socket found")
	}
	port, err := s.forward(ctx, sock)
	if err != nil {
		return Target{}, err
	}
	body, err := httpDo(ctx, "PUT", fmt.Sprintf("http://127.0.0.1:%d/json/new?%s", port, u))
	if err != nil {
		return Target{}, err
	}
	var t Target
	ts, err := ParseTargets([]byte("[" + string(body) + "]"))
	if err != nil || len(ts) == 0 {
		return Target{}, fmt.Errorf("parse /json/new: %v", err)
	}
	t = ts[0]
	t.Socket, t.port = sock, port
	return t, nil
}

// Close drops pages and removes adb forwards.
func (s *Session) Close(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.pages {
		_ = p.Close()
		delete(s.pages, id)
	}
	for sock, port := range s.forwards {
		_, _ = s.adb(ctx, "forward", "--remove", "tcp:"+itoa(port))
		delete(s.forwards, sock)
	}
}

// Manager caches one Session per device serial.
type Manager struct {
	run Runner
	mu  sync.Mutex
	ss  map[string]*Session
}

// NewManager creates a Manager using the adb binary at adbPath.
func NewManager(adbPath string) *Manager { return NewManagerWithRunner(ExecRunner(adbPath)) }

// NewManagerWithRunner creates a Manager with a custom adb Runner.
func NewManagerWithRunner(run Runner) *Manager {
	return &Manager{run: run, ss: map[string]*Session{}}
}

// Session returns (creating if needed) the session for serial.
func (m *Manager) Session(serial string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.ss[serial]
	if !ok {
		s = NewSession(serial, m.run)
		m.ss[serial] = s
	}
	return s
}

// Close closes and forgets the session for serial.
func (m *Manager) Close(ctx context.Context, serial string) {
	m.mu.Lock()
	s := m.ss[serial]
	delete(m.ss, serial)
	m.mu.Unlock()
	if s != nil {
		s.Close(ctx)
	}
}

// CloseAll closes every session.
func (m *Manager) CloseAll(ctx context.Context) {
	m.mu.Lock()
	all := m.ss
	m.ss = map[string]*Session{}
	m.mu.Unlock()
	for _, s := range all {
		s.Close(ctx)
	}
}
