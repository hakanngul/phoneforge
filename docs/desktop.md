# PhoneForge desktop app (macOS)

`PhoneForge.app` runs the same server as `phoneforged` (REST `/api/v1`, MCP `/mcp`, web UI) in-process and
shows the UI in a native window built with Wails v3 (v3.1.0, pinned in `go.mod`).

## Build

```sh
scripts/build-desktop.sh          # -> dist/PhoneForge.app (ad-hoc signed)
scripts/build-desktop.sh --dmg    # also dist/PhoneForge.dmg
scripts/build-desktop.sh --dev    # enable View > Toggle DevTools
```

Requirements: Xcode command line tools (cgo), Go 1.27+, macOS 13+. The script renders `desktop/icon.svg` and
`desktop/tray.svg` with `sips`, builds the `.icns` with `iconutil`, builds the binary, writes `Info.plist`
(bundle id `com.phoneforge.desktop`, camera and local-network usage strings) and signs with
`codesign -s -` plus a camera entitlement. Plain `go build ./...` also compiles the desktop target on macOS.
Wails v3 is alpha-era software; the build was verified with Wails v3.1.0 on macOS 27.2.

## Behaviour

- Default address `127.0.0.1:8700`; override with `--addr` or `PHONEFORGE_ADDR`.
- If a PhoneForge server (`GET /api/v1/health` reports `service: phoneforge`) already answers there, the
  window attaches to it and the title reads "PhoneForge — connected to running server" (plus a toast).
  No second server is started and quitting the app does not stop that server.
- If the port is used by something else, a free port is chosen.
- Otherwise the server starts in-process; on quit it is shut down cleanly (WDA bridges, simulators, debuggers).
- `--webdir <dir>` serves the UI from disk instead of the embedded copy (development).
  Other flags: `--adb`, `--token`, `--no-tray`. State dir: `$PHONEFORGE_HOME` (default `~/.phoneforge`).
- Window size/position are stored in `~/.phoneforge/desktop.json`.
- Menu-bar icon: Open PhoneForge, server status, Quit. Closing the window hides it (app keeps running);
  with `--no-tray` closing quits. Dock click reopens the window.
- Menus: PhoneForge (About, Settings… opens Integrations > AI Configuration, Quit), Edit, View
  (Reload, zoom, full screen, DevTools in `--dev` builds), Window, Help (GitHub docs, Open in Browser).

## Permissions

The native 60 fps iPhone capture path needs camera access; running inside the app means macOS asks under
the app's own identity (`NSCameraUsageDescription`). Ad-hoc signing changes identity per build, so the
prompt may reappear after rebuilding. No notarization: first launch on another Mac needs right-click > Open.

## Testing without touching a running server

```sh
PHONEFORGE_HOME=$(mktemp -d) PF_WDA_HOME=$HOME/.phoneforge/wda PHONEFORGE_ADDR=127.0.0.1:8791 \
  dist/PhoneForge.app/Contents/MacOS/phoneforge-desktop
```
