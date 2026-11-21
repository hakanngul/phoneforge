# PhoneForge Backlog

Last updated: 2026-11-21

## MobAI parity (2026-11-21)

Legend: ✅ done & verified, 🟡 code exists but not fully verified, 🔨 in progress, ❌ missing, ? unknown for MobAI.

| Area | MobAI | PhoneForge | Notes |
|---|---|---|---|
| **Devices** | | | |
| Android device discovery and info | ✅ | ✅ | |
| iOS real device (USB) | ✅ | ✅ | Verified on iPhone 15 |
| iOS Wi-Fi connection | ✅ | 🟡 | Mitigations for connection drops exist; not tried with the iOS 27.3 phone |
| iOS simulators | ✅ | ✅ | Boot, WDA, tap and typing tried |
| Simulator slimming (memory) | ✅ | ❌ | |
| **Mirroring and control** | | | |
| Android live view | ✅ | ✅ | H.264, 540p, max 3 Mbps |
| iPhone live view | ✅ | ✅ | ~24–28 fps (WDA) |
| iPhone 60 fps mirroring | ? | 🟡 | Code exists; macOS does not list the iPhone |
| Mouse tap and swipe | ✅ | ✅ | |
| Element Picker, UI tree | ✅ | ✅ | |
| Live FPS indicator | ? | ✅ | |
| OCR | ✅ | ✅ | Simulator 0.4 s · 🟡 on iPhone: 23 s |
| Web/Chrome automation | ✅ | 🟡 | Not tried on a phone |
| **AI** | | | |
| MCP server | ✅ | ✅ | 48 tools |
| Phone control with Claude Code | ✅ | ✅ | App launch tried with Haiku |
| Codex, Cursor, Gemini | ✅ | 🟡 | Connection exists; runs not tried |
| Own agent (API key or Ollama) | ✅ | 🟡 | Not tried with a real model |
| Agent replanning / asking the user | ✅ | 🟡 | Not verified |
| Model selection, chat memory, unlimited permissions | ? | ✅ | |
| Claude Code skills | ✅ | 🟡 | Written, plugin validator passes; not yet installed/verified in Claude Code |
| Claude Code plugin | ✅ | 🟡 | Written, plugin validator passes; not yet installed/verified in Claude Code |
| Cloud agents (mobai-dev) | ✅ | ❌ | |
| **Automation and testing** | | | |
| DSL | ✅ | ✅ | ~70 actions |
| .mob tests, runner, history | ✅ | 🟡 | Not run on a phone |
| Maestro YAML import | ✅ | ❌ | |
| .mobflow flows | ✅ | ❌ | |
| JavaScript steps in DSL | ✅ | ❌ | |
| Screen recording and performance metrics from DSL | ✅ | ❌ | UI record button exists |
| GitHub Action (CI) | ✅ | ❌ | |
| **Apps and debugging** | | | |
| App list, launch, stop | ✅ | ✅ | |
| App install and uninstall | ✅ | 🟡 | Not tried on a device |
| Crash reports | ✅ | ✅ | iPhone, Galaxy, simulator |
| lldb | ✅ | 🟡 | Developer Tools disabled on the Mac |
| App builder | ✅ | ❌ | |
| **Platform** | | | |
| REST API | ✅ | ✅ | |
| CLI | ✅ (mobai) | 🔨 (phoneforge) | phoneforge CLI being finished |
| Desktop app | ✅ | ❌ | Browser UI only |
| Remote devices (Tailscale, sharing) | ✅ | 🟡 | Code exists |
| Cloud relay | ✅ | ❌ | |
| AWS Device Farm | ✅ | 🟡 | Settings screen only |
| Webhooks, secrets, integrations | ✅ | 🟡 | Code exists |
| License | Free: 1 device · Pro $9.99/mo: unlimited | Open source | |

Items already tracked in the sections below (Wails desktop shell, DSL JavaScript steps, Maestro import, DSL recording and metrics, AWS Device Farm, Cloud Agents, 60 fps mirroring, lldb, iOS Wi-Fi, app install/uninstall, `.mob` runs, Codex, Chrome/WebView, built-in agent with an API key) are not repeated in the checklists.

### Missing entirely

- [ ] Simulator slimming (memory)
- [ ] `.mobflow` flows
- [ ] GitHub Action (CI)
- [ ] App builder
- [ ] Cloud relay

### Implemented but unverified on device

- [ ] Cursor and Gemini runs, and the built-in agent with Ollama (see also the Codex / API key bullet under "Untested on device")
- [ ] Agent replan / ask-user loop
- [ ] Remote devices / Tailscale
- [ ] Webhooks, secrets, integrations

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
