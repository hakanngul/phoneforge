# Running Pocketlab (MVP)

## Headless server (REST + MCP)
```bash
cd pocketlab
go run ./cmd/pocketlabd -addr 127.0.0.1:8700
```
- Web UI:  http://127.0.0.1:8700/
- REST:    http://127.0.0.1:8700/api/v1/health
- MCP:     http://127.0.0.1:8700/mcp   (Streamable HTTP)

Flags: `-addr` listen addr, `-token` require X-API-Key/Bearer for non-loopback, `-adb` adb path,
`-webdir` web UI dir (default ./frontend).

## Connect an Android device
- A physical phone with USB debugging on, or an emulator. Verify: `adb devices`.
- Then the device appears in `GET /api/v1/devices` and the UI sidebar.

## Wire into Claude Code (MCP)
Add to your MCP client config:
```json
{ "mcpServers": { "pocketlab": { "type": "http", "url": "http://127.0.0.1:8700/mcp" } } }
```
Tools: list_devices, get_device, get_screenshot, ui_tree, tap, swipe, type_text, press_key,
launch_app, stop_app, execute_dsl, double_tap, long_press, drag, open_url, dismiss_keyboard,
install_app (server-local path), uninstall_app, set_permission, get_clipboard, set_clipboard.

## Extra device routes (`/api/v1/devices/{id}/...`)
Optional capabilities; devices lacking one answer `501 {"code":"UNSUPPORTED"}`.
`GET stream.h264` (live H.264), `POST double-tap | drag | pinch | open-url | dismiss-keyboard`,
`GET|POST clipboard`, `POST install-app` (multipart `file` .apk, or JSON `{"path"}` on the server),
`DELETE apps/{bundleId}`, `POST permissions`, `GET logs` (SSE), `GET recording?seconds=N` (mp4).
Remote nodes proxy all of these.

## UiAutomator2 bridge (Android)
Optional, off by default. `POST /api/v1/devices/{id}/bridge/start` launches the UiAutomator2 server on the
phone and routes UI tree + gestures through it; `.../bridge/stop` returns to plain adb; `GET .../bridge`
reports `{supported, running, port, sessionId, since, lastError, installed}`. If the two
`io.appium.uiautomator2.server[.test]` packages are already on the phone they are used as-is. If not, the
APKs must be in `~/.pocketlab/uia2` (`GET /api/v1/bridge/assets`); `POST /api/v1/bridge/assets/download`
fetches them from GitHub (explicit action only), otherwise start answers `409 BRIDGE_ASSETS_MISSING`.
MCP: `start_bridge`, `stop_bridge`, `bridge_status`.

## Test projects and `.mob` scripts (`/api/v1/tests`)
Data lives in `~/.pocketlab/tests/<project>/`. `GET|POST /tests/projects`, `GET|PUT|DELETE
/tests/projects/{id}/scripts/{name}` (`.mob` or `.json`), `POST /tests/validate[?projectId=]`,
`POST /tests/projects/{id}/scripts/{name}/run` with `{"deviceId":"..."}`, `GET /tests/runs[?projectId=&limit=]`,
`GET /tests/runs/{id}`. `type_secret` ids come from the credential store. MCP: `test_list_projects`,
`test_list_scripts`, `test_run`, `test_list_runs`.

## Tests
```bash
go test ./...
```

## Status
MVP: Android via adb (screencap / uiautomator dump / input). Hardened CORS (no Origin reflection,
no Allow-Private-Network). iOS live control ships via the WDA bridge (see below); a native Wails v3 shell is future work;
the current UI is a plain web app the server serves at /.

## AI agent (optional)
`export ANTHROPIC_API_KEY=...` (or run Ollama) before starting, then use the Agent bar / execute_dsl.
See FEATURES.md.

## iOS live control

Real iPhones are controlled through Pocketlab's own signed WebDriverAgent (WDA). The UI builds it for you
(needs Xcode): select the iPhone and click the bridge toggle, which reads **Build WDA** the first time (also
`pocketlab ios build-wda`; `scripts/build-wda.sh` remains as a developer alternative). It then installs
(explicit, asks first) and starts WDA. Once the chip reads `Bridge: running` the Live view switches to an MJPEG stream and taps, swipes,
Home, typing, Element Picker, OCR and app launch work through WDA. Full prerequisites and troubleshooting:
[docs/ios-setup.md](docs/ios-setup.md).
