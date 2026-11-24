// Package lifecycle keeps a persistent record of why the Pocketlab server
// started and stopped: a capped JSON-lines event log (server-events.jsonl) and
// a heartbeat state file (server.state) used to detect exits nobody recorded
// (kill -9, power loss, a crash in a non-main goroutine).
package lifecycle

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Event kinds.
const (
	KindStart          = "start"
	KindStop           = "stop"
	KindUnexpectedExit = "unexpected_exit"
	KindCrash          = "crash"
	KindListenFailed   = "listen_failed"

	// Updater events (origin "updater").
	KindUpdateStarted = "update_started"
	KindUpdateOK      = "update_ok"
	KindUpdateFailed  = "update_failed"
	KindRollback      = "rollback"
)

// File names inside the state directory.
const (
	EventsFile = "server-events.jsonl"
	StateFile  = "server.state"
)

// Tunables (variables so tests can shrink them).
var (
	MaxEvents        = 500
	HeartbeatEvery   = 10 * time.Second
	compactThreshold = int64(256 << 10) // compact the log once it grows beyond this
	// aliveWindow: a previous run whose pid is alive and whose heartbeat is
	// younger than this is considered a concurrently running instance.
	aliveWindow = 45 * time.Second
	pidAlive    = processAlive
)

// Event is one line of server-events.jsonl.
type Event struct {
	Time          time.Time  `json:"time"`
	Event         string     `json:"event"`
	PID           int        `json:"pid"`
	Version       string     `json:"version,omitempty"`
	Addr          string     `json:"addr,omitempty"`
	StartedVia    string     `json:"startedVia,omitempty"`
	Owner         string     `json:"owner,omitempty"`
	ParentPID     int        `json:"parentPid,omitempty"`
	ParentCmd     string     `json:"parentCmd,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	Origin        string     `json:"origin,omitempty"`
	Signal        string     `json:"signal,omitempty"`
	Message       string     `json:"message,omitempty"`
	LastHeartbeat *time.Time `json:"lastHeartbeat,omitempty"`
	PIDGone       *bool      `json:"pidGone,omitempty"`
	UptimeSec     int64      `json:"uptimeSec,omitempty"`
	LogTail       []string   `json:"logTail,omitempty"`
}

// Unexpected reports whether the event is an exit nobody asked for.
func (e Event) Unexpected() bool {
	return e.Event == KindUnexpectedExit || e.Event == KindCrash || e.Event == KindListenFailed
}

// Info describes the process being recorded.
type Info struct {
	Version    string
	Addr       string
	StartedVia string // e.g. "pocketlabd", "desktop", "pocketlab server start"
	Owner      string // "cli" or "desktop"
	// LogTail returns the most recent log lines (optional).
	LogTail func() []string
}

// State is the heartbeat file: the last thing the running server wrote.
type State struct {
	Running    bool      `json:"running"`
	PID        int       `json:"pid"`
	Version    string    `json:"version,omitempty"`
	Addr       string    `json:"addr,omitempty"`
	StartedVia string    `json:"startedVia,omitempty"`
	Owner      string    `json:"owner,omitempty"`
	ParentPID  int       `json:"parentPid,omitempty"`
	ParentCmd  string    `json:"parentCmd,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	Heartbeat  time.Time `json:"heartbeat"`
	LogTail    []string  `json:"logTail,omitempty"`
}

// Status is the live view served by GET /api/v1/server/status.
type Status struct {
	Running    bool      `json:"running"`
	PID        int       `json:"pid"`
	Version    string    `json:"version"`
	Addr       string    `json:"addr"`
	StartedAt  time.Time `json:"startedAt"`
	UptimeSec  int64     `json:"uptimeSec"`
	StartedVia string    `json:"startedVia"`
	Owner      string    `json:"owner"`
	ParentPID  int       `json:"parentPid,omitempty"`
	ParentCmd  string    `json:"parentCmd,omitempty"`
	Home       string    `json:"home"`
	EventsFile string    `json:"eventsFile"`
	Stopping   bool      `json:"stopping,omitempty"`
}

var fileMu sync.Mutex // serialises log appends/compaction within this process

// Recorder records the lifecycle of one running server.
type Recorder struct {
	dir       string
	info      Info
	pid       int
	startedAt time.Time
	parentPID int
	parentCmd string

	mu       sync.Mutex
	reason   string
	origin   string
	signal   string
	finished atomic.Bool
	quit     chan struct{}
}

// Begin records the start of a server (and, first, the unexpected end of the
// previous run if it left a running marker behind), writes the state file and
// starts the heartbeat. Call Stop or Crash to finish it.
func Begin(dir string, info Info) *Recorder {
	_ = os.MkdirAll(dir, 0o700)
	r := &Recorder{dir: dir, info: info, pid: os.Getpid(), startedAt: time.Now(), quit: make(chan struct{})}
	r.parentPID, r.parentCmd = parentInfo()
	DetectUnclean(dir, r.pid)
	r.append(Event{
		Event: KindStart, Reason: "server started",
		StartedVia: info.StartedVia, Owner: info.Owner,
		ParentPID: r.parentPID, ParentCmd: r.parentCmd,
	})
	r.writeState(true)
	go r.heartbeat()
	return r
}

// DetectUnclean looks at the previous run's state file. If it says "running"
// but that process is gone (or stopped heart-beating), it records an
// unexpected_exit event and marks the state stopped. Returns whether an event
// was written. selfPID is the caller's own pid (never treated as previous).
func DetectUnclean(dir string, selfPID int) bool {
	st, err := ReadState(dir)
	if err != nil || st == nil || !st.Running || st.PID == selfPID {
		return false
	}
	gone := !pidAlive(st.PID)
	if !gone && time.Since(st.Heartbeat) < aliveWindow {
		return false // another instance is genuinely running against this home
	}
	hb := st.Heartbeat
	reason := fmt.Sprintf("previous run (pid %d) ended unexpectedly (killed or crashed) at about %s", st.PID, hb.Local().Format("2006-01-02 15:04:05"))
	if !gone {
		reason = fmt.Sprintf("previous run (pid %d) stopped heart-beating at %s (pid still alive or reused)", st.PID, hb.Local().Format("2006-01-02 15:04:05"))
	}
	ev := Event{
		Time: hb, Event: KindUnexpectedExit, PID: st.PID, Version: st.Version, Addr: st.Addr,
		StartedVia: st.StartedVia, Owner: st.Owner, ParentPID: st.ParentPID, ParentCmd: st.ParentCmd,
		Reason: reason, LastHeartbeat: &hb, PIDGone: &gone, LogTail: st.LogTail,
		UptimeSec: int64(hb.Sub(st.StartedAt).Seconds()),
	}
	appendEvent(dir, ev)
	st.Running = false
	writeState(dir, st)
	return true
}

// SetStopReason remembers why the server is about to stop. The first call wins
// so the originating cause (e.g. the shutdown request) is not overwritten by
// the generic one recorded later.
func (r *Recorder) SetStopReason(reason, origin, signal string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reason != "" {
		return
	}
	r.reason, r.origin, r.signal = reason, origin, signal
}

// Stop records a clean stop (idempotent). If no reason was set it uses fallback.
func (r *Recorder) Stop(fallback string) {
	if !r.finished.CompareAndSwap(false, true) {
		return
	}
	close(r.quit)
	r.mu.Lock()
	reason, origin, sig := r.reason, r.origin, r.signal
	r.mu.Unlock()
	if reason == "" {
		reason = fallback
	}
	r.append(Event{Event: KindStop, Reason: reason, Origin: origin, Signal: sig, UptimeSec: int64(time.Since(r.startedAt).Seconds())})
	r.writeState(false)
}

// Crash records a fatal error / panic (idempotent; wins over a later Stop).
func (r *Recorder) Crash(message string) {
	if !r.finished.CompareAndSwap(false, true) {
		return
	}
	close(r.quit)
	stack := strings.Split(strings.TrimSpace(string(debug.Stack())), "\n")
	tail := r.logTail()
	if len(stack) > 40 {
		stack = stack[:40]
	}
	tail = append(tail, "--- stack ---")
	tail = append(tail, stack...)
	r.mu.Lock()
	r.reason = ""
	r.mu.Unlock()
	ev := Event{Event: KindCrash, Reason: "fatal error: " + message, Message: message, LogTail: tail, UptimeSec: int64(time.Since(r.startedAt).Seconds())}
	r.append(ev)
	r.writeState(false)
}

// Status returns the live status.
func (r *Recorder) Status() Status {
	r.mu.Lock()
	stopping := r.reason != ""
	r.mu.Unlock()
	return Status{
		Running: !r.finished.Load(), PID: r.pid, Version: r.info.Version, Addr: r.info.Addr,
		StartedAt: r.startedAt, UptimeSec: int64(time.Since(r.startedAt).Seconds()),
		StartedVia: r.info.StartedVia, Owner: r.info.Owner, ParentPID: r.parentPID, ParentCmd: r.parentCmd,
		Home: r.dir, EventsFile: filepath.Join(r.dir, EventsFile), Stopping: stopping,
	}
}

// History returns up to limit most recent events (newest first).
func (r *Recorder) History(limit int) []Event { return ReadHistory(r.dir, limit) }

// SetAddr updates the recorded address (used once the final port is known).
func (r *Recorder) SetAddr(addr string) { r.info.Addr = addr }

func (r *Recorder) logTail() []string {
	if r.info.LogTail == nil {
		return nil
	}
	return r.info.LogTail()
}

func (r *Recorder) heartbeat() {
	t := time.NewTicker(HeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-r.quit:
			return
		case <-t.C:
			r.writeState(true)
		}
	}
}

func (r *Recorder) writeState(running bool) {
	writeState(r.dir, &State{
		Running: running, PID: r.pid, Version: r.info.Version, Addr: r.info.Addr,
		StartedVia: r.info.StartedVia, Owner: r.info.Owner, ParentPID: r.parentPID, ParentCmd: r.parentCmd,
		StartedAt: r.startedAt, Heartbeat: time.Now(), LogTail: lastN(r.logTail(), 20),
	})
}

func (r *Recorder) append(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	ev.PID = r.pid
	ev.Version, ev.Addr = r.info.Version, r.info.Addr
	if ev.StartedVia == "" {
		ev.StartedVia, ev.Owner = r.info.StartedVia, r.info.Owner
	}
	appendEvent(r.dir, ev)
}

// RecordListenFailure logs that the server could not bind its address.
func RecordListenFailure(dir string, info Info, err error) {
	_ = os.MkdirAll(dir, 0o700)
	ev := Event{
		Time: time.Now(), Event: KindListenFailed, PID: os.Getpid(), Version: info.Version, Addr: info.Addr,
		StartedVia: info.StartedVia, Owner: info.Owner,
		Reason: "could not listen on " + info.Addr + " (port in use?)", Message: err.Error(),
	}
	ev.ParentPID, ev.ParentCmd = parentInfo()
	appendEvent(dir, ev)
}

// Append writes one event to the log in dir (used by the updater, which runs
// outside a Recorder). Time and PID are filled in when unset.
func Append(dir string, ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	if ev.PID == 0 {
		ev.PID = os.Getpid()
	}
	_ = os.MkdirAll(dir, 0o700)
	appendEvent(dir, ev)
}

// ---- files ----

func appendEvent(dir string, ev Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fileMu.Lock()
	defer fileMu.Unlock()
	p := filepath.Join(dir, EventsFile)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
	if st, err := os.Stat(p); err == nil && st.Size() > compactThreshold {
		compact(p)
	}
}

// compact keeps only the newest MaxEvents lines.
func compact(p string) {
	lines := readLines(p)
	if len(lines) <= MaxEvents {
		return
	}
	lines = lines[len(lines)-MaxEvents:]
	tmp := p + ".tmp"
	if os.WriteFile(tmp, append(bytes.Join(lines, []byte("\n")), '\n'), 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

func readLines(p string) [][]byte {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		if l := bytes.TrimSpace(sc.Bytes()); len(l) > 0 {
			out = append(out, append([]byte(nil), l...))
		}
	}
	return out
}

// ReadHistory returns up to limit most recent events, newest first.
// limit <= 0 means 50.
func ReadHistory(dir string, limit int) []Event {
	if limit <= 0 {
		limit = 50
	}
	fileMu.Lock()
	lines := readLines(filepath.Join(dir, EventsFile))
	fileMu.Unlock()
	out := make([]Event, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
		var ev Event
		if json.Unmarshal(lines[i], &ev) == nil {
			out = append(out, ev)
		}
	}
	return out
}

// ReadState reads the heartbeat file (nil, nil if absent).
func ReadState(dir string) (*State, error) {
	b, err := os.ReadFile(filepath.Join(dir, StateFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeState(dir string, st *State) {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	p := filepath.Join(dir, StateFile)
	tmp := p + fmt.Sprintf(".%d.tmp", os.Getpid())
	if os.WriteFile(tmp, b, 0o600) == nil {
		if os.Rename(tmp, p) != nil {
			_ = os.Remove(tmp)
		}
	}
}

func lastN(s []string, n int) []string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// ---- log tail ring ----

// TailWriter keeps the last lines written to it (an io.Writer for log output).
type TailWriter struct {
	mu    sync.Mutex
	lines []string
	part  string
	max   int
}

// NewTailWriter keeps the last max lines.
func NewTailWriter(max int) *TailWriter { return &TailWriter{max: max} }

func (t *TailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.part + string(p)
	parts := strings.Split(s, "\n")
	t.part = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		if len(l) > 500 {
			l = l[:500] + "…"
		}
		t.lines = append(t.lines, l)
	}
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
	return len(p), nil
}

// Lines returns a copy of the retained lines.
func (t *TailWriter) Lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.lines...)
}
