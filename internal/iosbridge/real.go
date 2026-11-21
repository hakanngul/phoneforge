package iosbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/Masterminds/semver"
	"github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/forward"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
	"github.com/danielpaulus/go-ios/ios/testmanagerd"
	"github.com/danielpaulus/go-ios/ios/tunnel"
)

// ---- tunnel ---------------------------------------------------------------

type tunEntry struct {
	tun tunnel.Tunnel
	dev ios.DeviceEntry
}

// goiosTunnel opens one in-process userspace tunnel per device. No root, no
// tunnel agent process.
type goiosTunnel struct {
	mu      sync.Mutex
	tunnels map[string]*tunEntry
}

func newGoIOSTunnel() *goiosTunnel { return &goiosTunnel{tunnels: map[string]*tunEntry{}} }

func freeLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

var (
	v174 = semver.MustParse("17.4.0")
	v17  = semver.MustParse("17.0.0")
)

func (g *goiosTunnel) Ensure(ctx context.Context, udid string) (ios.DeviceEntry, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if e, ok := g.tunnels[udid]; ok {
		return e.dev, nil
	}
	dev, err := ios.GetDevice(udid)
	if err != nil {
		return ios.DeviceEntry{}, err
	}
	ver, err := ios.GetProductVersion(dev)
	if err != nil {
		return ios.DeviceEntry{}, fmt.Errorf("read iOS version (is the phone unlocked and trusted?): %w", err)
	}
	if ver.LessThan(v17) {
		return dev, nil // no tunnel needed before iOS 17
	}
	if !ver.GreaterThan(v174) {
		return ios.DeviceEntry{}, fmt.Errorf("%w: iOS %s (17.0-17.4) cannot use the userspace tunnel; run `sudo ios tunnel start` yourself or update the phone to 17.5+", ErrTunnelUnsupported, ver)
	}
	port, err := freeLocalPort()
	if err != nil {
		return ios.DeviceEntry{}, err
	}
	dev.UserspaceTUNPort = port

	type res struct {
		t   tunnel.Tunnel
		err error
	}
	ch := make(chan res, 1)
	go func() {
		t, e := tunnel.ConnectUserSpaceTunnelLockdown(dev, port)
		ch <- res{t, e}
	}()
	var tun tunnel.Tunnel
	select {
	case r := <-ch:
		if r.err != nil {
			return ios.DeviceEntry{}, fmt.Errorf("userspace tunnel (CoreDeviceProxy): %w", r.err)
		}
		tun = r.t
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.err == nil {
				_ = r.t.Close()
			}
		}()
		return ios.DeviceEntry{}, ctx.Err()
	}

	dev.UserspaceTUN = true
	dev.UserspaceTUNHost = "127.0.0.1"
	rsd, err := ios.NewWithAddrPortDevice(tun.Address, tun.RsdPort, dev)
	if err != nil {
		_ = tun.Close()
		return ios.DeviceEntry{}, fmt.Errorf("RSD connect: %w", err)
	}
	prov, err := rsd.Handshake()
	_ = rsd.Close()
	if err != nil {
		_ = tun.Close()
		return ios.DeviceEntry{}, fmt.Errorf("RSD handshake: %w", err)
	}
	out, err := ios.GetDeviceWithAddress(udid, tun.Address, prov)
	if err != nil {
		_ = tun.Close()
		return ios.DeviceEntry{}, err
	}
	out.UserspaceTUN, out.UserspaceTUNHost, out.UserspaceTUNPort = true, "127.0.0.1", port
	g.tunnels[udid] = &tunEntry{tun: tun, dev: out}
	return out, nil
}

func (g *goiosTunnel) Release(udid string) error {
	g.mu.Lock()
	e := g.tunnels[udid]
	delete(g.tunnels, udid)
	g.mu.Unlock()
	if e == nil {
		return nil
	}
	return e.tun.Close()
}

// ---- launcher -------------------------------------------------------------

type goiosLauncher struct{ stateDir string }

func (l goiosLauncher) Run(ctx context.Context, dev ios.DeviceEntry, o Options, logs io.Writer) error {
	env := map[string]any{
		"USE_PORT":          fmt.Sprint(o.WDAPort),
		"MJPEG_SERVER_PORT": fmt.Sprint(o.MJPEGPort),
	}
	for k, v := range o.Env {
		env[k] = v
	}
	att := filepath.Join(l.stateDir, "attachments", dev.Properties.SerialNumber)
	if err := os.MkdirAll(att, 0o755); err != nil {
		att = os.TempDir()
	}
	_, err := testmanagerd.RunTestWithConfig(ctx, testmanagerd.TestConfig{
		BundleId:           o.RunnerBundleID,
		TestRunnerBundleId: o.TestRunnerBundleID,
		XctestConfigName:   o.XCTestConfig,
		Env:                env,
		Args:               o.Args,
		Device:             dev,
		Listener:           testmanagerd.NewTestListener(logs, logs, att),
	})
	if err == nil && ctx.Err() != nil {
		return nil
	}
	return err
}

// ---- forwarder ------------------------------------------------------------

type goiosForwarder struct{}

type listenerCloser struct {
	l    net.Listener
	once sync.Once
}

func (c *listenerCloser) Close() error {
	var err error
	c.once.Do(func() { err = c.l.Close() })
	return err
}

// Forward listens on 127.0.0.1:<free> (go-ios' own forward.Forward binds
// 0.0.0.0, which would expose WDA to the LAN). USB devices go through usbmuxd;
// Wi-Fi devices dial the device through the userspace tunnel.
func (goiosForwarder) Forward(ctx context.Context, dev ios.DeviceEntry, devicePort int) (int, io.Closer, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, err
	}
	viaTunnel := dev.Properties.ConnectionType != "USB" && dev.UserspaceTUN && dev.Address != ""
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			if viaTunnel {
				go proxyViaTunnel(c, dev, devicePort)
			} else {
				go func() { _ = forward.StartNewProxyConnection(ctx, c, dev.DeviceID, uint16(devicePort)) }()
			}
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, &listenerCloser{l: l}, nil
}

func proxyViaTunnel(c net.Conn, dev ios.DeviceEntry, port int) {
	dc, err := ios.ConnectTUNDevice(dev.Address, port, dev)
	if err != nil {
		c.Close()
		return
	}
	var once sync.Once
	closeBoth := func() { once.Do(func() { c.Close(); dc.Close() }) }
	go func() { _, _ = io.Copy(dc, c); closeBoth() }()
	_, _ = io.Copy(c, dc)
	closeBoth()
}

// ---- installer ------------------------------------------------------------

type goiosInstaller struct{}

func (goiosInstaller) Installed(ctx context.Context, udid, bundleID string) (bool, error) {
	type res struct {
		ok  bool
		err error
	}
	ch := make(chan res, 1)
	go func() {
		dev, err := ios.GetDevice(udid)
		if err != nil {
			ch <- res{false, err}
			return
		}
		conn, err := installationproxy.New(dev)
		if err != nil {
			ch <- res{false, err}
			return
		}
		defer conn.Close()
		apps, err := conn.BrowseUserApps()
		if err != nil {
			ch <- res{false, err}
			return
		}
		for _, a := range apps {
			if a.CFBundleIdentifier() == bundleID {
				ch <- res{true, nil}
				return
			}
		}
		ch <- res{false, nil}
	}()
	select {
	case r := <-ch:
		return r.ok, r.err
	case <-ctx.Done():
		return false, errors.Join(ctx.Err())
	}
}
