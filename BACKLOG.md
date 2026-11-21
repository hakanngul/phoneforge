# PhoneForge Backlog

Last updated: 2026-11-21

## Live mirroring

- **iOS native 60 fps capture (parked).** Code exists: `internal/avcapture` (Swift helper `internal/avcapture/helper/pfcapture.swift` → `~/.phoneforge/bin/pfcapture`, gate in `internal/httpapi/avgate.go`).
  - Status: macOS Camera (TCC) permission was granted to the Claude app/Terminal panel context; the first test streamed frames (1180x2556). After stopping that test with SIGINT, `pfcapture list` returns no devices, even after replugging the iPhone.
  - Suspects: device left claimed after SIGINT (the helper should stop the AVCaptureSession on SIGINT/SIGTERM); MobAI.app holding the CMIO device; discovery needing AVCaptureDeviceWasConnected notifications or a longer wait.
  - Also: the TCC permission belongs to the launching app, so the server must be started from a context that has Camera permission.
  - Next: add a `diag` subcommand (CMIO + AVFoundation device list), add graceful signal handling, retest with MobAI closed.
- Browser first frame on simulators took 3–7 s in an earlier test (before the fetch-based MJPEG viewer); re-measure.
- iOS stats badge turns amber at <24 fps while WDA delivers ~24; consider a threshold of 20.

## iOS

- OCR on the real iPhone takes ~23 s (simulator 0.4 s); likely the real-device screenshot path (2.7 MB PNG). Speed up with JPEG / a scaled screenshot from WDA.
- iOS 27.3 iPhone (iPhone18,2): needs USB connection + Trust, developer disk image, WDA install; currently only seen over Wi-Fi.
- lldb debugging: implemented (`internal/debugger`), but never attached live. The Mac has Developer Tools security disabled (`sudo DevToolsSecurity -enable` needed, user action).
- Untested on real devices: iOS app install/uninstall, iOS crash-report delete, element picker/tap via the new fetch-based mirror viewer.
- REST gestures take points (393x852 on iPhone 15); clients using screenshot pixel coordinates tap the wrong place. Consider a `units=px` option.

## Remaining parity features

- Wails v3 native desktop shell
- Screen recording + performance metrics (CPU/memory/FPS) from DSL
- DSL JavaScript steps, Maestro YAML import
- AWS Device Farm integration, Cloud Agents
- Remote-node proxying for crashes and debug routes

## Untested on device (needs user OK)

- Android pinch/drag, app install/uninstall, permissions, `.mob` runs on a phone, agent tap/type actions, Codex runs, Chrome/WebView automation on a phone, built-in agent with an API key.
