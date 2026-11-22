**English** | [Türkçe](README_tr.md)

# Pocketlab

*Formerly PhoneForge.*

[![CI](https://github.com/hakanngul/phoneforge/actions/workflows/ci.yml/badge.svg)](https://github.com/hakanngul/phoneforge/actions/workflows/ci.yml)
[![Release](https://github.com/hakanngul/phoneforge/actions/workflows/release.yml/badge.svg)](https://github.com/hakanngul/phoneforge/actions/workflows/release.yml)
[![Latest release](https://img.shields.io/github/v/release/hakanngul/phoneforge)](https://github.com/hakanngul/phoneforge/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A local tool that lets AI coding agents and humans see and drive real iOS and Android devices, iOS simulators and web pages.
It runs as one Go server with a web UI, a REST API, an MCP server and a CLI.

Pocketlab is original, clean-room software written in Go (desktop app: Wails v3). It uses open components (adb, go-ios,
[appium/WebDriverAgent](https://github.com/appium/WebDriverAgent)) and is not affiliated with any commercial product.

## Features

**Devices and mirroring**
- Android over adb (no on-device agent required), iOS real devices over USB, iOS simulators, and remote Pocketlab nodes added by address.
- Live mirror with FPS control, a multi-device grid, click-to-tap, and an Element Picker that inspects the UI tree.
- Gestures: tap, double-tap, long-press, swipe, drag, pinch, typing, hardware keys, open URL, clipboard, keyboard dismiss.
- App install, uninstall, launch and stop; runtime permissions; log streaming; screen recording.
- OCR of on-screen text (Apple Vision on macOS, tesseract fallback) with find and tap-by-text.
- Chrome and debuggable WebView automation over CDP on Android (navigate, eval, click, type).

**Automation and testing**
- A JSON DSL (version 0.2) and the readable `.mob` script format, with selectors, waits, assertions, params and secrets.
- Test projects, a test runner and run history, from the UI, the REST API or `pocketlab run`.

**AI agents**
- An in-app chat box that drives the device through Claude Code, Codex, Gemini, Cursor, Kiro or kiro-cli, or a built-in agent (Anthropic API key or local Ollama).
- An MCP server with 48 tools at `/mcp`, and six Claude Code skills packaged as a plugin.

**Debugging**
- Crash reports for Android and iOS devices and simulators.
- lldb sessions for iOS (attach or launch, breakpoints, stepping, eval), macOS only.

**Interfaces**
- Web UI, REST API (`/api/v1`), MCP (`/mcp`), the `pocketlab` CLI, and a native desktop app.

## Platform support

| | macOS | Linux | Windows (new, not yet tested on hardware) |
|---|---|---|---|
| Server, CLI, web UI, MCP | yes | yes | yes |
| Desktop app | yes (signed, notarized) | no | experimental, unsigned zip |
| Android real devices | yes | yes | yes (`adb.exe` in `PATH`) |
| iOS real devices | yes | yes (usbmuxd) | yes, needs Apple Devices or iTunes for USB; install WebDriverAgent once from a Mac |
| iOS simulators | yes | no | no |
| lldb debugger, native USB capture, Vision OCR | yes | no | no |

On non-macOS hosts, simulator features return "iOS simulators require macOS".

## Install

**Desktop app (macOS).** Download `Pocketlab-<version>-macos-<arch>.dmg` (arm64 or amd64) from
[Releases](https://github.com/hakanngul/phoneforge/releases/latest) and drag `Pocketlab.app` to Applications.
Release builds are signed and notarized. A `.zip` of the app is also published.

**CLI and server.** Download `pocketlab_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) for macOS, Linux or Windows
(amd64, arm64), extract it, and run `./pocketlabd` and `./pocketlab`. `SHA256SUMS.txt` is attached to every release.
The experimental Windows desktop app is `Pocketlab-<version>-windows-amd64.zip` and needs the WebView2 runtime.

**CLI install script (from a clone).**

```sh
scripts/install-cli.sh          # builds bin/pocketlab and links it into ~/.local/bin or /usr/local/bin
scripts/install-cli.sh --copy   # copy instead of symlink
```

**Build from source.** Requires Go 1.27 or newer (see `go.mod`).

```sh
go build -o bin/pocketlabd ./cmd/pocketlabd
go build -o bin/pocketlab ./cmd/pocketlab
scripts/build-desktop.sh --dmg   # macOS desktop app, needs Xcode command line tools
```

## Quick start

```sh
./pocketlabd                     # serves the web UI, REST API and MCP on 127.0.0.1:8700
open http://127.0.0.1:8700        # or use the desktop app, which runs the same server
pocketlab devices
```

Android needs `adb` in `PATH` and USB debugging enabled on the phone. Connect a device and it appears in the UI.

**iOS real devices** are controlled through Pocketlab's own signed WebDriverAgent (WDA). Everything happens inside the app (needs Xcode and an Apple ID,
free is fine): select the iPhone and click the bridge toggle. When the helper was never built it reads **Build WDA**; Pocketlab builds it
on your Mac with your own Apple team (the pinned source ships inside the app, no repo or terminal needed), then installs and starts it.
Trust the developer profile on the phone when asked. Headless: `pocketlab ios build-wda [--sim] [--install <device>] [--team <id>]`.
Prerequisites and troubleshooting: [docs/ios-setup.md](docs/ios-setup.md).
iOS simulators on macOS are listed automatically once booted.

## Using with AI agents

Register the MCP server (start `pocketlabd` first):

```sh
claude mcp add --transport http pocketlab http://127.0.0.1:8700/mcp
```

Or install the Claude Code plugin, which also registers the MCP server and loads the skills:

```
/plugin marketplace add hakanngul/phoneforge
/plugin install pocketlab
```

Only the skills: `scripts/install-skills.sh` (symlinks `skills/pocketlab-*` into `~/.claude/skills`; `--copy`, `--uninstall` and
`--dry-run` are supported). The skills cover device control, smoke tests, accessibility audits, store screenshots, test authoring and
the CLI. Details: [docs/skills.md](docs/skills.md).

**In-app chat box.** Pick an agent in the AI Configuration panel: Claude Code, OpenAI Codex CLI, Gemini CLI, Cursor Agent, Kiro (IDE, connect
only), Kiro CLI, or the built-in agent. **Connect** registers Pocketlab's MCP server with the selected tool. The built-in agent uses an Anthropic
API key or a local Ollama model (`OLLAMA_HOST`, `OLLAMA_MODEL`). Some agents are implemented but not yet exercised end to end; see
[BACKLOG.md](BACKLOG.md) for what has been verified.

## CLI examples

```sh
pocketlab devices
pocketlab -d iphone screenshot -o home.png
pocketlab -d sm-g tap --text "Settings"
pocketlab tree --filter "Wi-Fi"
pocketlab swipe up
pocketlab type "hello"
pocketlab launch com.example.app
pocketlab install app.apk
pocketlab ocr --find "Sign in"
pocketlab run login.mob
pocketlab crashes --app com.example.app
pocketlab agent "open Settings and turn on Dark Mode"
```

`-d` matches device id, serial, name or a unique prefix. `--json` gives machine-readable output. Exit codes: 0 ok, 1 error, 2 usage,
3 device not found or ambiguous, 4 server unreachable. All commands: [docs/cli.md](docs/cli.md).

**Server lifecycle.** Every start and stop is recorded in `~/.pocketlab/server-events.jsonl` with its reason (shutdown API and who
asked, signal, crash, port in use, or an unexpected exit such as `kill -9`). See it with `pocketlab server history`, in the web UI's
**Server** panel, or in the desktop app (Server menu: start, stop, restart). `pocketlab server start` launches `pocketlabd` detached.

## Writing tests

A `.mob` script is one statement per line:

```
# Param: email = qa@example.com
# Alerts: accept

app "com.example.app" fresh
wait_for ~"Sign in" timeout:10000
type ~"Email" -> "${email}" clear
type_secret ~"Password" -> "login-password"
tap "Sign in" type:button
assert_exists ~"Welcome" timeout:5000
scroll down to "Log out"
tap "Log out"
assert_not_exists ~"Welcome" timeout:3000
```

Run it with `pocketlab run login.mob`, from a test project in the UI, or through the MCP `test_run` / `execute_dsl` tools.
`type_secret` takes a secret id from the credential store, never the password itself. Full syntax is in
[skills/pocketlab-test-authoring/references/mob-syntax.md](skills/pocketlab-test-authoring/references/mob-syntax.md).

## Configuration

| Variable | Meaning |
|---|---|
| `POCKETLAB_ADDR` | desktop app listen address (default `127.0.0.1:8700`); `pocketlabd` uses `-addr` |
| `POCKETLAB_HOME` | state directory (default `~/.pocketlab`) |
| `POCKETLAB_URL`, `POCKETLAB_DEVICE`, `POCKETLAB_TOKEN` | CLI defaults: server URL, target device, API token (`pocketlabd -token` sets the server side) |
| `PL_MJPEG_FPS`, `PL_MJPEG_SCALE`, `PL_MJPEG_QUALITY` | iOS WDA MJPEG stream (defaults 60 / 25 / 50; ranges 1-60 / 1-100 / 1-100) |
| `PL_ANDROID_AUTOBRIDGE=0` | do not start the Android UiAutomator2 bridge on first UI-tree read |
| `PL_SIM_AUTOWDA=0` | do not start WDA lazily on simulators |
| `PL_TEAM_ID`, `PL_BUNDLE_PREFIX` | signing overrides for the in-app WDA build (a Team ID entered in the UI is saved in `~/.pocketlab/settings.json`; `PL_TEAM_ID` wins); `PL_WDA_HOME`, `PL_WDA_TARBALL` relocate the WDA work dir / source tarball |
| `ANTHROPIC_API_KEY`, `ANTHROPIC_MODEL`, `OLLAMA_HOST`, `OLLAMA_MODEL` | built-in agent providers |

`pocketlabd` flags: `-addr`, `-adb`, `-token`, `-webdir`.

Upgrading from PhoneForge: on first start the server, CLI and desktop app move `~/.phoneforge` to `~/.pocketlab`
(when `~/.pocketlab` does not exist yet). The old `PHONEFORGE_*` / `PF_*` environment variables and the
`X-PhoneForge-*` HTTP headers are still honoured as fallbacks; the new `POCKETLAB_*` / `PL_*` names win when both are set.
Re-connecting an AI agent in the AI Configuration page replaces its old `phoneforge` MCP entry with `pocketlab`.

## Docs

- [docs/cli.md](docs/cli.md): CLI commands, device resolution, exit codes
- [docs/ios-setup.md](docs/ios-setup.md): WebDriverAgent setup for iPhones
- [docs/desktop.md](docs/desktop.md): the Wails desktop app
- [docs/debugging.md](docs/debugging.md): crash reports and lldb sessions
- [docs/skills.md](docs/skills.md): Claude Code skills and plugin
- [docs/release.md](docs/release.md): release builds, signing and notarization
- [FEATURES.md](FEATURES.md): detailed feature list
- [RUN.md](RUN.md): running notes

## Roadmap

Open work, verification status and known gaps are tracked in [BACKLOG.md](BACKLOG.md).

## Contributing and license

Issues and pull requests are welcome. Run `go test ./...` before sending a change. Pocketlab is released under the [MIT License](LICENSE).
