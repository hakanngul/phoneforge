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
| Android live view | ✅ | ✅ | H.264, 540p, max 3 Mbps; decodes in WebKit (Safari 80 fps while swiping); not checked inside the desktop app itself |
| iPhone live view | ✅ | ✅ | ~24–28 fps (WDA); desktop app ~22 fps with the WebKit raw-MJPEG fix |
| iPhone 60 fps mirroring | ? | 🟡 | Camera permission granted to PhoneForge.app, device found, but no frames within 15 s |
| Mouse tap and swipe | ✅ | ✅ | |
| Element Picker, UI tree | ✅ | ✅ | |
| Live FPS indicator | ? | ✅ | |
| OCR | ✅ | ✅ | Simulator 0.4 s · iPhone ~0.5 s warm; tap pixel→point mapping verified |
| Web/Chrome automation | ✅ | ✅ | Android Chrome: targets, navigate, html, eval, click |
| **AI** | | | |
| MCP server | ✅ | ✅ | 48 tools |
| Phone control with Claude Code | ✅ | ✅ | App launch tried with Haiku |
| Codex | ✅ | ✅ | Run verified (Codex 0.169): opened Settings, read items, returned home |
| Cursor, Gemini | ✅ | 🟡 | Connection exists; runs not tried (no keys/installs) |
| Kiro / kiro-cli | ? | 🟡 | Implemented, not installed/tested: connect, kiro-cli runs |
| Own agent (API key or Ollama) | ✅ | 🟡 | Not tried with a real model |
| Agent replanning / asking the user | ✅ | 🟡 | Not verified |
| Model selection, chat memory, unlimited permissions | ? | ✅ | |
| Claude Code skills | ✅ | ✅ | Installed and listed by Claude Code |
| Claude Code plugin | ✅ | 🟡 | Validator passes; marketplace install not tried |
| Cloud agents (mobai-dev) | ✅ | ❌ | |
| **Automation and testing** | | | |
| DSL | ✅ | ✅ | ~70 actions; batch verified on Android (8 steps) |
| .mob tests, runner, history | ✅ | ✅ | Run on Android, iPhone, simulator (CLI `run` and REST); history recorded |
| Maestro YAML import | ✅ | ❌ | |
| .mobflow flows | ✅ | ❌ | |
| JavaScript steps in DSL | ✅ | ❌ | |
| Screen recording and performance metrics from DSL | ✅ | ❌ | UI record button exists |
| GitHub Action (CI) | ✅ | 🟡 | Release + CI workflows added (`.github/workflows`), not yet run on GitHub; no reusable Action for users yet |
| **Apps and debugging** | | | |
| App list, launch, stop | ✅ | ✅ | |
| App install and uninstall | ✅ | ✅ | Android (bridge APKs via CLI + REST incl. multipart); iOS (.app reinstall via CLI, WDA auto-recovered ~15 s) |
| Crash reports | ✅ | ✅ | iPhone, Galaxy, simulator |
| lldb | ✅ | 🟡 | Developer Tools disabled on the Mac |
| App builder | ✅ | ❌ | |
| **Platform** | | | |
| REST API | ✅ | ✅ | |
| CLI | ✅ (mobai) | ✅ (phoneforge) | State-changing commands (launch, tap --text, type, key, swipe, stop, wait) verified on Android, iPhone, simulator |
| Desktop app | ✅ | ✅ | Wails v3 `PhoneForge.app` (ad-hoc signed; notarization/auto-update open) |
| Remote devices (Tailscale, sharing) | ✅ | 🟡 | Code exists |
| Cloud relay | ✅ | ❌ | |
| AWS Device Farm | ✅ | 🟡 | Settings screen only |
| Webhooks, secrets, integrations | ✅ | 🟡 | Code exists |
| License | Free: 1 device · Pro $9.99/mo: unlimited | Open source | |

Items already tracked in the sections below (Wails notarization/auto-update, DSL JavaScript steps, Maestro import, DSL recording and metrics, AWS Device Farm, Cloud Agents, 60 fps mirroring, lldb, iOS Wi-Fi, built-in agent with an API key) are not repeated in the checklists.

### Missing entirely

- [ ] Simulator slimming (memory)
- [ ] `.mobflow` flows
- [ ] GitHub Action (CI)
- [ ] App builder
- [ ] Cloud relay

### Implemented but unverified on device

- [ ] Kiro IDE connect and kiro-cli runs (needs KIRO_API_KEY; verify `--trust-tools=@phoneforge`, `--resume`, `--require-mcp-startup`), Cursor and Gemini runs, and the built-in agent with a real model (API key or Ollama)
- [ ] Agent replan / ask-user loop
- [ ] Remote devices / Tailscale
- [ ] Webhooks, secrets, integrations

## Live mirroring

- **iOS native 60 fps capture (parked).** Code exists: `internal/avcapture` (Swift helper `internal/avcapture/helper/pfcapture.swift` → `~/.phoneforge/bin/pfcapture`, gate in `internal/httpapi/avgate.go`).
  - Status: Camera permission is now granted to PhoneForge.app; the device is found but no frames arrive within 15 s. Earlier (Claude app/Terminal context) a first test streamed frames (1180x2556); after stopping it with SIGINT, `pfcapture list` returned no devices.
  - Suspects: device left claimed after SIGINT (the helper should stop the AVCaptureSession on SIGINT/SIGTERM); MobAI.app holding the CMIO device; discovery needing AVCaptureDeviceWasConnected notifications or a longer wait.
  - Also: the TCC permission belongs to the launching app, so the server must be started from a context that has Camera permission.
  - Next: add a `diag` subcommand (CMIO + AVFoundation device list), add graceful signal handling, retest with MobAI closed.
- Browser first frame on simulators took 3–7 s in an earlier test (before the fetch-based MJPEG viewer); re-measure.
- iOS stats badge turns amber at <24 fps while WDA delivers ~24; consider a threshold of 20.

## iOS

- iOS Settings search needs a tap on the SearchField itself before typing (a tip for skills/docs, not a bug).
- iOS 27.3 iPhone (iPhone18,2): needs USB connection + Trust, developer disk image, WDA install; currently only seen over Wi-Fi.
- lldb debugging: implemented (`internal/debugger`), but never attached live. The Mac has Developer Tools security disabled (`sudo DevToolsSecurity -enable` needed, user action).
- Untested on real devices: iOS crash-report delete, element picker/tap via the new fetch-based mirror viewer.
- REST gestures take points (393x852 on iPhone 15); clients using screenshot pixel coordinates tap the wrong place. Consider a `units=px` option.

## Fixed 2026-11-21

- Codex flags (Codex 0.169), simctl screenshot temp file, device deep link `?device=`.
- Android `tree`/`wait`/`assert` speed (commit e6188d0): ~2.2 s on the Galaxy (SM-G780G) → 0.1–0.2 s via lazy UiAutomator2 bridge auto-start (`PF_ANDROID_AUTOBRIDGE=0` disables), `waitForIdleTimeout=0`, 250 ms polling.
- Simulator WDA auto-restart (commit e6188d0): simulators are now supervised like real iPhones (desired state in `~/.phoneforge/iossim-state.json`, restored on server restart and sim reboot, lazy start on first input/tree; `PF_SIM_AUTOWDA=0` disables).

## Remaining parity features

- Wails v3 native desktop shell: 🟡 done (`cmd/phoneforge-desktop`, `scripts/build-desktop.sh`); remaining: Developer ID signing + notarization, auto-update, Windows/Linux builds
- Screen recording + performance metrics (CPU/memory/FPS) from DSL
- DSL JavaScript steps
- Maestro YAML import (kept in the backlog for later by user's choice; low priority, no existing Maestro tests)
- AWS Device Farm integration, Cloud Agents
- Remote-node proxying for crashes and debug routes

## Untested on device (needs user OK)

- Android pinch/drag, permissions, agent tap/type actions, built-in agent with a real model, Cursor, Gemini, Kiro (no keys/installs).
