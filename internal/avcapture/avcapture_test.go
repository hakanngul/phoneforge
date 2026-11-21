package avcapture

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	frames := [][]byte{{0xFF, 0xD8, 1, 2, 3, 0xFF, 0xD9}, {0xFF, 0xD8, 9, 0xFF, 0xD9}}
	for _, f := range frames {
		if err := WriteFrame(&buf, f); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range frames {
		got, err := ReadFrame(&buf)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("frame %d: %v %v", i, got, err)
		}
	}
	if _, err := ReadFrame(&buf); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestReadFrameErrors(t *testing.T) {
	// Truncated body.
	var b bytes.Buffer
	_ = WriteFrame(&b, []byte("abcdef"))
	if _, err := ReadFrame(bytes.NewReader(b.Bytes()[:7])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated: %v", err)
	}
	// Truncated header.
	if _, err := ReadFrame(bytes.NewReader([]byte{0, 0})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short header: %v", err)
	}
	// Absurd and zero lengths.
	for _, n := range []uint32{0, MaxFrame + 1, 0xFFFFFFFF} {
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], n)
		if _, err := ReadFrame(bytes.NewReader(h[:])); !errors.Is(err, ErrBadFrame) {
			t.Fatalf("len %d: %v", n, err)
		}
	}
}

func TestParseListingAndResolve(t *testing.T) {
	l, err := ParseListing([]byte(`{"authorization":"authorized","devices":[{"id":"0824F741-5133-4E4A-9A68-ABE3324B6C53","name":"iPhone 15","model":"iOS Device","connected":true}]}`))
	if err != nil || l.Authorization != "authorized" || len(l.Devices) != 1 {
		t.Fatalf("%+v %v", l, err)
	}
	if _, ok := Resolve(l, "00008120-000A1B2C3D4E5F60"); ok {
		t.Fatal("udid must not match a GUID")
	}
	if d, ok := Resolve(l, "00008120-000A1B2C3D4E5F60", "iphone 15"); !ok || d.Name != "iPhone 15" {
		t.Fatal("name fallback failed")
	}
	if _, ok := Resolve(l, "x", "Other"); ok {
		t.Fatal("unexpected match")
	}
	if d, ok := Resolve(l, "0824f74151334e4a9a68abe3324b6c53"); !ok || d.ID == "" {
		t.Fatal("dashless uniqueID should match")
	}
	two := Listing{Devices: []CaptureDevice{{ID: "a", Name: "iPhone"}, {ID: "b", Name: "iPhone"}}}
	if _, ok := Resolve(two, "u", "iPhone"); ok {
		t.Fatal("ambiguous name must not match")
	}
}

func TestClassifyExit(t *testing.T) {
	if e := classifyExit(10, `{"event":"tcc_denied","message":"nope"}`); e.Code != "TCC_DENIED" || e.Msg != "nope" || !e.Fatal() {
		t.Fatalf("%+v", e)
	}
	if e := classifyExit(1, ""); e.Code != "HELPER_FAILED" || e.Fatal() {
		t.Fatalf("%+v", e)
	}
}

// fakeHelper hands out pipes; each spawn returns the next script.
type fakeHelper struct {
	spawns  atomic.Int32
	kills   atomic.Int32
	scripts []func(w io.WriteCloser) *Error // returns exit error after closing
}

func (f *fakeHelper) spawn(ctx context.Context, id string, o Options) (io.ReadCloser, func() *Error, func(), error) {
	n := int(f.spawns.Add(1)) - 1
	pr, pw := io.Pipe()
	res := make(chan *Error, 1)
	script := f.scripts[len(f.scripts)-1]
	if n < len(f.scripts) {
		script = f.scripts[n]
	}
	go func() {
		e := script(pw)
		pw.Close()
		res <- e
	}()
	var once sync.Once
	kill := func() { once.Do(func() { f.kills.Add(1); pw.CloseWithError(io.EOF) }) }
	go func() { <-ctx.Done() }()
	return pr, func() *Error { return <-res }, kill, nil
}

func newTestManager(f *fakeHelper) *Manager {
	m := New()
	m.spawn = f.spawn
	m.list = func(ctx context.Context) (Listing, error) {
		return Listing{Authorization: "authorized", Devices: []CaptureDevice{{ID: "G1", Name: "iPhone 15"}}}, nil
	}
	m.IdleStop = 100 * time.Millisecond
	m.RestartDelay = 5 * time.Millisecond
	m.FirstFrame = 2 * time.Second
	return m
}

// streamFrames emits frames until the pipe is killed.
func streamFrames(interval time.Duration) func(io.WriteCloser) *Error {
	return func(w io.WriteCloser) *Error {
		for i := 0; ; i++ {
			if err := WriteFrame(w, []byte{0xFF, 0xD8, byte(i), 0xFF, 0xD9}); err != nil {
				return nil
			}
			time.Sleep(interval)
		}
	}
}

func TestFanOutSharedProcessAndIdleStop(t *testing.T) {
	f := &fakeHelper{scripts: []func(io.WriteCloser) *Error{streamFrames(5 * time.Millisecond)}}
	m := newTestManager(f)
	ctx := context.Background()
	a, err := m.Subscribe(ctx, "U", Options{}, "iPhone 15")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Subscribe(ctx, "U", Options{}, "iPhone 15")
	if err != nil {
		t.Fatal(err)
	}
	if f.spawns.Load() != 1 {
		t.Fatalf("expected one shared helper, got %d", f.spawns.Load())
	}
	if m.ActiveViewers("G1") != 2 {
		t.Fatalf("viewers=%d", m.ActiveViewers("G1"))
	}
	for _, s := range []*Subscription{a, b} {
		for i := 0; i < 3; i++ {
			select {
			case fr := <-s.Frames:
				if len(fr) != 5 {
					t.Fatal("bad frame")
				}
			case <-time.After(time.Second):
				t.Fatal("no frame")
			}
		}
	}
	a.Close()
	time.Sleep(250 * time.Millisecond)
	if f.kills.Load() != 0 {
		t.Fatal("helper stopped while a viewer remained")
	}
	b.Close()
	b.Close() // idempotent
	deadline := time.Now().Add(time.Second)
	for f.kills.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.kills.Load() != 1 {
		t.Fatalf("helper not stopped after last viewer left (kills=%d)", f.kills.Load())
	}
	// A new viewer starts a fresh helper.
	c, err := m.Subscribe(ctx, "U", Options{}, "iPhone 15")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if f.spawns.Load() != 2 {
		t.Fatalf("spawns=%d", f.spawns.Load())
	}
}

func TestSlowViewerGetsLatestFrame(t *testing.T) {
	f := &fakeHelper{scripts: []func(io.WriteCloser) *Error{streamFrames(time.Millisecond)}}
	m := newTestManager(f)
	s, err := m.Subscribe(context.Background(), "U", Options{}, "iPhone 15")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	<-s.Frames
	time.Sleep(100 * time.Millisecond) // many frames produced meanwhile
	fr := <-s.Frames
	if fr[2] < 10 {
		t.Fatalf("expected a recent frame, got index %d", fr[2])
	}
}

func TestRestartOnCrash(t *testing.T) {
	crash := func(w io.WriteCloser) *Error {
		_ = WriteFrame(w, []byte{0xFF, 0xD8, 1, 0xFF, 0xD9})
		return &Error{"HELPER_FAILED", "boom"}
	}
	f := &fakeHelper{scripts: []func(io.WriteCloser) *Error{crash, streamFrames(5 * time.Millisecond)}}
	m := newTestManager(f)
	s, err := m.Subscribe(context.Background(), "U", Options{}, "iPhone 15")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deadline := time.After(2 * time.Second)
	for f.spawns.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("helper not restarted")
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case _, ok := <-s.Frames:
		if !ok {
			t.Fatal("subscription closed by a recoverable crash")
		}
	case <-time.After(time.Second):
		t.Fatal("no frames after restart")
	}
}

func TestFatalErrorFallsBackAndIsRemembered(t *testing.T) {
	deny := func(w io.WriteCloser) *Error { return &Error{"TCC_DENIED", "camera denied"} }
	f := &fakeHelper{scripts: []func(io.WriteCloser) *Error{deny}}
	m := newTestManager(f)
	_, err := m.Subscribe(context.Background(), "U", Options{}, "iPhone 15")
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != "TCC_DENIED" {
		t.Fatalf("err=%v", err)
	}
	if m.Available("U", "iPhone 15") {
		t.Fatal("Available must be false right after a fatal failure")
	}
	if f.spawns.Load() != 1 {
		t.Fatalf("fatal error must not restart (spawns=%d)", f.spawns.Load())
	}
}

func TestAvailableChecks(t *testing.T) {
	f := &fakeHelper{scripts: []func(io.WriteCloser) *Error{streamFrames(time.Millisecond)}}
	m := newTestManager(f)
	if !m.Available("U", "iPhone 15") {
		t.Fatal("should be available")
	}
	if m.Available("U", "Other Phone") {
		t.Fatal("unknown device must be unavailable")
	}
	m.list = func(ctx context.Context) (Listing, error) { return Listing{Authorization: "denied"}, nil }
	m.cacheOK = false
	if err := m.Check(context.Background(), "U", "iPhone 15"); err == nil {
		t.Fatal("denied TCC must fail")
	}
}
