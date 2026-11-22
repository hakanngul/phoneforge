package avcapture

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Options tune the helper's capture.
type Options struct {
	FPS     int     // default 60
	Scale   float64 // default 0.5
	Quality float64 // default 0.6
}

func (o Options) norm() Options {
	if o.FPS <= 0 {
		o.FPS = 60
	}
	if o.Scale <= 0 || o.Scale > 1 {
		o.Scale = 0.5
	}
	if o.Quality <= 0 || o.Quality > 1 {
		o.Quality = 0.6
	}
	return o
}

// spawner starts one helper capture for a capture-device id. stdout carries
// length-prefixed frames; wait is called after stdout ends and reports the
// classified exit; kill terminates the process.
type spawner func(ctx context.Context, id string, o Options) (stdout io.ReadCloser, wait func() *Error, kill func(), err error)

// Manager owns one helper process per device, shared by all viewers.
type Manager struct {
	IdleStop     time.Duration // stop capture this long after the last viewer leaves (default 4s)
	FirstFrame   time.Duration // Subscribe waits this long for a first frame (default 15s)
	FailureTTL   time.Duration // remember fatal failures this long (default 30s)
	ListTTL      time.Duration // cache `plcapture list` this long (default 5s)
	MaxRestarts  int           // consecutive frameless restarts before giving up (default 3)
	RestartDelay time.Duration // base delay between restarts (default 500ms)

	spawn spawner
	list  func(ctx context.Context) (Listing, error)

	mu      sync.Mutex
	streams map[string]*stream // key: capture device id
	fails   map[string]failure // key: capture device id
	cache   Listing
	cacheAt time.Time
	cacheOK bool
}

type failure struct {
	err *Error
	at  time.Time
}

// Default is the process-wide manager.
var Default = New()

// New returns a Manager using the real helper.
func New() *Manager {
	m := &Manager{streams: map[string]*stream{}, fails: map[string]failure{}}
	m.spawn = execSpawn
	m.list = runList
	return m
}

func (m *Manager) idle() time.Duration       { return dflt(m.IdleStop, 4*time.Second) }
func (m *Manager) firstFrame() time.Duration { return dflt(m.FirstFrame, 15*time.Second) }
func (m *Manager) failureTTL() time.Duration { return dflt(m.FailureTTL, 30*time.Second) }
func (m *Manager) listTTL() time.Duration    { return dflt(m.ListTTL, 5*time.Second) }
func (m *Manager) restartDelay() time.Duration {
	return dflt(m.RestartDelay, 500*time.Millisecond)
}
func (m *Manager) maxRestarts() int {
	if m.MaxRestarts > 0 {
		return m.MaxRestarts
	}
	return 3
}
func dflt(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func norm(s string) string { return strings.ToLower(strings.ReplaceAll(s, "-", "")) }

// Supported reports whether native capture can work on this host at all.
func Supported() bool {
	return runtime.GOOS == "darwin" && !strings.EqualFold(envDisable(), "0") && !strings.EqualFold(envDisable(), "off")
}

func (m *Manager) snapshot(ctx context.Context) (Listing, error) {
	m.mu.Lock()
	if m.cacheOK && time.Since(m.cacheAt) < m.listTTL() {
		l := m.cache
		m.mu.Unlock()
		return l, nil
	}
	m.mu.Unlock()
	l, err := m.list(ctx)
	if err != nil {
		return Listing{}, err
	}
	m.mu.Lock()
	m.cache, m.cacheAt, m.cacheOK = l, time.Now(), true
	m.mu.Unlock()
	return l, nil
}

// Resolve finds the capture device for an iPhone. The AVCapture uniqueID is a
// GUID, so after trying UDID equality it falls back to a unique device-name
// match against names (e.g. the lockdown device name and model).
func Resolve(l Listing, udid string, names ...string) (CaptureDevice, bool) {
	for _, d := range l.Devices {
		if norm(d.ID) == norm(udid) {
			return d, true
		}
	}
	for _, n := range names {
		if n == "" {
			continue
		}
		var hit []CaptureDevice
		for _, d := range l.Devices {
			if strings.EqualFold(d.Name, n) {
				hit = append(hit, d)
			}
		}
		if len(hit) == 1 {
			return hit[0], true
		}
	}
	return CaptureDevice{}, false
}

// Available reports whether the iPhone is currently capturable: the helper
// lists it, camera permission is not denied, and no recent fatal failure is
// remembered. names are extra name hints (see Resolve). It may compile the
// helper on first use.
func (m *Manager) Available(udid string, names ...string) bool {
	_, err := m.check(context.Background(), udid, names...)
	return err == nil
}

// Check is like Available but explains why not.
func (m *Manager) Check(ctx context.Context, udid string, names ...string) error {
	_, err := m.check(ctx, udid, names...)
	return err
}

func (m *Manager) check(ctx context.Context, udid string, names ...string) (CaptureDevice, error) {
	if !Supported() {
		return CaptureDevice{}, &Error{"UNSUPPORTED", "native capture needs macOS"}
	}
	l, err := m.snapshot(ctx)
	if err != nil {
		return CaptureDevice{}, &Error{"HELPER_FAILED", err.Error()}
	}
	if l.Authorization == "denied" || l.Authorization == "restricted" {
		return CaptureDevice{}, &Error{"TCC_DENIED", "camera permission " + l.Authorization + ": allow the app running Pocketlab in System Settings > Privacy & Security > Camera"}
	}
	d, ok := Resolve(l, udid, names...)
	if !ok {
		return CaptureDevice{}, &Error{"DEVICE_NOT_FOUND", "iPhone not offered by macOS screen capture (unlock it and tap Trust)"}
	}
	m.mu.Lock()
	f, bad := m.fails[d.ID]
	if bad && time.Since(f.at) > m.failureTTL() {
		delete(m.fails, d.ID)
		bad = false
	}
	m.mu.Unlock()
	if bad {
		return d, f.err
	}
	return d, nil
}

// Invalidate drops the cached device listing so the next check re-lists.
func (m *Manager) Invalidate() {
	m.mu.Lock()
	m.cacheOK = false
	m.mu.Unlock()
}

// Subscription is one viewer's handle on a shared capture. Frames delivers the
// newest frame (slow readers skip frames); it is closed when the capture ends.
type Subscription struct {
	Frames <-chan []byte
	ch     chan []byte
	s      *stream
	m      *Manager
	once   sync.Once
	mu     sync.Mutex
	err    error
}

// Err is why Frames closed (nil if the viewer closed it).
func (s *Subscription) Err() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }

// Close detaches the viewer; the capture stops after the idle delay if it was
// the last one.
func (s *Subscription) Close() {
	s.once.Do(func() {
		if s.s != nil {
			s.s.remove(s)
		}
	})
}

// NewFakeSubscription returns a subscription over frames that is not backed by
// a helper process (for tests of code that consumes an AV backend).
func NewFakeSubscription(frames <-chan []byte) *Subscription {
	return &Subscription{Frames: frames}
}

// Subscribe attaches a viewer to the iPhone's shared capture, starting the
// helper if needed, and waits for the first frame. The returned subscription
// has that first frame queued. Errors are *Error and the caller should fall
// back (e.g. to WDA).
func (m *Manager) Subscribe(ctx context.Context, udid string, o Options, names ...string) (*Subscription, error) {
	d, err := m.check(ctx, udid, names...)
	if err != nil {
		return nil, err
	}
	o = o.norm()
	m.mu.Lock()
	st := m.streams[d.ID]
	if st == nil {
		st = newStream(m, d.ID, o)
		m.streams[d.ID] = st
		go st.run()
	}
	sub := st.add()
	m.mu.Unlock()

	t := time.NewTimer(m.firstFrame())
	defer t.Stop()
	select {
	case f, ok := <-sub.ch:
		if !ok {
			sub.Close()
			if e := sub.Err(); e != nil {
				return nil, e
			}
			return nil, &Error{"HELPER_FAILED", "capture ended"}
		}
		// Put the first frame back for the caller.
		sub.push(f)
		return sub, nil
	case <-t.C:
		sub.Close()
		return nil, &Error{"NO_FRAMES", "no frame within " + m.firstFrame().String()}
	case <-ctx.Done():
		sub.Close()
		return nil, ctx.Err()
	}
}

// ActiveViewers returns the number of viewers on the iPhone's capture (tests/diagnostics).
func (m *Manager) ActiveViewers(captureID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.streams[captureID]; s != nil {
		return s.viewers()
	}
	return 0
}

// stream is one helper process shared by many viewers.
type stream struct {
	m    *Manager
	id   string
	opts Options

	mu       sync.Mutex
	subs     map[*Subscription]struct{}
	last     []byte
	idleT    *time.Timer
	stopped  bool
	killCur  func()
	stopCh   chan struct{}
	gotFrame bool
}

func newStream(m *Manager, id string, o Options) *stream {
	return &stream{m: m, id: id, opts: o, subs: map[*Subscription]struct{}{}, stopCh: make(chan struct{})}
}

func (s *stream) viewers() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.subs) }

// add registers a viewer (caller holds m.mu, so a stopping stream is never revived).
func (s *stream) add() *Subscription {
	ch := make(chan []byte, 1)
	sub := &Subscription{Frames: ch, ch: ch, s: s, m: s.m}
	s.mu.Lock()
	if s.idleT != nil {
		s.idleT.Stop()
		s.idleT = nil
	}
	s.subs[sub] = struct{}{}
	if s.last != nil {
		sub.push(s.last)
	}
	s.mu.Unlock()
	return sub
}

// push delivers f, dropping any older undelivered frame.
func (sub *Subscription) push(f []byte) {
	for {
		select {
		case sub.ch <- f:
			return
		default:
			select {
			case <-sub.ch:
			default:
			}
		}
	}
}

func (s *stream) remove(sub *Subscription) {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[sub]; !ok {
		return
	}
	delete(s.subs, sub)
	if len(s.subs) == 0 && !s.stopped {
		s.idleT = time.AfterFunc(s.m.idle(), func() { s.stopIfIdle() })
	}
}

func (s *stream) stopIfIdle() {
	s.m.mu.Lock()
	s.mu.Lock()
	if len(s.subs) > 0 || s.stopped {
		s.mu.Unlock()
		s.m.mu.Unlock()
		return
	}
	s.stopped = true
	delete(s.m.streams, s.id)
	kill := s.killCur
	close(s.stopCh)
	s.mu.Unlock()
	s.m.mu.Unlock()
	if kill != nil {
		kill()
	}
}

// finish ends the stream, closing every viewer with err and recording a fatal failure.
func (s *stream) finish(err *Error) {
	s.m.mu.Lock()
	s.mu.Lock()
	if !s.stopped {
		s.stopped = true
		close(s.stopCh)
	}
	if s.m.streams[s.id] == s {
		delete(s.m.streams, s.id)
	}
	if err != nil && err.Fatal() {
		s.m.fails[s.id] = failure{err, time.Now()}
	}
	subs := s.subs
	s.subs = map[*Subscription]struct{}{}
	if s.idleT != nil {
		s.idleT.Stop()
	}
	s.mu.Unlock()
	s.m.mu.Unlock()
	for sub := range subs {
		sub.mu.Lock()
		if err != nil {
			sub.err = err
		}
		sub.mu.Unlock()
		close(sub.ch)
	}
}

func (s *stream) publish(f []byte) {
	s.mu.Lock()
	s.last = f
	s.gotFrame = true
	subs := make([]*Subscription, 0, len(s.subs))
	for sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()
	for _, sub := range subs {
		sub.push(f)
	}
}

func (s *stream) isStopped() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

// run drives the helper, restarting it on crashes.
func (s *stream) run() {
	fails := 0
	for {
		if s.isStopped() {
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		out, wait, kill, err := s.m.spawn(ctx, s.id, s.opts)
		if err != nil {
			cancel()
			s.finish(&Error{"HELPER_FAILED", err.Error()})
			return
		}
		s.mu.Lock()
		s.killCur = kill
		stopped := s.stopped
		s.mu.Unlock()
		if stopped {
			kill()
		}
		gotAny := false
		for {
			f, rerr := ReadFrame(out)
			if rerr != nil {
				break
			}
			gotAny = true
			s.publish(f)
		}
		out.Close()
		werr := wait()
		cancel()
		if s.isStopped() {
			return
		}
		if werr != nil && werr.Fatal() {
			s.finish(werr)
			return
		}
		if gotAny {
			fails = 0
		} else {
			fails++
		}
		if fails >= s.m.maxRestarts() {
			if werr == nil {
				werr = &Error{"HELPER_FAILED", "helper kept exiting without frames"}
			}
			s.finish(werr)
			return
		}
		select {
		case <-s.stopCh:
			return
		case <-time.After(s.m.restartDelay() * time.Duration(fails+1)):
		}
	}
}

// execSpawn runs the real plcapture helper.
func execSpawn(ctx context.Context, id string, o Options) (io.ReadCloser, func() *Error, func(), error) {
	bin, err := EnsureHelper(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "stream", "--id", id,
		"--fps", strconv.Itoa(o.FPS),
		"--scale", strconv.FormatFloat(o.Scale, 'f', 3, 64),
		"--quality", strconv.FormatFloat(o.Quality, 'f', 3, 64))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	var (
		mu   sync.Mutex
		last string
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t != "" {
				mu.Lock()
				last = t
				mu.Unlock()
			}
		}
	}()
	wait := func() *Error {
		<-done
		werr := cmd.Wait()
		mu.Lock()
		l := last
		mu.Unlock()
		if werr == nil {
			return nil
		}
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			return classifyExit(ee.ExitCode(), l)
		}
		return &Error{"HELPER_FAILED", werr.Error()}
	}
	kill := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	return stdout, wait, kill, nil
}
