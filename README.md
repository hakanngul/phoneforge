# PhoneForge

[![CI](https://github.com/hakanngul/phoneforge/actions/workflows/ci.yml/badge.svg)](https://github.com/hakanngul/phoneforge/actions/workflows/ci.yml)
[![Release](https://github.com/hakanngul/phoneforge/actions/workflows/release.yml/badge.svg)](https://github.com/hakanngul/phoneforge/actions/workflows/release.yml)

A clean-room, original mobile-device automation tool: a local HTTP + MCP service that lets an AI
coding agent drive real Android devices (screenshot, UI tree, tap/swipe/type) via a JSON DSL.

Built from scratch in Go (+ Wails v3 for the desktop UI). Uses only open components (adb; later
go-ios + WebDriverAgent for iOS). **Not affiliated with, and contains no code or branding from, any
commercial product.** Architecture inspired by the documented design of mobile-automation tools.

- Bundle id: `dev.phoneforge.app` · default API/MCP port: `127.0.0.1:8700`
- MVP platform: Android via adb (no on-device agent required).

## Platform support

| Feature | macOS | Windows | Linux |
|---|---|---|---|
| Server, CLI, web UI, MCP | yes | yes | yes |
| Android (adb) | yes | yes | yes |
| iOS real devices (go-ios + WDA) | yes | yes (needs Apple Devices/iTunes for USB; install WDA once from a Mac) | yes (usbmuxd) |
| iOS simulators, lldb debugger, native USB capture, Vision OCR | yes | no (macOS only) | no |
| Desktop app (Wails v3) | yes | experimental, unsigned zip | no |

On non-macOS hosts the simulator features return "iOS simulators require macOS" and the simulator discovery does not start.

## Layout
- `internal/device`  — Device interface + shared types (stable contract)
- `internal/androiddrv` — Android driver (adb: screencap, uiautomator dump, input)
- `internal/registry` — device discovery/registry
- `internal/dsl`     — JSON DSL engine (steps, predicates, assertions, settle)
- `internal/httpapi` — REST API (/api/v1)
- `internal/mcpserver` — embedded MCP server (/mcp) proxying to the API
- `internal/ocr`     — on-screen text recognition (Apple Vision helper / tesseract) + `/api/v1/ocr`
- `internal/webauto` — Chrome/WebView automation over CDP (Android) + `/api/v1/web`
- `internal/aiconfig` — AI Configuration: external agent CLIs (Claude Code, Codex, Gemini, Cursor, Kiro IDE, kiro-cli), MCP connect, run console + `/api/v1/ai`
- `cmd/phoneforged`  — headless server (like `serve`)
- `internal/server` — reusable server wiring (used by `phoneforged` and the desktop app)
- `cmd/phoneforge-desktop` — native macOS app (Wails v3), see [docs/desktop.md](docs/desktop.md)
- `frontend` — web UI (embedded into the binaries via `frontend/embed.go`)

## Desktop app (macOS)
`scripts/build-desktop.sh [--dmg]` builds `dist/PhoneForge.app` (Wails v3, ad-hoc signed): the same server runs
in-process and the UI opens in a native window, or attaches to a server already running on `127.0.0.1:8700`.
See [docs/desktop.md](docs/desktop.md).

## Releases

Pushing a `v*` tag builds the macOS app (`.dmg`/`.zip`, arm64 + amd64), CLI/server archives for macOS, Linux (`.tar.gz`) and Windows (`.zip`, amd64 + arm64), an experimental Windows desktop zip and
`SHA256SUMS.txt`, and publishes a GitHub Release. See [docs/release.md](docs/release.md) (signing and notarization
secrets are optional; builds are ad-hoc signed by default).

## Claude Code skills and plugin

`skills/` holds six agent skills (device control, smoke test, accessibility audit, store screenshots, test
authoring, CLI). Install them as a plugin, which also registers the MCP server `phoneforge`:

```
/plugin marketplace add hakanngul/phoneforge
/plugin install phoneforge --marketplace hakanngul/phoneforge
```
or only the skills: `scripts/install-skills.sh`. Details in [docs/skills.md](docs/skills.md).

## iOS live control

Real iPhones are controlled through PhoneForge's own signed WebDriverAgent (WDA). Build it once with
`scripts/build-wda.sh`, then in the UI select the iPhone and click **Install helper** (explicit, asks first),
then **Start WDA**. Once the chip reads `WDA: running` the Live view switches to an MJPEG stream and taps, swipes,
Home, typing, Element Picker, OCR and app launch work through WDA. Full prerequisites and troubleshooting:
[docs/ios-setup.md](docs/ios-setup.md).

## Command-line client

`phoneforge` is a CLI for a running server: `devices`, `screenshot`, `tree`, `tap`, `swipe`, `type`, `apps`,
`launch`, `install`, `ocr`, `wait`, `dsl`, `run`, `logs`, `record`, `crashes`, `bridge`, `sims`, `agent`, ...

```sh
scripts/install-cli.sh        # builds bin/phoneforge and links it into ~/.local/bin or /usr/local/bin
phoneforge devices
phoneforge -d sm-g tap --text "Settings"
```

See [docs/cli.md](docs/cli.md) for all commands, device resolution and exit codes.

Backlog and known gaps: [BACKLOG.md](BACKLOG.md)

## Kiro and kiro-cli
AI Configuration detects the Kiro IDE (`Kiro.app` or `kiro` on PATH) and `kiro-cli`. **Connect** adds
`"phoneforge": {"url": "http://127.0.0.1:8700/mcp"}` to `~/.kiro/settings/mcp.json` (other servers kept, original backed up to
`mcp.json.phoneforge-bak`); **Disconnect** removes it. The IDE is connect-only. `kiro-cli` can also be picked as the Automation or
Testing agent; headless runs need a `KIRO_API_KEY` in the environment (Kiro Pro and above). Install the CLI with
`curl -fsSL https://cli.kiro.dev/install | bash`. This integration follows the official docs and has not been run against a real Kiro install.
