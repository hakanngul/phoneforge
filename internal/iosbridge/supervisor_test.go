package iosbridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newSup(r *rig) (*Supervisor, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s := NewSupervisor(r.m, func(ctx context.Context, u string) (Status, error) { return r.m.Start(ctx, u, Options{}) })
	s.now = clk.now
	s.run = func(f func()) { f() }
	s.Backoff = func(n int) time.Duration { return time.Duration(n) * time.Second }
	s.StableAfter = 10 * time.Second
	return s, clk
}

func waitState(t *testing.T, m *Manager, want State) Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := m.Status(udid)
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s (%s), want %s", st.State, st.Error, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDesiredStatePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "iosbridge-state.json")
	r := newRig(fakeInst{ok: true})
	r.m.SetStatePath(path)
	if r.m.Desired(udid) {
		t.Fatal("desired before set")
	}
	if err := r.m.SetDesired(udid, true); err != nil {
		t.Fatal(err)
	}
	if !r.m.Status(udid).Desired {
		t.Fatal("status should report desired")
	}
	// A fresh manager sees the persisted flag.
	r2 := newRig(fakeInst{ok: true})
	r2.m.SetStatePath(path)
	if got := r2.m.DesiredDevices(); len(got) != 1 || got[0] != udid {
		t.Fatalf("DesiredDevices = %v", got)
	}
	// Server-shutdown Stop/StopAll keep it; an explicit UserStop clears it.
	if _, err := r2.m.Start(context.Background(), udid, Options{}); err != nil {
		t.Fatal(err)
	}
	r2.m.StopAll()
	if !r2.m.Desired(udid) {
		t.Fatal("StopAll must not clear the desired state")
	}
	if err := r2.m.UserStop(udid); err != nil {
		t.Fatal(err)
	}
	r3 := newRig(fakeInst{ok: true})
	r3.m.SetStatePath(path)
	if r3.m.Desired(udid) {
		t.Fatal("UserStop must clear the persisted state")
	}
	b, _ := os.ReadFile(path)
	if len(b) == 0 {
		t.Fatal("state file should exist")
	}
}

func TestDesiredStateCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	_ = os.WriteFile(path, []byte("{not json"), 0o600)
	r := newRig(fakeInst{ok: true})
	r.m.SetStatePath(path)
	if len(r.m.DesiredDevices()) != 0 {
		t.Fatal("corrupt file should load as empty")
	}
	if err := r.m.SetDesired(udid, true); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorAutoStartsOnAppearance(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	_ = r.m.SetDesired(udid, true)
	s, _ := newSup(r)
	ctx := context.Background()

	s.Seen(ctx, nil) // not present yet: nothing happens
	s.Tick(ctx)
	if r.l.runs.Load() != 0 {
		t.Fatal("must not start an absent device")
	}
	s.Seen(ctx, []string{udid})
	st := waitState(t, r.m, StateRunning)
	if st.Restarts != 1 || !st.Desired {
		t.Fatalf("status %+v", st)
	}
	// Not desired: ignored.
	_ = r.m.SetDesired(udid, false)
	_ = r.m.Stop(udid)
	s.Seen(ctx, nil)
	s.Seen(ctx, []string{udid})
	s.Tick(ctx)
	if r.m.Status(udid).State != StateStopped {
		t.Fatal("desired=off device must stay stopped")
	}
}

func TestSupervisorNotInstalledDoesNotLoop(t *testing.T) {
	r := newRig(fakeInst{ok: false})
	_ = r.m.SetDesired(udid, true)
	s, clk := newSup(r)
	ctx := context.Background()
	s.Seen(ctx, []string{udid})
	for i := 0; i < 10; i++ {
		clk.add(time.Minute)
		s.Tick(ctx)
	}
	st := r.m.Status(udid)
	if st.State != StateError || st.Error == "" {
		t.Fatalf("want error state with message, got %+v", st)
	}
	if st.Restarts != 1 {
		t.Fatalf("restarts = %d, want exactly one attempt", st.Restarts)
	}
	// Replug clears the block (the user may have installed the runner).
	s.Seen(ctx, nil)
	s.Seen(ctx, []string{udid})
	if got := r.m.Status(udid).Restarts; got != 1 {
		t.Fatalf("restarts after replug = %d, want a fresh single attempt", got)
	}
}

func TestSupervisorRestartsAfterRunnerExit(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	_ = r.m.SetDesired(udid, true)
	s, clk := newSup(r)
	ctx := context.Background()
	s.Seen(ctx, []string{udid})
	waitState(t, r.m, StateRunning)

	r.l.exit <- errors.New("crash")
	waitState(t, r.m, StateError)
	s.Tick(ctx) // inside the backoff window: no restart yet
	if r.m.Status(udid).State != StateError {
		t.Fatal("restart must wait for the backoff")
	}
	clk.add(2 * time.Second)
	s.Tick(ctx)
	st := waitState(t, r.m, StateRunning)
	if st.Restarts != 2 {
		t.Fatalf("restarts = %d, want 2", st.Restarts)
	}
	if r.l.runs.Load() != 2 {
		t.Fatalf("runs = %d", r.l.runs.Load())
	}
}

func TestSupervisorHealthCheckRestartsDeadWDA(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	_ = r.m.SetDesired(udid, true)
	s, clk := newSup(r)
	ctx := context.Background()
	s.Seen(ctx, []string{udid})
	waitState(t, r.m, StateRunning)

	r.up.Store(false)
	s.Tick(ctx)
	s.Tick(ctx)
	if r.m.Status(udid).State != StateRunning {
		t.Fatal("two failures are below the threshold")
	}
	s.Tick(ctx) // third consecutive failure
	st := r.m.Status(udid)
	if st.State != StateError {
		t.Fatalf("state = %s, want error after 3 failed checks", st.State)
	}
	r.up.Store(true)
	clk.add(5 * time.Second)
	s.Tick(ctx)
	waitState(t, r.m, StateRunning)

	// A single blip resets the counter.
	r.up.Store(false)
	s.Tick(ctx)
	r.up.Store(true)
	s.Tick(ctx)
	r.up.Store(false)
	s.Tick(ctx)
	s.Tick(ctx)
	if r.m.Status(udid).State != StateRunning {
		t.Fatal("non-consecutive failures must not restart")
	}
}

func TestSupervisorGivesUpAfterMaxAttempts(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	_ = r.m.SetDesired(udid, true)
	s, clk := newSup(r)
	var calls int
	s.Start = func(ctx context.Context, u string) (Status, error) {
		calls++
		return Status{}, errors.New("tunnel: device gone")
	}
	s.MaxAttempts = 5
	ctx := context.Background()
	s.Seen(ctx, []string{udid})
	for i := 0; i < 20; i++ {
		clk.add(time.Minute)
		s.Tick(ctx)
	}
	if calls != 5 {
		t.Fatalf("start calls = %d, want 5", calls)
	}
	st := r.m.Status(udid)
	if st.Restarts != 5 {
		t.Fatalf("restarts = %d", st.Restarts)
	}
	_ = st
}

func TestSupervisorGiveUpRecordsError(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	r.tun.err = errors.New("no device")
	_ = r.m.SetDesired(udid, true)
	s, clk := newSup(r)
	s.MaxAttempts = 3
	ctx := context.Background()
	s.Seen(ctx, []string{udid})
	for i := 0; i < 10; i++ {
		clk.add(time.Minute)
		s.Tick(ctx)
	}
	st := r.m.Status(udid)
	if st.State != StateError || st.Restarts != 3 {
		t.Fatalf("status %+v", st)
	}
	if want := "gave up after 3 automatic start attempts"; len(st.Error) < len(want) || st.Error[:len(want)] != want {
		t.Fatalf("error = %q", st.Error)
	}
	// Replug starts over.
	r.tun.err = nil
	s.Seen(ctx, nil)
	s.Seen(ctx, []string{udid})
	waitState(t, r.m, StateRunning)
}

func TestSupervisorStableRunResetsAttempts(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	_ = r.m.SetDesired(udid, true)
	s, clk := newSup(r)
	ctx := context.Background()
	s.Seen(ctx, []string{udid})
	waitState(t, r.m, StateRunning)
	if r.m.Status(udid).Restarts != 1 {
		t.Fatal("expected one attempt")
	}
	s.Tick(ctx) // healthy streak begins
	clk.add(11 * time.Second)
	s.Tick(ctx)
	if got := r.m.Status(udid).Restarts; got != 0 {
		t.Fatalf("restarts = %d, want reset to 0 after a stable period", got)
	}
}

func TestStopAsksWDAToShutDown(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	st, err := r.m.Start(context.Background(), udid, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.m.Stop(udid); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.shut) != 1 || r.shut[0] != st.WDAURL {
		t.Fatalf("Shutdown calls = %v, want [%s]", r.shut, st.WDAURL)
	}
}

func TestSupervisorGivesUpWhenRunnerKeepsDying(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	_ = r.m.SetDesired(udid, true)
	s, clk := newSup(r)
	s.MaxAttempts = 3
	ctx := context.Background()
	s.Seen(ctx, []string{udid})
	for i := 0; i < 8; i++ {
		deadline := time.Now().Add(2 * time.Second)
		for r.m.Status(udid).State == StateStarting && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
		if r.m.Status(udid).State == StateRunning {
			r.l.exit <- errors.New("lost connection to testmanagerd")
			waitState(t, r.m, StateError)
		}
		clk.add(time.Minute)
		s.Tick(ctx)
	}
	if got := r.l.runs.Load(); got != 3 {
		t.Fatalf("runner launched %d times, want 3", got)
	}
	st := r.m.Status(udid)
	if st.State != StateError || len(st.Error) < 12 || st.Error[:12] != "gave up afte" {
		t.Fatalf("status %+v", st)
	}
}

var errCustomBlock = errors.New("custom: cannot fix by retrying")

func TestSupervisorBlockingHookDoesNotLoop(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	_ = r.m.SetDesired(udid, true)
	var calls int
	s, clk := newSup(r)
	s.Start = func(ctx context.Context, u string) (Status, error) { calls++; return Status{}, errCustomBlock }
	s.Blocking = func(err error) bool { return errors.Is(err, errCustomBlock) }
	ctx := context.Background()
	s.Seen(ctx, []string{udid})
	for i := 0; i < 10; i++ {
		clk.add(time.Minute)
		s.Tick(ctx)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want exactly 1", calls)
	}
	// Reappearance clears the block.
	s.Seen(ctx, nil)
	s.Seen(ctx, []string{udid})
	if calls != 2 {
		t.Fatalf("calls after reappear = %d, want 2", calls)
	}
}

func TestSupervisorNotReadyHookDoesNotCountAttempts(t *testing.T) {
	r := newRig(fakeInst{ok: true})
	_ = r.m.SetDesired(udid, true)
	errNotBooted := errors.New("not booted")
	var calls int
	booted := false
	s, clk := newSup(r)
	s.MaxAttempts = 2
	s.Start = func(ctx context.Context, u string) (Status, error) {
		calls++
		if !booted {
			return Status{}, errNotBooted
		}
		return r.m.Start(ctx, u, Options{})
	}
	s.NotReady = func(err error) bool { return errors.Is(err, errNotBooted) }
	ctx := context.Background()
	s.Seen(ctx, []string{udid})
	for i := 0; i < 6; i++ { // would exceed MaxAttempts if counted
		clk.add(time.Minute)
		s.Tick(ctx) // device treated as absent: no further attempts
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 until the device is seen again", calls)
	}
	booted = true
	s.Seen(ctx, []string{udid}) // sim shows up booted
	waitState(t, r.m, StateRunning)
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}
