package iosbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Supervisor keeps WDA running on devices whose desired state is on: it starts
// it when the server boots or the device (re)appears, notices a dead runner or
// tunnel through periodic /status checks, and restarts with exponential
// backoff (bounded attempts, then an error state until the device is replugged
// or the user starts the bridge again).
type Supervisor struct {
	Mgr *Manager
	// Start brings WDA up for udid (typically including post-start tuning).
	// Required.
	Start func(ctx context.Context, udid string) (Status, error)
	// Log receives human-readable progress lines (optional).
	Log func(msg string)
	// Blocking, if set, marks start errors that retrying cannot fix (for
	// example a missing simulator WDA build): the device is not auto-started
	// again until it reappears or the user starts it explicitly. ErrNotInstalled
	// and ErrTunnelUnsupported always block.
	Blocking func(err error) bool
	// NotReady, if set, marks start errors meaning the device is not ready yet
	// (for example a simulator that is not booted): not counted as an attempt;
	// the device is treated as absent until discovery reports it again.
	NotReady func(err error) bool

	// Tunables; zero values pick the defaults noted.
	HealthInterval time.Duration // between reconcile passes (5s)
	FailThreshold  int           // consecutive failed checks before restart (3)
	MaxAttempts    int           // automatic starts before giving up (5)
	StableAfter    time.Duration // healthy time that resets the attempt counter (30s)
	Backoff        func(attempt int) time.Duration

	now func() time.Time
	run func(func()) // runs a start attempt; default is a goroutine

	mu   sync.Mutex
	devs map[string]*supDev
}

type supDev struct {
	present   bool
	attempts  int
	fails     int
	nextAt    time.Time
	inflight  bool
	blocked   string // non-empty: do not auto-start (not installed, tunnel root)
	gaveUp    bool
	healthyAt time.Time // when the current healthy streak began
}

// NewSupervisor returns a Supervisor over mgr using start to bring devices up.
func NewSupervisor(mgr *Manager, start func(ctx context.Context, udid string) (Status, error)) *Supervisor {
	return &Supervisor{Mgr: mgr, Start: start}
}

func (s *Supervisor) healthInterval() time.Duration {
	if s.HealthInterval > 0 {
		return s.HealthInterval
	}
	return 5 * time.Second
}

func (s *Supervisor) failThreshold() int {
	if s.FailThreshold > 0 {
		return s.FailThreshold
	}
	return 3
}

func (s *Supervisor) maxAttempts() int {
	if s.MaxAttempts > 0 {
		return s.MaxAttempts
	}
	return 5
}

func (s *Supervisor) stableAfter() time.Duration {
	if s.StableAfter > 0 {
		return s.StableAfter
	}
	return 30 * time.Second
}

func (s *Supervisor) backoff(attempt int) time.Duration {
	if s.Backoff != nil {
		return s.Backoff(attempt)
	}
	d := 2 * time.Second
	for i := 1; i < attempt && d < 30*time.Second; i++ {
		d *= 2
	}
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func (s *Supervisor) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Supervisor) logf(format string, a ...any) {
	if s.Log != nil {
		s.Log(fmt.Sprintf(format, a...))
	}
}

func (s *Supervisor) dev(udid string) *supDev {
	if s.devs == nil {
		s.devs = map[string]*supDev{}
	}
	d := s.devs[udid]
	if d == nil {
		d = &supDev{}
		s.devs[udid] = d
	}
	return d
}

// Seen reports the udids currently present in device discovery. A device that
// is new, or reappears after being absent, gets a clean slate (attempt counter,
// give-up and "not installed" blocks cleared) and is reconciled immediately.
func (s *Supervisor) Seen(ctx context.Context, present []string) {
	in := map[string]bool{}
	for _, u := range present {
		in[u] = true
	}
	var arrived []string
	s.mu.Lock()
	for u, d := range s.devs {
		if d.present && !in[u] {
			d.present = false
			d.fails = 0
		}
	}
	for u := range in {
		d := s.dev(u)
		if !d.present {
			d.present = true
			d.attempts, d.gaveUp, d.blocked, d.fails = 0, false, "", 0
			d.nextAt = time.Time{}
			s.Mgr.setRestarts(u, 0)
			arrived = append(arrived, u)
		}
	}
	s.mu.Unlock()
	for _, u := range arrived {
		if s.Mgr.Desired(u) {
			s.Reconcile(ctx, u)
		}
	}
}

// ClearBlocks resets the give-up/blocked flags for udid (explicit user start).
func (s *Supervisor) ClearBlocks(udid string) {
	s.mu.Lock()
	d := s.dev(udid)
	d.attempts, d.gaveUp, d.blocked, d.fails = 0, false, "", 0
	d.nextAt = time.Time{}
	s.mu.Unlock()
	s.Mgr.setRestarts(udid, 0)
}

// Run reconciles every desired device each HealthInterval until ctx ends.
func (s *Supervisor) Run(ctx context.Context) {
	t := time.NewTicker(s.healthInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

// Tick runs one reconcile pass over all desired devices.
func (s *Supervisor) Tick(ctx context.Context) {
	for _, u := range s.Mgr.DesiredDevices() {
		s.Reconcile(ctx, u)
	}
}

// Reconcile drives one device towards its desired state.
func (s *Supervisor) Reconcile(ctx context.Context, udid string) {
	if !s.Mgr.Desired(udid) {
		return
	}
	st := s.Mgr.Status(udid)
	switch st.State {
	case StateStarting:
		return
	case StateRunning:
		s.healthCheck(ctx, udid, st)
		return
	}
	// Stopped or error.
	s.mu.Lock()
	d := s.dev(udid)
	d.fails = 0
	d.healthyAt = time.Time{}
	switch {
	case !d.present:
		s.mu.Unlock()
		return
	case d.blocked != "" || d.gaveUp || d.inflight:
		s.mu.Unlock()
		return
	case s.clock().Before(d.nextAt):
		s.mu.Unlock()
		return
	}
	if d.attempts >= s.maxAttempts() {
		// Starts that "succeed" but die again before a stable period count too.
		d.gaveUp = true
		n := d.attempts
		s.mu.Unlock()
		msg := fmt.Sprintf("gave up after %d automatic start attempts: %s", n, firstNonEmpty(st.Error, "WDA keeps exiting"))
		s.Mgr.annotate(udid, msg)
		s.logf("WDA on %s: %s", udid, msg)
		return
	}
	d.inflight = true
	d.attempts++
	attempt := d.attempts
	s.mu.Unlock()
	s.Mgr.setRestarts(udid, attempt)

	run := s.run
	if run == nil {
		run = func(f func()) { go f() }
	}
	run(func() { s.attempt(ctx, udid, attempt) })
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Supervisor) attempt(ctx context.Context, udid string, n int) {
	s.logf("WDA auto-start on %s (attempt %d/%d)", udid, n, s.maxAttempts())
	_, err := s.Start(ctx, udid)

	s.mu.Lock()
	d := s.dev(udid)
	d.inflight = false
	d.nextAt = s.clock().Add(s.backoff(n))
	switch {
	case err == nil:
		d.healthyAt = time.Time{}
		s.mu.Unlock()
		return
	case errors.Is(err, ErrBusy):
		d.attempts-- // somebody else is starting it; not our failure
		d.nextAt = time.Time{}
		s.mu.Unlock()
		return
	case s.NotReady != nil && s.NotReady(err):
		d.attempts--
		d.nextAt = time.Time{}
		d.present = false
		s.mu.Unlock()
		s.logf("WDA auto-start on %s deferred: %v", udid, err)
		return
	case errors.Is(err, ErrNotInstalled), errors.Is(err, ErrTunnelUnsupported), s.Blocking != nil && s.Blocking(err):
		d.blocked = err.Error()
		s.mu.Unlock()
		s.logf("WDA auto-start on %s blocked: %v", udid, err)
		return
	}
	gave := n >= s.maxAttempts()
	if gave {
		d.gaveUp = true
	}
	s.mu.Unlock()
	if gave {
		msg := fmt.Sprintf("gave up after %d automatic start attempts: %v", n, err)
		s.Mgr.annotate(udid, msg)
		s.logf("WDA on %s: %s", udid, msg)
	}
}

func (s *Supervisor) healthCheck(ctx context.Context, udid string, st Status) {
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, err := s.Mgr.deps.Probe(hctx, st.WDAURL+"/status")
	cancel()

	s.mu.Lock()
	d := s.dev(udid)
	if err == nil {
		d.fails = 0
		now := s.clock()
		if d.healthyAt.IsZero() {
			d.healthyAt = now
		}
		reset := d.attempts > 0 && now.Sub(d.healthyAt) >= s.stableAfter()
		if reset {
			d.attempts = 0
		}
		s.mu.Unlock()
		if reset {
			s.Mgr.setRestarts(udid, 0)
		}
		return
	}
	d.fails++
	d.healthyAt = time.Time{}
	fails := d.fails
	over := fails >= s.failThreshold()
	if over {
		d.fails = 0
	}
	s.mu.Unlock()
	if !over {
		return
	}
	msg := fmt.Sprintf("WDA stopped answering /status (%d failed checks): %v", fails, err)
	if s.Mgr.markDead(udid, msg) {
		s.logf("WDA on %s: %s; restarting", udid, msg)
		s.Reconcile(ctx, udid)
	}
}
