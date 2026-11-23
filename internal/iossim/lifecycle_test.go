package iossim

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"pocketlab/internal/iosbridge"
	"pocketlab/internal/iossim/slim"
)

type lifeFake struct {
	mu    sync.Mutex
	calls []string
	devs  map[string]*struct{ name, state string }
	order []string
	n     int
}

func newLifeFake() *lifeFake {
	f := &lifeFake{devs: map[string]*struct{ name, state string }{}}
	f.add("11111111-1111-1111-1111-111111111111", "Base", "Shutdown")
	return f
}

func (f *lifeFake) add(udid, name, state string) {
	f.devs[udid] = &struct{ name, state string }{name, state}
	f.order = append(f.order, udid)
}

func (f *lifeFake) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "list":
		switch args[1] {
		case "devicetypes":
			return []byte(`{"devicetypes":[{"identifier":"com.apple.CoreSimulator.SimDeviceType.iPhone-17","name":"iPhone 17","productFamily":"iPhone"}]}`), nil
		case "runtimes":
			return []byte(`{"runtimes":[{"identifier":"com.apple.CoreSimulator.SimRuntime.iOS-26-0","name":"iOS 26.0","platform":"iOS","version":"26.0","isAvailable":true},{"identifier":"com.apple.CoreSimulator.SimRuntime.iOS-27-2","name":"iOS 27.2","platform":"iOS","version":"27.2","isAvailable":true}]}`), nil
		}
		var sb strings.Builder
		sb.WriteString(`{"devices":{"com.apple.CoreSimulator.SimRuntime.iOS-27-2":[`)
		first := true
		for _, u := range f.order {
			d, ok := f.devs[u]
			if !ok {
				continue
			}
			if !first {
				sb.WriteString(",")
			}
			first = false
			fmt.Fprintf(&sb, `{"udid":%q,"name":%q,"state":%q,"isAvailable":true}`, u, d.name, d.state)
		}
		sb.WriteString(`]}}`)
		return []byte(sb.String()), nil
	case "create", "clone":
		f.n++
		u := fmt.Sprintf("2222222%d-2222-2222-2222-222222222222", f.n)
		name := args[1]
		if args[0] == "clone" {
			name = args[2]
		}
		f.add(u, name, "Shutdown")
		return []byte(u + "\n"), nil
	case "boot":
		f.devs[args[1]].state = "Booted"
	case "shutdown":
		f.devs[args[1]].state = "Shutdown"
	case "delete":
		delete(f.devs, args[1])
	case "spawn":
		switch args[3] {
		case "manageruid":
			return []byte("501\n"), nil
		case "list":
			return []byte("PID\tStatus\tLabel\n123\t0\tcom.apple.assistantd\n"), nil
		}
	}
	return nil, nil
}

func (f *lifeFake) has(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func newLifeControl(t *testing.T) (*Control, *lifeFake) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("simulators are macOS only")
	}
	f := newLifeFake()
	c := New(t.TempDir())
	c.Run = f.run
	c.Host = func(_ context.Context, name string, args ...string) ([]byte, error) { return nil, nil }
	c.SetSlimPath(filepath.Join(t.TempDir(), "iossim-slim.json"))
	return c, f
}

func TestResolveCatalog(t *testing.T) {
	cat, err := ParseCatalog([]byte(`{"devicetypes":[{"identifier":"com.apple.CoreSimulator.SimDeviceType.iPhone-17","name":"iPhone 17"}]}`),
		[]byte(`{"runtimes":[{"identifier":"com.apple.CoreSimulator.SimRuntime.iOS-26-0","platform":"iOS","version":"26.0","name":"iOS 26.0","isAvailable":true},{"identifier":"com.apple.CoreSimulator.SimRuntime.iOS-27-2","platform":"iOS","version":"27.2","name":"iOS 27.2","isAvailable":true},{"identifier":"x.tvOS-30","platform":"tvOS","version":"30","name":"tvOS 30","isAvailable":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"iPhone-17", "iPhone 17", "iphone_17", "com.apple.CoreSimulator.SimDeviceType.iPhone-17"} {
		if id, err := cat.ResolveDeviceType(q); err != nil || !strings.HasSuffix(id, "iPhone-17") {
			t.Errorf("type %q: %q %v", q, id, err)
		}
	}
	var ie *slim.InputError
	if _, err := cat.ResolveDeviceType("nope"); !errors.As(err, &ie) {
		t.Errorf("unknown type should be an input error: %v", err)
	}
	for q, want := range map[string]string{"": "27.2", "iOS": "27.2", "iOS 26.0": "26.0", "26.0": "26.0", "iOS-27-2": "27.2", "tvOS": "30"} {
		if r, err := cat.ResolveRuntime(q); err != nil || r.Version != want {
			t.Errorf("runtime %q: %+v %v", q, r, err)
		}
	}
	if _, err := cat.ResolveRuntime("iOS 9"); err == nil {
		t.Error("expected error")
	}
}

func TestCloneNeedsShutdownAndReappliesSlim(t *testing.T) {
	c, f := newLifeControl(t)
	base := "11111111-1111-1111-1111-111111111111"
	ctx := context.Background()
	// give the base a slim record (as if slimmed earlier)
	eng, _ := c.slimEngine()
	_ = eng.Store.Put(slim.Record{UDID: base, Profile: "aggressive", Disabled: []string{"com.apple.assistantd"}, BaselineMB: 9000})

	f.devs[base].state = "Booted"
	if _, err := c.Clone(ctx, base, CloneOptions{}); !errors.Is(err, ErrBooted) {
		t.Fatalf("booted source without shutdown: %v", err)
	}
	if f.has("clone ") {
		t.Fatal("cloned a booted source")
	}
	res, err := c.Clone(ctx, base, CloneOptions{Shutdown: true, Count: 2, Name: "Copy"})
	if err != nil || len(res) != 2 {
		t.Fatalf("clone: %v %+v", err, res)
	}
	if !f.has("shutdown " + base) {
		t.Error("source not shut down")
	}
	for i, r := range res {
		if r.Error != "" || r.Slim == nil || r.Slim.Profile != "aggressive" {
			t.Fatalf("clone %d should be re-slimmed with the source profile: %+v", i, r)
		}
		rec, ok := eng.Store.Get(r.Simulator.UDID)
		if !ok || rec.BaselineMB != 9000 {
			t.Errorf("clone %d record: %+v %v", i, rec, ok)
		}
		if r.Simulator.State != "Shutdown" {
			t.Errorf("clone %d should be shut down again, is %s", i, r.Simulator.State)
		}
	}
	if res[0].Simulator.Name != "Copy 1" {
		t.Errorf("name: %q", res[0].Simulator.Name)
	}
	// slim=false: no slimming, no boot
	no := false
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	res, err = c.Clone(ctx, base, CloneOptions{SlimSpec: SlimSpec{Slim: &no}})
	if err != nil || res[0].Slim != nil || f.has("boot ") {
		t.Fatalf("slim=false: %v %+v %v", err, res, f.calls)
	}
}

func TestEraseKeepSlimAndDelete(t *testing.T) {
	c, f := newLifeControl(t)
	base := "11111111-1111-1111-1111-111111111111"
	ctx := context.Background()
	eng, _ := c.slimEngine()
	_ = eng.Store.Put(slim.Record{UDID: base, Profile: "minimal-safe", Disabled: []string{"com.apple.assistantd"}})

	f.devs[base].state = "Booted"
	if _, err := c.Erase(ctx, base, EraseOptions{}); !errors.Is(err, ErrBooted) {
		t.Fatalf("erase booted: %v", err)
	}
	if _, err := c.Erase(ctx, base, EraseOptions{Shutdown: true, KeepSlim: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := eng.Store.Get(base); !ok {
		t.Error("keep-slim dropped the record")
	}
	if f.has("spawn " + base + " launchctl enable") {
		t.Error("keep-slim must not re-enable labels")
	}
	// without keep-slim the labels are re-enabled first and the record goes
	if _, err := c.Erase(ctx, base, EraseOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := eng.Store.Get(base); ok || !f.has("spawn "+base+" launchctl enable user/501/com.apple.assistantd") {
		t.Error("erase without keep-slim should revert slimming")
	}
	if !f.has("erase " + base) {
		t.Error("erase not called")
	}
	// delete clears slim + WDA desired state
	_ = eng.Store.Put(slim.Record{UDID: base, Profile: "aggressive", Disabled: []string{"x"}})
	_ = c.Mgr.SetDesired(base, true)
	if err := c.Delete(ctx, base, DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := eng.Store.Get(base); ok || c.Mgr.Desired(base) {
		t.Error("delete should clear slim record and WDA desired state")
	}
	if _, err := c.Get(ctx, base); err == nil {
		t.Error("still listed after delete")
	}
}

func TestCreateAndFarm(t *testing.T) {
	c, f := newLifeControl(t)
	ctx := context.Background()
	res, err := c.Create(ctx, CreateOptions{Name: "N", DeviceType: "iPhone 17", Boot: true})
	if err != nil || res.Simulator.State != "Booted" || !f.has("create N com.apple.CoreSimulator.SimDeviceType.iPhone-17 com.apple.CoreSimulator.SimRuntime.iOS-27-2") {
		t.Fatalf("create: %v %+v %v", err, res, f.calls)
	}
	yes := true
	fr, err := c.Farm(ctx, FarmOptions{From: "11111111-1111-1111-1111-111111111111", Count: 2, SlimSpec: SlimSpec{Slim: &yes}})
	if err != nil || fr.Status != "ok" || fr.Succeeded != 2 {
		t.Fatalf("farm: %v %+v", err, fr)
	}
	for _, s := range fr.Sims {
		if !s.Booted || !s.Slimmed || !strings.HasPrefix(s.Name, "pl-farm-Base-") {
			t.Errorf("farm sim: %+v", s)
		}
	}
	if len(c.FarmRuns()) != 1 {
		t.Fatalf("runs: %+v", c.FarmRuns())
	}
	// cleanup deletes only the farm clones: the created sim and the base stay
	cl := c.FarmCleanup(ctx)
	if len(cl.Deleted) != 2 || len(cl.Failed) != 0 {
		t.Fatalf("cleanup: %+v", cl)
	}
	sims, _ := c.List(ctx)
	if len(sims) != 2 {
		t.Fatalf("expected base + created sim to survive, got %+v", sims)
	}
	if len(c.FarmRuns()) != 0 {
		t.Error("record not cleared")
	}
	// bad count
	if _, err := c.Farm(ctx, FarmOptions{From: "x", Count: 0}); err == nil {
		t.Error("count 0 should fail")
	}
}

func TestPortFreeSeesWildcardListener(t *testing.T) {
	l, err := net.Listen("tcp", ":0") // wildcard, like the user's own WDA
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	p := l.Addr().(*net.TCPAddr).Port
	if portFree(p) {
		t.Fatalf("port %d is busy on the wildcard address but reported free", p)
	}
	l.Close()
	if !portFree(p) {
		t.Fatalf("port %d should be free again", p)
	}
}

func TestPrepareOptsSkipsBusyDefaultAndReusesPorts(t *testing.T) {
	l, err := net.Listen("tcp", ":8200")
	if err != nil {
		t.Skip("8200 is in use on this machine; covered by the busy-port logic itself")
	}
	c := New(t.TempDir())
	o := c.PrepareOpts("U1", iosbridge.Options{})
	l.Close()
	if o.WDAPort == 8200 {
		t.Fatalf("picked the busy default port 8200")
	}
	// a restart without options reuses the ports of the last start
	c.lastPorts["U1"] = [2]int{o.WDAPort, o.MJPEGPort}
	if o2 := c.PrepareOpts("U1", iosbridge.Options{}); o2.WDAPort != o.WDAPort || o2.MJPEGPort != o.MJPEGPort {
		t.Fatalf("restart ports %d/%d, want %d/%d", o2.WDAPort, o2.MJPEGPort, o.WDAPort, o.MJPEGPort)
	}
	// explicit ports from an earlier start are remembered too
	c.lastPorts["U2"] = [2]int{8333, 9333}
	if o3 := c.PrepareOpts("U2", iosbridge.Options{}); o3.WDAPort != 8333 {
		t.Fatalf("explicit ports not reused: %+v", o3)
	}
}
