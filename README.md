# PhoneForge

A clean-room, original mobile-device automation tool: a local HTTP + MCP service that lets an AI
coding agent drive real Android devices (screenshot, UI tree, tap/swipe/type) via a JSON DSL.

Built from scratch in Go (+ Wails v3 for the desktop UI). Uses only open components (adb; later
go-ios + WebDriverAgent for iOS). **Not affiliated with, and contains no code or branding from, any
commercial product.** Architecture inspired by the documented design of mobile-automation tools.

- Bundle id: `dev.phoneforge.app` · default API/MCP port: `127.0.0.1:8700`
- MVP platform: Android via adb (no on-device agent required).

## Layout
- `internal/device`  — Device interface + shared types (stable contract)
- `internal/androiddrv` — Android driver (adb: screencap, uiautomator dump, input)
- `internal/registry` — device discovery/registry
- `internal/dsl`     — JSON DSL engine (steps, predicates, assertions, settle)
- `internal/httpapi` — REST API (/api/v1)
- `internal/mcpserver` — embedded MCP server (/mcp) proxying to the API
- `internal/ocr`     — on-screen text recognition (Apple Vision helper / tesseract) + `/api/v1/ocr`
- `internal/webauto` — Chrome/WebView automation over CDP (Android) + `/api/v1/web`
- `internal/aiconfig` — AI Configuration: external agent CLIs, MCP connect, run console + `/api/v1/ai`
- `cmd/phoneforged`  — headless server (like `serve`)
- `frontend` + Wails app — desktop UI (added later)

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
