package iosdrv

import (
	"context"
	"sync"
	"time"

	"github.com/danielpaulus/go-ios/ios"
)

// HotplugEvent is one usbmuxd attach/detach notification. Resync is set
// instead when the listener (re)connected, meaning events may have been missed
// and the caller should do a full rescan.
type HotplugEvent struct {
	Attached bool
	Detached bool
	UDID     string
	Network  bool
	Resync   bool
}

// ListenFunc opens a usbmuxd listen session. next blocks until the next
// attach/detach message; closeFn must make a blocked next return.
type ListenFunc func() (next func() (HotplugEvent, error), closeFn func() error, err error)

// UsbmuxdListen is the production ListenFunc, built on go-ios's Listen.
func UsbmuxdListen() (func() (HotplugEvent, error), func() error, error) {
	recv, closeFn, err := ios.Listen()
	if err != nil {
		if closeFn != nil {
			_ = closeFn()
		}
		return nil, nil, err
	}
	return func() (HotplugEvent, error) {
		m, err := recv()
		if err != nil {
			return HotplugEvent{}, err
		}
		return HotplugEvent{
			Attached: m.DeviceAttached(),
			Detached: m.DeviceDetached(),
			UDID:     m.Properties.SerialNumber,
			Network:  isNetwork(m.DeviceEntry()),
		}, nil
	}, closeFn, nil
}

// WatchOptions tunes Watch; zero values pick sensible defaults.
type WatchOptions struct {
	MinBackoff time.Duration // first reconnect delay (default 1s)
	MaxBackoff time.Duration // reconnect delay cap (default 30s)
	// Logf, if set, receives one line per listener state change (not per retry).
	Logf func(format string, a ...any)
}

// Watch keeps a usbmuxd listen session open and calls notify for every attach,
// detach and (re)connect until ctx ends. If the session cannot be opened (no
// usbmuxd, e.g. on Windows/Linux without it) or drops, Watch retries with
// exponential backoff; callers should keep a periodic rescan as the safety net
// meanwhile. notify must not block for long.
func Watch(ctx context.Context, listen ListenFunc, notify func(HotplugEvent), o WatchOptions) {
	if o.MinBackoff <= 0 {
		o.MinBackoff = time.Second
	}
	if o.MaxBackoff < o.MinBackoff {
		o.MaxBackoff = 30 * time.Second
	}
	logf := func(f string, a ...any) {
		if o.Logf != nil {
			o.Logf(f, a...)
		}
	}
	backoff := o.MinBackoff
	failing := false
	for ctx.Err() == nil {
		next, closeFn, err := listen()
		if err != nil {
			if !failing {
				logf("usbmuxd hot-plug listener unavailable (%v); falling back to periodic rescan", err)
				failing = true
			}
			if !sleepCtx(ctx, backoff) {
				return
			}
			if backoff *= 2; backoff > o.MaxBackoff {
				backoff = o.MaxBackoff
			}
			continue
		}
		if failing {
			logf("usbmuxd hot-plug listener connected")
		}
		failing = false
		backoff = o.MinBackoff
		notify(HotplugEvent{Resync: true})

		var once sync.Once
		closeOnce := func() { once.Do(func() { _ = closeFn() }) }
		stop := make(chan struct{})
		go func() { // unblock next() when ctx ends
			select {
			case <-ctx.Done():
				closeOnce()
			case <-stop:
			}
		}()
		for {
			ev, err := next()
			if err != nil {
				break
			}
			notify(ev)
		}
		close(stop)
		closeOnce()
		if ctx.Err() != nil {
			return
		}
		logf("usbmuxd hot-plug listener dropped; reconnecting")
		failing = true
		if !sleepCtx(ctx, backoff) {
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
