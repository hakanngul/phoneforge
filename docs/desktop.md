# Pocketlab desktop app (macOS)

`Pocketlab.app` runs the same server as `pocketlabd` (REST `/api/v1`, MCP `/mcp`, web UI) in-process and
shows the UI in a native window built with Wails v3 (v3.1.0, pinned in `go.mod`).

## Build

```sh
scripts/build-desktop.sh          # -> dist/Pocketlab.app (ad-hoc signed)
scripts/build-desktop.sh --dmg    # also dist/Pocketlab.dmg
scripts/build-desktop.sh --dev    # enable View > Toggle DevTools
PL_VERSION=1.2.3 scripts/build-desktop.sh   # stamp a version (see docs/release.md)
```

Requirements: Xcode command line tools (cgo), Go 1.27+, macOS 13+. The script renders `desktop/icon.svg` and
`desktop/tray.svg` with `sips`, builds the `.icns` with `iconutil`, builds the binary, writes `Info.plist`
(bundle id `com.pocketlab.desktop`, camera and local-network usage strings) and signs with
`codesign -s -` plus a camera entitlement. Plain `go build ./...` also compiles the desktop target on macOS.
Wails v3 is alpha-era software; the build was verified with Wails v3.1.0 on macOS 27.2.

## Windows (experimental)

Releases include `Pocketlab-<version>-windows-amd64.zip` with `pocketlab-desktop.exe` (built with `CGO_ENABLED=0 go build
-ldflags "-H windowsgui" ./cmd/pocketlab-desktop`). It is unsigned and not yet tested on a device; it needs the WebView2 runtime.
Simulators, debugger and native capture are unavailable (macOS only).

## Behaviour

- Default address `127.0.0.1:8700`; override with `--addr` or `POCKETLAB_ADDR`.
- If a Pocketlab server (`GET /api/v1/health` reports `service: pocketlab`) already answers there, the
  window attaches to it and the title reads "Pocketlab — connected to running server" (plus a toast).
  No second server is started and quitting the app (menu, tray, Cmd+Q) does not stop that server.
- **Quit Pocketlab** button (top bar, with a confirm) calls `POST /api/v1/server/shutdown`: the server stops and
  the port is released, and the desktop app quits too, whether it owns the server or is attached to one
  (attached apps notice via `shuttingDown` on `/health`). The same button works in the browser UI, which then
  shows "Pocketlab stopped". `pocketlab server stop` does the same from a shell, and `pocketlabd` exits 0.
  The endpoint needs the `X-Pocketlab-Action: shutdown` header (forces a CORS preflight, which is never granted)
  and rejects a foreign `Origin` or a non-loopback/non-IP `Host` (anti-CSRF / DNS rebinding).
- If the port is used by something else, a free port is chosen.
- **Server menu** (also in the menu-bar icon): Start server, Stop server, Restart server, Server Status & History.
  These control the server this app owns (an attached external server is stopped through its shutdown API, recorded
  as origin `desktop-menu`). "Quit Pocketlab" keeps its meaning: stop the server and exit.
- If the server shown in the window disappears (crash, `kill`, `pocketlab server stop` from a shell while the app
  stays), the window switches to a local fallback page that states the last recorded reason and offers
  **Start server** and **Quit Pocketlab**; it reconnects by itself when a server answers again. The fallback is
  served by a loopback-only control listener with a random path token.
- The web UI's **Server** button (top bar) opens the same panel as in the browser: status, uptime, and the
  start/stop history. Every start and stop is written to `~/.pocketlab/server-events.jsonl` (last 500 events;
  `server.state` is a 10 s heartbeat used to detect exits nobody recorded, such as `kill -9`).
  API: `GET /api/v1/server/status`, `GET /api/v1/server/history?limit=50`; `POST /api/v1/server/shutdown` accepts an
  optional `X-Pocketlab-Origin` header (or JSON `{"origin","reason"}`) that is stored as the reason's origin.
- Otherwise the server starts in-process; on quit (menu, tray, Cmd+Q) it is shut down cleanly (WDA bridges, simulators, debuggers).
- `--webdir <dir>` serves the UI from disk instead of the embedded copy (development).
  Other flags: `--adb`, `--token`, `--no-tray`. State dir: `$POCKETLAB_HOME` (default `~/.pocketlab`).
- Window size/position are stored in `~/.pocketlab/desktop.json`.
- Menu-bar icon: Open Pocketlab, server status, Quit. Closing the window hides it (app keeps running);
  with `--no-tray` closing quits. Dock click reopens the window.
- Menus: Pocketlab (About, Settings… opens Integrations > AI Configuration, Quit), Edit, View
  (Reload, zoom, full screen, DevTools in `--dev` builds), Window, Help (GitHub docs, Open in Browser).

## Permissions

The native 60 fps iPhone capture path needs camera access; running inside the app means macOS asks under
the app's own identity (`NSCameraUsageDescription`). Ad-hoc signing changes identity per build, so the
prompt may reappear after rebuilding. No notarization: first launch on another Mac needs right-click > Open.

## Testing without touching a running server

```sh
POCKETLAB_HOME=$(mktemp -d) PL_WDA_HOME=$HOME/.pocketlab/wda POCKETLAB_ADDR=127.0.0.1:8791 \
  dist/Pocketlab.app/Contents/MacOS/pocketlab-desktop
```
