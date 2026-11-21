# phoneforge CLI

`phoneforge` is a single-binary command-line client for a running PhoneForge server
(`phoneforged`, default `http://127.0.0.1:8700`). It uses only the REST API under `/api/v1`.

## Build and install

```sh
go build -o bin/phoneforge ./cmd/phoneforge   # build only
scripts/install-cli.sh                        # build + symlink into ~/.local/bin or /usr/local/bin
scripts/install-cli.sh --copy                 # copy instead of symlink
```

The script picks the first of `~/.local/bin`, `/usr/local/bin` that is on `PATH` and writable, and prints
what it did.

### Windows

`phoneforge.exe` and `phoneforged.exe` ship as a `.zip` (amd64, arm64). Android works with `adb.exe` in `PATH`. iOS real devices need
Apple Devices or iTunes installed (USB driver) and WDA installed once from a Mac. Simulators, the lldb debugger and native
capture are macOS-only and report a clear error elsewhere.

## Global flags and environment

| Flag | Env | Meaning |
|---|---|---|
| `--server URL` | `PHONEFORGE_URL` | server base URL (default `http://127.0.0.1:8700`) |
| `-d`, `--device ID` | `PHONEFORGE_DEVICE` | target device |
| `--json` | | machine-readable output (raw server JSON where there is one) |
| `--timeout DUR` | | operation timeout, e.g. `30s` (each command has a sensible default) |
| | `PHONEFORGE_TOKEN` | API token, sent as `X-API-Key` when the server requires one |

Flags may appear before or after the command; use `--` before positional text that starts with `-`.

### Device resolution

If `--device` is omitted and exactly one device is connected, it is used. Otherwise the value is matched
case-insensitively against id, serial, name and model: an exact match wins, then a unique prefix, then a
unique substring. So `-d sm-g` selects `SM-G780G`. `-d iphone` is ambiguous when an iPhone and the iPhone
simulator are both connected; the error lists the candidates, and you use a longer name or the id.

### Coordinates

Tap, long-press and swipe coordinates are in the units of the server's tap endpoint: **iOS points,
Android pixels**. `phoneforge tree` prints tap centers as `@(x,y)` in the same units.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | ok |
| 1 | error (command failed, wait timed out, DSL/test did not pass, ...) |
| 2 | usage error |
| 3 | device not found or ambiguous |
| 4 | server unreachable (start `phoneforged`) |

## Commands

```
devices                       list devices
device info                   details for one device
screenshot [-o file.png]      default ./screenshot-<ts>.png; "-o -" writes PNG to stdout
tree [--filter T] [--max-nodes N] [--offscreen]   compact text tree (like MCP ui_tree)
tap X Y | tap --text T [--index N] [--ocr] | tap --id RES_ID
longpress X Y [--ms N]
swipe X1 Y1 X2 Y2 [--ms N] | swipe up|down|left|right
type "text"                   key home|back|enter|delete|volume_up|...
apps [--filter T]             launch|stop|uninstall <bundle>     install <file.apk|.ipa|.app>
open <url>                    ocr [--find T] [--exact] [--regex] [--case-sensitive] [--words]
wait --text T | --id ID [--gone] [--ocr] [--timeout 10s]
dsl <file.json|->             run <file.mob|file.json> [--project NAME]
logs [-f] [--seconds N] [--grep T]       record [--seconds N] [-o file.mp4]
crashes [--app B] [--limit N]            crash <name> [-o file]
bridge start|stop|status      sims      sim boot|shutdown <udid|name>
agent "prompt" [--model haiku] [--agent NAME] [--session ID] [--max-steps N]
server status|stop           version   help [command]
```

`phoneforge help <command>` (or `<command> --help`) prints concise per-command help.

### Notes

- **tap --text / --id** reads the UI tree, ranks exact matches before substring matches (clickable first)
  and taps the element center. With `--ocr`, if the tree has no match, it falls back to
  `POST /ocr/devices/{id}/tap`.
- **swipe up|down|left|right** moves the finger in that direction across the middle of the screen
  (`swipe up` scrolls content down). The screen size comes from the UI tree root.
- **wait** polls the tree (and OCR with `--ocr`) every 250 ms by default (`--interval`) until the element appears,
  or with `--gone` disappears. Exit 1 on timeout.
- **dsl** posts the document to `POST /devices/{id}/dsl/execute` and prints one line per step.
- **run** stores the script in a server-side test project (default `cli`, created on first use), runs
  it, and prints per-step results.
- **install** uploads APK/IPA files; an `.app` directory is installed by path (server must be local).
- **bridge** is UiAutomator2 on Android and WebDriverAgent on iOS; the server picks by platform.
- **agent** starts `POST /ai/runs` (type `automation`) and streams events compactly: plain lines are
  thoughts, `▸ tool(args)` actions, `✓ done: ...` the result. Ctrl-C cancels the run on the server.
  `--model` temporarily sets the automation model in the AI settings (the run API has no model field)
  and restores it as soon as the run has started. If the selected agent is the built-in one, the CLI
  streams `POST /devices/{id}/agent/run` instead.
- **agent --session ID** resumes an earlier Claude Code session (sent as `sessionId` in the run request).
- **agent --max-steps N** caps the number of steps for the built-in agent only (`maxSteps` in the
  `agent/run` request); other agent CLIs ignore it.
- **ocr --case-sensitive** makes `--find` match case-sensitively (default is case-insensitive).

## Examples

```sh
phoneforge devices
phoneforge -d iphone15 tree --filter "Giriş"
phoneforge -d sm-g tap --text "Ayarlar" --ocr
phoneforge -d android-RF8RB1B8NNY swipe up && phoneforge -d android-RF8RB1B8NNY screenshot -o home.png
phoneforge -d ios-... wait --text "Welcome" --timeout 20s && echo ready
echo '{"actions":[{"action":"tap","text":"OK"}]}' | phoneforge -d sm-g dsl -
phoneforge -d sm-g agent "open Settings and turn on dark mode" --model haiku
```
