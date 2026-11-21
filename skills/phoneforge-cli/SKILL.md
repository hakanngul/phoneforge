---
name: phoneforge-cli
description: Use the `phoneforge` command-line tool to control Android/iOS devices from a shell - one-off taps, screenshots, UI tree dumps, app install/launch, .mob/DSL runs, crash reports, simulators - and to script it in shell scripts and CI. Use for "phoneforge cli", "from the terminal", "shell script for the phone", "CI device test", or in Turkish "terminalden telefonu kontrol et", "komut satırıyla", "phoneforge komutu", "betikle otomasyon".
---

# phoneforge CLI

A thin client for the PhoneForge server (default `http://127.0.0.1:8700`). Good for one step at a time in
unfamiliar apps, shell scripts and CI. When the MCP tools (`mcp__phoneforge__*`) are available and you already know
the screens, batching with `execute_dsl` is faster; the observe/act/verify rules are the same
(`phoneforge-device-control`). `phoneforge help` lists commands and `phoneforge help <command>` (or
`<command> --help`) prints exact flags; trust it over memory.

## Setup
```bash
phoneforge server status                  # is the server reachable? (exit 4 if not)
export PHONEFORGE_URL=http://127.0.0.1:8700      # only if not the default (or --server URL)
export PHONEFORGE_DEVICE=<id|name|prefix>        # default target; or pass -d/--device per call
export PHONEFORGE_TOKEN=<token>                  # only if the server requires an API token (sent as X-API-Key)
phoneforge devices                        # list ids  (--json for machines)
```
If the server is down, start it: `go run ./cmd/phoneforged -addr 127.0.0.1:8700` (or the `phoneforged` binary).

Global flags (before or after the command): `--server URL`, `-d/--device ID`, `--json`, `--timeout DUR`
(e.g. `30s`; every command has its own default otherwise). Use `--` before positional text that starts with
`-`: `phoneforge type -- "-5 degrees"`. Prefer `--json` whenever a script parses the output.

### Picking the device
With no `-d`, the only connected device is used; with several connected it fails (exit 3) and lists them.
`-d` is matched case-insensitively against id, serial, name and model: an exact match wins, then a unique
prefix, then a unique substring (`-d sm-g` selects `SM-G780G`). If the value matches several devices it is
ambiguous (exit 3, candidates listed): `-d iphone` is ambiguous when a booted iPhone simulator sits next to
a real iPhone, so use a longer name (`-d "iphone 15"`) or the full id.

### Exit codes
`0` ok, `1` error (command failed, `wait` timed out, `ocr --find` had no match, DSL/test did not pass),
`2` usage error, `3` device not found or ambiguous, `4` server unreachable (start `phoneforged`).

## Commands
| Task | Command |
|---|---|
| Devices | `phoneforge devices`, `phoneforge device info` |
| Screenshot | `phoneforge screenshot -o shot.png` (default `./screenshot-<ts>.png`; `-o -` writes PNG to stdout, refused on a terminal) |
| UI tree | `phoneforge tree`, `tree --filter "Sign in"`, `--max-nodes N`, `--offscreen`, `--json` (raw hierarchy) |
| Tap | `phoneforge tap 540 1620`, `tap --text "Sign in" [--index N] [--ocr]`, `tap --id login_btn [--index N]` |
| Long press | `phoneforge longpress 540 800 [--ms 800]` |
| Swipe | `phoneforge swipe 540 1600 540 600 [--ms 300]`, `swipe up\|down\|left\|right` |
| Text and keys | `phoneforge type "hello"`, `phoneforge key home\|back\|enter\|delete\|volume_up\|volume_down` (the last two only where the driver supports them) |
| Apps | `phoneforge apps [--filter TEXT]`, `launch <id>`, `stop <id>`, `install <file.apk\|file.ipa\|dir.app>`, `uninstall <id>` |
| Deep link | `phoneforge open https://example.com` or `myapp://path` |
| OCR | `phoneforge ocr`, `ocr --find "Total" [--exact] [--regex] [--case-sensitive] [--words]` |
| Wait | `phoneforge wait --text "Welcome"` or `wait --id ID`, plus `[--gone] [--ocr] [--timeout 10s] [--interval 500ms]` |
| Scripts | `phoneforge dsl flow.json` (or `-` for stdin), `phoneforge run flow.mob [--project NAME]` |
| Diagnostics | `phoneforge logs [-f] [--seconds N] [--grep T]`, `record [--seconds N] [-o file.mp4]`, `crashes [--app B] [--limit N]`, `crash <name> [-o file]` |
| iOS bridge | `phoneforge bridge start\|stop\|status` (UiAutomator2 on Android, WebDriverAgent on iOS; iOS needs it for screenshots/tree/input; `start` can take a minute or more) |
| Simulators | `phoneforge sims`, `phoneforge sim boot <udid\|name>`, `phoneforge sim shutdown <udid\|name>` (name may be a unique prefix) |
| AI agent | `phoneforge agent "open Settings and turn on dark mode" [--model haiku] [--agent NAME] [--session ID] [--max-steps N]` |
| Misc | `phoneforge server status`, `phoneforge version`, `phoneforge help [command]` |

Coordinates are in the server tap endpoint's units: iOS points, Android pixels. Take them from the `@(x,y)`
tap centers in `phoneforge tree`, not from a screenshot's pixel size.

### Behaviour details
- **tap --text / --id** reads the UI tree and taps the element center. `--text` is a case-insensitive match
  on text/label, exact matches ranked before substring matches (clickable first); `--id` matches a resource id
  exactly or the part after `/` (`btn_ok`). `--index N` picks the Nth match (default 0). `--ocr` (with `--text`)
  falls back to on-screen text recognition when the tree has no match; it handles pixel-to-point scaling for you.
- **swipe up|down|left|right**: the direction is where the *finger* moves, across the middle of the screen.
  `swipe up` scrolls content down (reveals what is below); `swipe left` goes to the next page.
- **ocr** boxes are in screenshot pixels, which can be larger than the tap coordinate space on iOS. Do not
  feed them to `tap X Y`; use `tap --text T --ocr`. With `--find`, only matches are listed and exit is 1 if none.
- **wait** polls the tree (and OCR with `--ocr`, which needs `--text`) every 500 ms. Exit 0 when it appears
  (or, with `--gone`, disappears), exit 1 on timeout (default 10s).
- **logs** without `-f` collects for `--seconds` (default 3) and exits; `-f` follows until Ctrl-C.
  `--grep` is a case-insensitive line filter.
- **record** defaults to 10 seconds and `./recording-<ts>.mp4`.
- **install**: APK/IPA files are uploaded to the server, so the server need not see your path. An `.app`
  directory is installed from its path, so the server must run on the same machine.
- **dsl** posts the JSON document to the device and prints one line per step; exit 1 if it did not pass.
- **run** saves the `.mob` or `.json` script into a server-side test project (default name `cli`, created on
  first use; override with `--project`) under the file's base name, runs it on the device and prints
  per-step results; exit 1 if it did not pass. The script therefore shows up in the server's test projects.
- **agent** starts an AI run of type `automation` and streams it: plain lines are the agent's thoughts,
  `▸ tool(args)` are actions, `✓ done: ...` is the result; Ctrl-C cancels the run on the server. `--model` sets
  the model for this run (the server's automation model is changed briefly and restored right after start),
  `--agent` picks the agent CLI (default: the one chosen in AI settings), `--session` resumes an earlier Claude
  Code session. `--max-steps` applies only to the built-in agent. With `--json`, raw events print one per line.

## One-off loop (exploring)
```bash
phoneforge tree --filter "Search"          # find the node, read its @(x,y)
phoneforge tap --text "Search"             # or: tap 540 210
phoneforge type "running shoes"
phoneforge key enter
phoneforge wait --text "results"
phoneforge screenshot -o results.png       # then look at it
```
Check each command's exit status; a non-zero exit means the action failed (no match, timeout, device error).
Do not chain blindly with `;` when later steps depend on earlier ones; use `&&` or `set -e`.

## Script / CI pattern
```bash
#!/usr/bin/env bash
set -uo pipefail                              # no -e: we want artifacts even when the run fails
export PHONEFORGE_DEVICE="${DEVICE_ID:?set DEVICE_ID}"   # use the exact id so the match cannot be ambiguous
out=artifacts; mkdir -p "$out"
phoneforge server status || exit 4
phoneforge bridge start || true               # iOS needs it; on Android it is optional
phoneforge install build/app-debug.apk || exit 1   # APK/IPA are uploaded; an .app dir needs a local server
phoneforge run tests/smoke.mob --json | tee "$out/result.json"
rc=${PIPESTATUS[0]}                           # exit code of phoneforge, not of tee
phoneforge screenshot -o "$out/final.png" || true
phoneforge crashes --json > "$out/crashes.json" || true
exit "$rc"
```
- CI: run `phoneforged` as a background service, wait for `phoneforge server status` to succeed, set
  `PHONEFORGE_URL` (and `PHONEFORGE_TOKEN` if the server needs one) when the runner is remote, upload
  `artifacts/` always (`if: always()`).
- Keep device ids in env/CI variables, not in scripts. One job per device; do not run two scripts against the
  same device at once.
- Secrets: scripts reference stored secret ids (`type_secret`), never literal passwords on the command line.

## Choosing CLI vs MCP
- CLI: shell scripts, CI, quick checks, when MCP is not connected, saving files (`screenshot -o`).
- MCP: interactive agent work, images returned inline, batched `execute_dsl`.
Both talk to the same server and device state.

## Troubleshooting
- Exit 4 / `connection refused`: server not running or wrong `PHONEFORGE_URL` / `--server`.
- Exit 3 "device not found" or "matches N devices": `phoneforge devices` for the exact id; use the id or a
  longer name. Simulators must be booted (`phoneforge sims`, `phoneforge sim boot <udid>`).
- iOS commands fail or tree is empty: `phoneforge bridge status`, then `bridge start`; the helper must be
  installed first (`docs/ios-setup.md`).
- `501 UNSUPPORTED`: the device lacks that capability; use another approach (e.g. OCR instead of tree).
- Stale results: re-run `tree`; never reuse coordinates after navigation.
