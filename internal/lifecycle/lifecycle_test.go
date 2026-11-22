package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartStopRecorded(t *testing.T) {
	dir := t.TempDir()
	r := Begin(dir, Info{Version: "1.2.3", Addr: "127.0.0.1:1", StartedVia: "pocketlabd", Owner: "cli"})
	r.SetStopReason("shutdown API request", "web-ui", "")
	r.SetStopReason("ignored later reason", "x", "") // first wins
	r.Stop("fallback")
	r.Stop("again") // idempotent
	h := ReadHistory(dir, 10)
	if len(h) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(h), h)
	}
	if h[0].Event != KindStop || h[0].Reason != "shutdown API request" || h[0].Origin != "web-ui" {
		t.Errorf("stop event: %+v", h[0])
	}
	if h[1].Event != KindStart || h[1].StartedVia != "pocketlabd" || h[1].PID != os.Getpid() || h[1].Version != "1.2.3" {
		t.Errorf("start event: %+v", h[1])
	}
	st, _ := ReadState(dir)
	if st == nil || st.Running {
		t.Errorf("state should be stopped: %+v", st)
	}
}

func TestStopFallbackReason(t *testing.T) {
	dir := t.TempDir()
	r := Begin(dir, Info{})
	r.Stop("stopped without a recorded reason")
	if h := ReadHistory(dir, 1); h[0].Reason != "stopped without a recorded reason" {
		t.Errorf("%+v", h[0])
	}
}

func TestCrashRecorded(t *testing.T) {
	dir := t.TempDir()
	tw := NewTailWriter(5)
	fmt.Fprintln(tw, "line one")
	r := Begin(dir, Info{LogTail: tw.Lines})
	r.Crash("boom")
	r.Stop("late") // ignored: already finished
	h := ReadHistory(dir, 10)
	if len(h) != 2 || h[0].Event != KindCrash || h[0].Message != "boom" || !h[0].Unexpected() {
		t.Fatalf("%+v", h)
	}
	if len(h[0].LogTail) == 0 || h[0].LogTail[0] != "line one" {
		t.Errorf("log tail missing: %v", h[0].LogTail)
	}
}

func TestUncleanExitDetected(t *testing.T) {
	dir := t.TempDir()
	hb := time.Now().Add(-5 * time.Minute)
	writeState(dir, &State{Running: true, PID: 999991, Version: "v", Addr: "a", StartedVia: "desktop", StartedAt: hb.Add(-time.Hour), Heartbeat: hb, LogTail: []string{"last words"}})
	old := pidAlive
	pidAlive = func(int) bool { return false }
	defer func() { pidAlive = old }()

	r := Begin(dir, Info{StartedVia: "pocketlabd"})
	defer r.Stop("test done")
	h := ReadHistory(dir, 10)
	if len(h) != 2 || h[0].Event != KindStart || h[1].Event != KindUnexpectedExit {
		t.Fatalf("want start after unexpected_exit: %+v", h)
	}
	u := h[1]
	if u.PID != 999991 || u.LastHeartbeat == nil || u.PIDGone == nil || !*u.PIDGone || u.StartedVia != "desktop" || len(u.LogTail) != 1 {
		t.Errorf("unexpected_exit: %+v", u)
	}
	// a second start must not repeat it
	if DetectUnclean(dir, os.Getpid()) {
		t.Error("state already marked stopped; no second event expected")
	}
}

func TestLiveOtherInstanceNotFlagged(t *testing.T) {
	dir := t.TempDir()
	writeState(dir, &State{Running: true, PID: 4242, Heartbeat: time.Now()})
	old := pidAlive
	pidAlive = func(int) bool { return true }
	defer func() { pidAlive = old }()
	if DetectUnclean(dir, os.Getpid()) {
		t.Error("a live instance with a fresh heartbeat must not be reported as exited")
	}
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	oldMax, oldThr := MaxEvents, compactThreshold
	MaxEvents, compactThreshold = 10, 1
	defer func() { MaxEvents, compactThreshold = oldMax, oldThr }()
	for i := 0; i < 40; i++ {
		appendEvent(dir, Event{Event: KindStop, Reason: fmt.Sprintf("r%d", i), PID: i})
	}
	lines := readLines(filepath.Join(dir, EventsFile))
	if len(lines) > 10 {
		t.Fatalf("log not capped: %d lines", len(lines))
	}
	h := ReadHistory(dir, 100)
	if h[0].Reason != "r39" {
		t.Errorf("newest first expected, got %s", h[0].Reason)
	}
}

func TestListenFailure(t *testing.T) {
	dir := t.TempDir()
	RecordListenFailure(dir, Info{Addr: "127.0.0.1:8700", StartedVia: "pocketlabd"}, fmt.Errorf("address already in use"))
	h := ReadHistory(dir, 5)
	if len(h) != 1 || h[0].Event != KindListenFailed || !h[0].Unexpected() || h[0].Message != "address already in use" {
		t.Errorf("%+v", h)
	}
}

func TestTailWriter(t *testing.T) {
	tw := NewTailWriter(3)
	for i := 0; i < 6; i++ {
		fmt.Fprintf(tw, "l%d\n", i)
	}
	got := tw.Lines()
	if len(got) != 3 || got[0] != "l3" || got[2] != "l5" {
		t.Errorf("%v", got)
	}
}
