# pocketlab CLI

`pocketlab` is a single-binary command-line client for a running Pocketlab server
(`pocketlabd`, default `http://127.0.0.1:8700`). It uses only the REST API under `/api/v1`.

## Build and install

```sh
go build -o bin/pocketlab ./cmd/pocketlab   # build only
scripts/install-cli.sh                        # build + symlink into ~/.local/bin or /usr/local/bin
scripts/install-cli.sh --copy                 # copy instead of symlink
```

The script picks the first of `~/.local/bin`, `/usr/local/bin` that is on `PATH` and writable, and prints
what it did.

### Windows

`pocketlab.exe` and `pocketlabd.exe` ship as a `.zip` (amd64, arm64). Android works with `adb.exe` in `PATH`. iOS real devices need
Apple Devices or iTunes installed (USB driver) and WDA installed once from a Mac. Simulators, the lldb debugger and native
capture are macOS-only and report a clear error elsewhere.

## Global flags and environment

| Flag | Env | Meaning |
|---|---|---|
| `--server URL` | `POCKETLAB_URL` | server base URL (default `http://127.0.0.1:8700`) |
| `-d`, `--device ID` | `POCKETLAB_DEVICE` | target device |
| `--json` | | machine-readable output (raw server JSON where there is one) |
| `--timeout DUR` | | operation timeout, e.g. `30s` (each command has a sensible default) |
| | `POCKETLAB_TOKEN` | API token, sent as `X-API-Key` when the server requires one |

Flags may appear before or after the command; use `--` before positional text that starts with `-`.

### Device resolution

If `--device` is omitted and exactly one device is connected, it is used. Otherwise the value is matched
case-insensitively against id, serial, name and model: an exact match wins, then a unique prefix, then a
unique substring. So `-d sm-g` selects `SM-G780G`. `-d iphone` is ambiguous when an iPhone and the iPhone
simulator are both connected; the error lists the candidates, and you use a longer name or the id.

### Coordinates

Tap, long-press and swipe coordinates are in the units of the server's tap endpoint: **iOS points,
Android pixels**. `pocketlab tree` prints tap centers as `@(x,y)` in the same units.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | ok |
| 1 | error (command failed, wait timed out, DSL/test did not pass, ...) |
| 2 | usage error |
| 3 | device not found or ambiguous |
| 4 | server unreachable (start `pocketlabd`) |

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
bridge start|stop|status      sims      sim boot|shutdown|unslim|mem <udid|name>
sim slim <udid|name> [--profile P] [--groups a,b] [--dry-run]
agent "prompt" [--model haiku] [--agent NAME] [--session ID] [--max-steps N]
server status|history|start|stop   version   help [command]
```

`pocketlab help <command>` (or `<command> --help`) prints concise per-command help.

### Simulator slimming (macOS)

A stock iOS 27 simulator uses about 10 GB of memory (measured as footprint, see below); most of it is background
daemons an automation run never touches. `sim slim` turns them off inside a **booted** iOS simulator so several
fit in RAM:

```
pocketlab sim slim "iPhone 17"                       # minimal-safe, about half the memory
pocketlab sim slim "iPhone 17" --profile aggressive  # about a quarter to a third
pocketlab sim slim "iPhone 17" --groups siri,photos  # custom (implies --profile custom)
pocketlab sim slim "iPhone 17" --dry-run             # list the labels, change nothing
pocketlab sim mem  "iPhone 17"                       # footprint, RSS, process count
pocketlab sim unslim "iPhone 17"                     # re-enable and reboot
```

| Profile | Disables | Measured on iOS 27.2 (stock 10.0 GB) |
|---|---|---|
| `minimal-safe` (default) | siri, intelligence, spotlight, photos, widgets, healthkit, gamecenter, news-weather, companion, screentime, analytics, mail | 52% live, 52% after reboot |
| `aggressive` | all of the above plus icloud, location, pim, comms, homekit, lockscreen | 30% live, 26% after reboot |
| `custom` | the `--groups` you list (also `icloud`, `location`, `pim`, `comms`, `homekit`, `lockscreen`) | |

How it works: for each label in the profile for the simulator's iOS major (nearest lower major if the exact one is
missing), Pocketlab runs `simctl spawn <udid> launchctl disable user/<uid>/<label>` and `bootout`. launchd keeps the
disabled set across reboot and `simctl erase` (not across `simctl clone`). A hard keep list (SpringBoard,
backboardd, testmanagerd/XCTest/WDA support, runningboardd, networking, text input, accessibility, pasteboard, lsd,
installd, containermanagerd, cfprefsd, notifyd, apsd/push, locationd, ...) is enforced in code and can never be
disabled. Labels that do not exist on a runtime are skipped and reported: the result lists `applied`, `missing` and
`failed`, so an Apple rename means less slimming, never a broken simulator. State is recorded in
`~/.pocketlab/iossim-slim.json`; `unslim` re-enables the recorded labels and reboots.

WDA is stopped before slimming; if it was running, the supervisor restarts it. Settings, Safari, keyboard typing,
the UI tree, `simctl push` and `simctl location` keep working (see `docs/plans/simulator-slimming-spike.md`).
Known side effects: home-screen widgets render blank; with `aggressive` the lock-screen wallpaper cannot be changed;
apps that use HealthKit, CloudKit, HomeKit, EventKit or telephony APIs may stall under the groups that disable those
services.

`--json` output is the REST body (`POST /api/v1/simulators/{udid}/slim`, `GET .../slim`, `POST .../unslim`,
`GET .../memory`). The same operations are MCP tools `slim_simulator`, `unslim_simulator`, `simulator_memory`, and
`boot_simulator` takes an optional `slim` flag (default off). On Windows and Linux slimming returns "requires macOS".

Memory is the sum of `footprint(1)` over the simulator's process tree (no sudo). RSS is shown too but over-counts
shared pages (a stock simulator sums to more than 20 GB of RSS), so use footprint. `sim slim <udid> --discover`
is a hidden diagnostic that lists every launchd label with its memory, profile group and keep flag.

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

- **server** shows and controls the server and, above all, why it stopped. `status` reads
  `GET /server/status` (pid, version, uptime, how it was started); `history [--limit N]` prints the start/stop log
  oldest first with the reason for each stop (shutdown API with its origin `web-ui`/`desktop`/`cli`, signal name,
  panic message, port in use) and marks unexpected exits (kill -9, crash) with `!`. When the server is down both
  fall back to the local log `~/.pocketlab/server-events.jsonl` (`$POCKETLAB_HOME`) and `status` exits 4.
  `start` launches `pocketlabd` detached (next to the CLI binary, else in PATH, or `--bin`), logs to
  `~/.pocketlab/pocketlabd.log` and waits until it is healthy; `stop` sends `POST /server/shutdown` with
  origin `cli`.

## Examples

```sh
pocketlab devices
pocketlab -d iphone15 tree --filter "Giriş"
pocketlab -d sm-g tap --text "Ayarlar" --ocr
pocketlab -d android-RF8RB1B8NNY swipe up && pocketlab -d android-RF8RB1B8NNY screenshot -o home.png
pocketlab -d ios-... wait --text "Welcome" --timeout 20s && echo ready
echo '{"actions":[{"action":"tap","text":"OK"}]}' | pocketlab -d sm-g dsl -
pocketlab -d sm-g agent "open Settings and turn on dark mode" --model haiku
```
