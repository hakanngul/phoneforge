package iossim

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios"

	"pocketlab/internal/iosbridge"
)

type fakeRunner struct {
	runs atomic.Int32
	up   atomic.Bool
	hold time.Duration // delay before WDA "answers"
}

func (f *fakeRunner) Run(ctx context.Context, _ ios.DeviceEntry, _ iosbridge.Options, _ io.Writer) error {
	f.runs.Add(1)
	go func() {
		select {
		case <-time.After(f.hold):
			f.up.Store(true)
		case <-ctx.Done():
		}
	}()
	<-ctx.Done()
	f.up.Store(false)
	return nil
}

func fakeControl(t *testing.T, built bool, hold time.Duration) (*Control, *fakeRunner) {
	t.Helper()
	home := t.TempDir()
	if built {
		dir := filepath.Join(home, "build-sim", "Build", "Products")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "WDA_iphonesimulator27.2-arm64.xctestrun"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := New(home)
	c.Run = func(ctx context.Context, args ...string) ([]byte, error) { return []byte(listJSON), nil }
	fr := &fakeRunner{hold: hold}
	c.Mgr = iosbridge.NewManagerWithDeps("", iosbridge.Deps{
		Tunnel: simTunnel{}, Launcher: fr, Forwarder: identityForwarder{}, Installer: alwaysInstalled{},
		Probe: func(context.Context, string) (string, error) {
			if !fr.up.Load() {
				return "", errors.New("down")
			}
			return "com.facebook.WebDriverAgentRunner", nil
		},
		Shutdown:       func(context.Context, string) {},
		PollInterval:   5 * time.Millisecond,
		PreProbeWindow: 20 * time.Millisecond,
	})
	return c, fr
}

func TestEnsureWDAStartsLazilyAndPersistsDesired(t *testing.T) {
	c, fr := fakeControl(t, true, 100*time.Millisecond)
	t.Cleanup(c.StopAll)
	ctx := context.Background()
	if err := c.EnsureWDA(ctx, "A"); err != nil {
		t.Fatal(err)
	}
	if c.Mgr.Status("A").State != iosbridge.StateRunning || !c.Mgr.Desired("A") {
		t.Fatalf("status %+v", c.Mgr.Status("A"))
	}
	// Already running: no second start.
	if err := c.EnsureWDA(ctx, "A"); err != nil || fr.runs.Load() != 1 {
		t.Fatalf("err=%v runs=%d", err, fr.runs.Load())
	}
}

func TestEnsureWDAConcurrentCallersShareOneStart(t *testing.T) {
	c, fr := fakeControl(t, true, 150*time.Millisecond)
	t.Cleanup(c.StopAll)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() { errs <- c.EnsureWDA(context.Background(), "A") }()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if fr.runs.Load() != 1 {
		t.Fatalf("runs = %d, want 1", fr.runs.Load())
	}
}

func TestEnsureWDADisabledByEnv(t *testing.T) {
	t.Setenv("PL_SIM_AUTOWDA", "0")
	c, fr := fakeControl(t, true, 0)
	if err := c.EnsureWDA(context.Background(), "A"); err != nil || fr.runs.Load() != 0 {
		t.Fatalf("err=%v runs=%d", err, fr.runs.Load())
	}
}

func TestEnsureWDANotBuilt(t *testing.T) {
	c, fr := fakeControl(t, false, 0)
	if err := c.EnsureWDA(context.Background(), "A"); !errors.Is(err, ErrNotBuilt) || fr.runs.Load() != 0 {
		t.Fatalf("err=%v runs=%d", err, fr.runs.Load())
	}
	if c.Mgr.Desired("A") {
		t.Fatal("must not record desired when the build is missing")
	}
}

func TestEnsureWDATimeoutReturnsStarting(t *testing.T) {
	c, _ := fakeControl(t, true, 5*time.Second)
	t.Cleanup(c.StopAll)
	c.autoWait = 200 * time.Millisecond
	if err := c.EnsureWDA(context.Background(), "A"); !errors.Is(err, ErrWDAStarting) {
		t.Fatalf("err = %v, want ErrWDAStarting", err)
	}
}

func TestUserStopClearsDesired(t *testing.T) {
	c, _ := fakeControl(t, true, 0)
	t.Cleanup(c.StopAll)
	if _, err := c.UserStart(context.Background(), "A", iosbridge.Options{}); err != nil {
		t.Fatal(err)
	}
	if !c.Mgr.Desired("A") {
		t.Fatal("desired after start")
	}
	st := c.UserStop("A")
	if c.Mgr.Desired("A") || st.State != iosbridge.StateStopped {
		t.Fatalf("status %+v", st)
	}
}

func TestSupervisorRestoresBootedSimAndSkipsMissingBuild(t *testing.T) {
	c, fr := fakeControl(t, true, 0)
	t.Cleanup(c.StopAll)
	_ = c.Mgr.SetDesired("A", true)
	sup := c.Supervise(nil)
	sup.Seen(context.Background(), []string{"A"})
	deadline := time.Now().Add(3 * time.Second)
	for c.Mgr.Status("A").State != iosbridge.StateRunning {
		if time.Now().After(deadline) {
			t.Fatalf("not restored: %+v", c.Mgr.Status("A"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fr.runs.Load() != 1 {
		t.Fatalf("runs = %d", fr.runs.Load())
	}

	// Missing build: one blocked attempt, then no loop.
	c2, fr2 := fakeControl(t, false, 0)
	_ = c2.Mgr.SetDesired("A", true)
	sup2 := c2.Supervise(nil)
	sup2.Seen(context.Background(), []string{"A"})
	for i := 0; i < 5; i++ {
		time.Sleep(20 * time.Millisecond)
		sup2.Tick(context.Background())
	}
	if fr2.runs.Load() != 0 {
		t.Fatalf("launched without a build: %d", fr2.runs.Load())
	}
}
