// Package iosbridge starts and stops a WebDriverAgent (WDA) runner on a real
// iPhone and exposes its HTTP (8100) and MJPEG (9100) ports on 127.0.0.1.
//
// Everything runs in-process on top of go-ios v1.3.2:
//
//   - Tunnel: for iOS 17.4+ a userspace tunnel is opened through the lockdown
//     CoreDeviceProxy service (the in-process equivalent of
//     `ios tunnel start --userspace`). It needs NO root, no sudo and no
//     separate tunnel agent: only a USB (or trusted Wi-Fi) connection and a
//     host that is already trusted/paired with the phone. iOS 17.0-17.4 would
//     need the kernel tunnel (root) and is reported as ErrTunnelUnsupported.
//     iOS < 17 needs no tunnel at all.
//   - Launch: testmanagerd.RunTestWithConfig, equivalent to
//     `ios runwda --bundleid=X --testrunnerbundleid=X --xctestconfig=WebDriverAgentRunner.xctest`.
//   - Forward: a 127.0.0.1-only TCP forwarder (usbmuxd for USB devices, the
//     userspace tunnel for Wi-Fi devices).
//
// User prerequisites that no code can remove: Developer Mode enabled on the
// phone, the Developer Disk Image mounted, and the WDA runner app
// (default com.phoneforge.WebDriverAgentRunner.xctrunner) built and installed
// with the user's own signing identity.
package iosbridge
