# PhoneForge — Features

Original, clean-room tool (Go + vanilla web UI). No third-party branding/code. No paid tier — it is
our own software, so every capability is unlocked by design.

## Devices (verified live: Android + 2 iPhones over USB)
- **Android** (adb): full control — screenshot, UI tree, tap/swipe/type, apps.
- **iOS** (go-ios): discovery, device info, app list; app launch (dev image permitting).
- **iOS live control (WDA bridge)**: in-process WebDriverAgent runner (userspace tunnel for iOS 17.4+, no root), `WDA: stopped/starting/running/error` chip, Start/Stop, "Install helper" button, MJPEG live view (`/devices/{id}/mjpeg`), WDA-backed screenshot/UI tree/tap/swipe/type/keys/app launch, MCP `start_bridge`/`stop_bridge`/`bridge_status`/`ios_install_helper`. See docs/ios-setup.md.
  Live screen + UI automation (tap/UI-tree) require the **WebDriverAgent bridge** (Xcode signing) —
  the UI shows an honest notice; Apps + info work today.
- **Remote devices**: "Add node by address" (any network incl. Tailscale) → another PhoneForge node's
  devices appear locally and are fully controllable. No Pro gating.

## Automation console
- **Live mirror** with FPS control, phone frame, click-to-tap.
- **MultiWindow**: grid of live mirrors for all devices at once; click a tile to make it active.
- **Element Picker**: click → deepest UI element inspector, Tap/Copy-selector, highlight overlay.
- **Gestures**: Home/Back/Enter/Delete, type, swipe, double-tap, drag, pinch, open URL, dismiss keyboard.
- **Device extras** (REST, optional per driver, proxied for remote nodes): live H.264 stream
  (`stream.h264`), clipboard get/set, APK install/uninstall, runtime permissions, log stream (SSE `logs`),
  screen recording (`recording`).
- **AI Agent bar**: natural-language task → streamed thought/action/observation (SSE). BYO LLM key
  (Anthropic or local Ollama), stored in the macOS Keychain via AI Tools.
- **Activity log**: live SSE event stream (device connect/disconnect, agent, webhooks, server).

## UiAutomator2 bridge (Android, opt-in)
- Start/Stop per device (`POST /devices/{id}/bridge/start|stop`, `GET /devices/{id}/bridge`). While running,
  UI tree and gestures (tap, long-press, swipe, double-tap, drag, **pinch**) go through the Appium
  UiAutomator2 server on the phone; if a call fails it falls back to plain adb. Stopped = unchanged behaviour.
- If `io.appium.uiautomator2.server` and `.server.test` are already installed on the phone they are used
  as they are (no install, no version check, nothing downloaded). Only when a package is missing are the
  APKs needed locally (`~/.phoneforge/uia2`): `GET /bridge/assets`, and `POST /bridge/assets/download`
  fetches them from GitHub on explicit request only (otherwise `409 BRIDGE_ASSETS_MISSING`).
- Bridges are stopped on server shutdown. Never started automatically.

## Apps & Testing
- **Apps**: search installed packages, Launch / Stop.
- **Testing**: JSON DSL editor + run → per-step pass/fail/skip chips, run history. New DSL actions:
  double_tap, drag, open_url, dismiss_keyboard, set_clipboard, install_app, uninstall_app,
  grant_permission, revoke_permission, wait_for, scroll_until_visible.
- **Test projects** (`/api/v1/tests`, stored in `~/.phoneforge/tests`): projects hold scripts written in the
  readable **`.mob`** format (or the JSON DSL); validate (`POST /tests/validate`), run on a device
  (`POST /tests/projects/{id}/scripts/{name}/run`) and browse run history (`GET /tests/runs`).
  `type_secret` steps resolve secret ids from the Keychain-backed credential store.
- **iOS Simulators**: booted simulators listed as `sim-<udid>`; `/api/v1/simulators` (list, boot, shutdown, WDA start/stop/status/logs); simctl-backed screenshot/apps/install/open URL/logs; WDA (simulator build via `scripts/build-wda.sh --sim`, ports 8200+/9200+) for input and UI tree; "iOS Simulators" entry under Integrations; MCP `list_simulators`/`boot_simulator`/`shutdown_simulator`.
- **Stable device list**: local Android, then iOS, then remote; by name then id.

## OCR and web automation
- **OCR** (`/api/v1/ocr`): `POST /ocr/devices/{id}` recognizes on-screen text (Apple Vision via a small Swift helper
  compiled on first use, tesseract fallback) and returns lines/words with pixel boxes; `/find` and `/tap`
  locate text (substring, `exact`, `regex`, `index`) and tap its centre. The helper is warmed up in the background
  at server start. The device view has an **OCR** tool: boxes are drawn over the live screen and listed; click one to tap it.
- **Web automation** (`/api/v1/web`, Android only): drives Chrome / debuggable WebViews over CDP via adb forwarding —
  targets, navigate, eval, click, type, wait, html, screenshot. The device view has a **Web** tool (targets,
  URL + Go, selector + Click/Type, JS eval). Sessions are closed on shutdown.
- **DSL actions**: `ocr_tap` (alias `tap_text_ocr`; `text`, `index`, `exact`, `regex`), `assert_text_ocr`
  (alias `wait_for_text_ocr`; polls until `timeout_ms`), `ocr` (alias `read_screen_text`; text in the step result,
  optional `storeAs`), and `web_navigate`, `web_click`, `web_type`, `web_eval`, `web_wait`, `web_assert_text`
  (`selector` is a CSS string). Web actions fail with a clear error when web automation is not configured or the device is not Android.
- **MCP tools**: `ocr_screen`, `ocr_tap`, `web_targets`, `web_navigate`, `web_eval`, `web_click`, `web_type`.

## Integrations (all working)
- **AI Configuration** (`/api/v1/ai`): detect external coding-agent CLIs, connect/disconnect PhoneForge's MCP server in
  their configs, per-task agent type settings, and a run console (external agents stream over SSE; the built-in agent
  runs through the existing agent endpoint).
- **AI Tools**: manage LLM provider keys (Keychain; never shown). Agent uses them after restart.
- **API Server**: REST + MCP URLs, copy buttons, MCP client config snippet.
- **Webhooks**: full CRUD, event filters, HMAC-SHA256 signatures, delivery status, Test.
- **Mobile Farms**: BrowserStack + Sauce Labs real device listing; AWS Device Farm credential storage
  (listing needs the AWS SDK — honestly marked). Credentials in Keychain, never shown.
- **Credentials / App Cards**: secrets in macOS Keychain; per-app login cards for the agent.
- **Cloud Agents**: honest "coming soon".
- **Share my devices**: modal explaining how to expose devices to other nodes (bind + token).

## iOS apps, crash reports and debugging
- **iOS install/uninstall (real devices)**: `POST /devices/{id}/install-app` (multipart `file` or JSON `{path}`; `.ipa` or `.app`, must be
  signed for the device; zipconduit) and `DELETE /devices/{id}/apps/{bundleId}` (installation proxy). App list now includes
  `version` and `build` (`version`/`build` omitted when unknown; Android lists package names only).
- **Crash reports** (`internal/crashreport`): `GET /devices/{id}/crashes?app=&since=<rfc3339>&limit=` (newest first, metadata parsed from the
  `.ips` header/body: bundle id, app, time, exception type, signal, kind crash/jetsam/other), `GET /devices/{id}/crashes/{name}` (full text),
  `DELETE /devices/{id}/crashes/{name}` (iOS only). iOS device: crash-report copy service. iOS Simulator: host DiagnosticReports filtered by the
  simulator udid. Android: `logcat -b crash -d` + `dumpsys dropbox` (`data_app_crash`, native crash, ANR) + tombstones when readable (root only).
  MCP `get_crash_reports`; the device view has a **Crash Reports** tool (list, filter, viewer, copy, delete on iOS).
- **lldb debugging** (`internal/debugger`, `/api/v1/debug`): attach to a running iOS app or launch it suspended under the debugger on a
  simulator (`simctl launch --wait-for-debugger` + `process attach`) or a real device (CoreDevice: `devicectl device process launch --start-stopped`
  + lldb `device select` / `device process attach`). Breakpoints (file:line, symbol, condition), continue/step over/into/out/interrupt,
  backtrace, variables, expression eval, detach. MCP `debug_attach`, `debug_breakpoint`, `debug_step`, `debug_eval`, `debug_state`, `debug_detach`.
  Android debugging returns a clear "not supported yet" error. See docs/debugging.md.

## Platform / API
- Single Go server: REST `/api/v1` (~55 routes plus `/ai`, `/ocr`, `/web`), embedded **MCP server** `/mcp` (36 tools: devices, gestures incl. pinch, bridge start/stop/status, test projects/scripts/run/runs, OCR, web) for
  Claude Code, SSE `/events`, agent SSE.
- **`phoneforge` CLI** (`cmd/phoneforge`, `scripts/install-cli.sh`): terminal client for the REST API - devices, screenshot, compact UI tree, tap by coords/text/id/OCR, swipe, type, apps/install, OCR, wait, DSL/test run, logs, record, crashes, bridge, simulators, streamed AI `agent`; `--json`, exit codes 0-4. See docs/cli.md.
- **Hardened security**: CORS does not reflect arbitrary Origins; no Allow-Private-Network; optional
  `-token` for non-loopback; all secrets in the OS Keychain.

## Honest limits / next
- iOS live control beyond the WDA bridge: Wi-Fi-only devices, iOS 17.0-17.3 (root kernel tunnel).
- AWS Device Farm live listing (AWS SDK), cloud device live streaming, Cloud Agents (CI).
- Desktop app (Wails v3, `PhoneForge.app`): built and verified standalone/attach on this Mac; ad-hoc signed only (no notarization), tray/menu verified by menu-bar inspection only.
- Agent not yet run against a real LLM here (no key set); unit-tested with a fake client.
- lldb debugging is unit-tested against a fake lldb and the real lldb's static commands; attaching to a live process was not verified on the
  build Mac (Developer mode disabled there) and real-device attach needs a development-signed app. Android debugging is not implemented.
