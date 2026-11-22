package iossim

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

const psSample = `    1     0   100 /sbin/launchd
 500     1  8000 launchd_sim /Users/me/Library/Developer/CoreSimulator/Devices/AAAA-1/data/var/run/launchd_bootstrap.plist
 501   500 40000 /rt/SpringBoard.app/SpringBoard
 502   500 20000 /rt/usr/libexec/logd
 503   501  1000 /rt/some child process with spaces
 600     1  9000 launchd_sim /Users/me/Library/Developer/CoreSimulator/Devices/BBBB-2/data/var/run/launchd_bootstrap.plist
 601   600 70000 /rt/SpringBoard.app/SpringBoard
 700     1   500 python3 -c launchd_sim /Devices/AAAA-1/
`

func TestParsePSAndSimTree(t *testing.T) {
	procs := ParsePS([]byte(psSample))
	if len(procs) != 8 {
		t.Fatalf("procs: %d", len(procs))
	}
	if procs[4].Command != "/rt/some child process with spaces" {
		t.Fatalf("command: %q", procs[4].Command)
	}
	tree, ok := SimTree(procs, "AAAA-1")
	if !ok || len(tree) != 4 {
		t.Fatalf("tree: %v %+v", ok, tree)
	}
	var rss int64
	for _, p := range tree {
		rss += p.RSSKB
	}
	if rss != 69000 {
		t.Fatalf("rss %d", rss)
	}
	if _, ok := SimTree(procs, "CCCC"); ok {
		t.Fatal("unexpected tree")
	}
}

func TestParseFootprint(t *testing.T) {
	multi := "==\nfoo [1]: 64-bit    Footprint: 100 B (16384 bytes per page)\n==\nbar [2]: 64-bit    Footprint: 50 B\n\nSummary Footprint: 150 B\n"
	if n, ok := ParseFootprint([]byte(multi)); !ok || n != 150 {
		t.Fatalf("multi: %d %v", n, ok)
	}
	one := "======\nfitnesscoachingd [82505]: 64-bit    Footprint: 54282448 B (16384 bytes per page)\n======\n\nAuxiliary data:\n    phys_footprint: 54806736 B\n"
	if n, ok := ParseFootprint([]byte(one)); !ok || n != 54282448 {
		t.Fatalf("one: %d %v", n, ok)
	}
	if _, ok := ParseFootprint([]byte("footprint: Unable to find any processes")); ok {
		t.Fatal("want !ok")
	}
}

func TestMeasure(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin only")
	}
	var fpArgs []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "ps":
			return []byte(psSample), nil
		case "footprint":
			fpArgs = args
			return []byte("Summary Footprint: 10485760 B\n"), nil
		}
		return nil, nil
	}
	m, err := Measure(context.Background(), run, "AAAA-1")
	if err != nil {
		t.Fatal(err)
	}
	if m.ProcCount != 4 || m.FootprintMB != 10 || m.RSSMB < 67 || m.RSSMB > 68 {
		t.Fatalf("%+v", m)
	}
	if got := strings.Join(fpArgs, " "); got != "-f bytes --noCategories 500 501 502 503" {
		t.Fatalf("footprint args: %s", got)
	}
	if _, err := Measure(context.Background(), run, "NOPE"); err != ErrNotBooted {
		t.Fatalf("want ErrNotBooted, got %v", err)
	}
}

func TestDiscoverParsers(t *testing.T) {
	fp := ParseFootprintPerPID([]byte("x [12]: 64-bit    Footprint: 1048576 B (16384 bytes per page)\ny [13]: 64-bit    Footprint: 2097152 B\n"))
	if fp[12] != 1048576 || fp[13] != 2097152 {
		t.Fatalf("%v", fp)
	}
	svc := ParseDomainServices([]byte("system = {\n\tservices = {\n\t\t   90323      - \tcom.apple.logd_helper\n\t\t       0      - \tcom.apple.cfprefsd.xpc.daemon.system\n\t}\n\tdisabled = {\n"))
	if len(svc) != 2 || svc[0].PID != 90323 || svc[1].Label != "com.apple.cfprefsd.xpc.daemon.system" {
		t.Fatalf("%+v", svc)
	}
}

func TestDiscoverJoin(t *testing.T) {
	host := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "ps" {
			return []byte(" 90  1  2048 /x\n 91  1  1024 /y\n"), nil
		}
		return []byte("a [90]: 64-bit    Footprint: 5242880 B\nb [91]: 64-bit    Footprint: 1048576 B\n"), nil
	}
	run := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[3] {
		case "manageruid":
			return []byte("501\n"), nil
		case "list":
			return []byte("PID\tStatus\tLabel\n91\t0\tcom.apple.SpringBoard\n90\t0\tcom.apple.assistantd\n-\t0\tcom.apple.idle\n"), nil
		}
		return nil, nil
	}
	got, err := Discover(context.Background(), run, host, "U", "com.apple.CoreSimulator.SimRuntime.iOS-27-2")
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %+v", err, got)
	}
	if got[0].Label != "com.apple.assistantd" || got[0].FootprintMB != 5 || got[0].RSSMB != 2 || len(got[0].Groups) == 0 {
		t.Fatalf("%+v", got[0])
	}
	if got[1].Label != "com.apple.SpringBoard" || !got[1].Kept {
		t.Fatalf("%+v", got[1])
	}
}
