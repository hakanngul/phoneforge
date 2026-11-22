# Plan: iOS simulator slimming

Status: proposed (2026-11-22). Clean-room: only public `simctl`/launchd behaviour; daemon labels come from our own measurements, never from a competitor's artifacts.

## Goal
`slim` / `unslim` for iOS simulators so parallel runs fit in RAM, plus create/clone/erase/delete and a "farm" helper for CI.
Targets (settled idle footprint vs stock): `minimal-safe` ≤ 55%, `aggressive` ≤ 40% (stretch ~25%). Settings, Safari, WDA, OCR, typing, push and location must keep working. The competitor's "~4x" claim is treated as unverified until our measurements exist.

## Mechanism
1. **Primary:** on a booted sim, `xcrun simctl spawn <udid> launchctl disable system/<label>` then `bootout`; same for `user/<uid>` (uid read inside the sim).
2. **Fallback (flag):** edit the sim's launchd disabled/overrides plist under `~/Library/Developer/CoreSimulator/Devices/<udid>/data/var/db/com.apple.xpc.launchd/` while shut down.
3. Rejected: editing the runtime's LaunchDaemons (shared, needs root, reverted by Xcode updates).
- Sidecar record `~/.pocketlab/iossim-slim.json`: `{udid, profile, profileVersion, runtimeId, disabled[], appliedAt, baselineRSS, slimRSS}` (separate from `iossim-state.json`).
- `unslim`: `launchctl enable` each recorded label, then reboot the sim.
- Spike must answer: does `disable` persist across reboot? across `erase` (probably not → re-apply from record)? across `clone`? does `bootout` cause KeepAlive respawn loops?

## Profiles (`internal/iossim/slim`, embedded JSON, versioned)
- Groups: siri, spotlight, photos (analysis), icloud, widgets, gamecenter, healthkit, location-heavy, analytics, news/stocks/weather.
- Presets: `minimal-safe` (default), `aggressive`, `custom` (explicit groups).
- Hard keep list (code-enforced): SpringBoard, backboardd, boot-critical launchd_sim children, testmanagerd/XCTest/WDA support, runningboardd/assertiond, networking (configd, mDNSResponder, nsurlsessiond), keyboard/text input, accessibility (UI tree), pasteboardd, lsd, installd, containermanagerd, cfprefsd, notifyd, apsd (push), locationd.
- Profiles keyed by runtime major; unknown labels are skipped and reported (`applied/missing/failed`), so Apple renames degrade to "less slimming", never "broken sim".
- Discovery: hidden `pocketlab sim slim <udid> --discover` lists labels joined with per-process RSS; a drift check dumps labels for installed runtimes.

## App-aware mode (`--app <path.app>`)
Read `Info.plist` (UIBackgroundModes, usage descriptions), embedded entitlements and linked frameworks (`debug/macho`, existing plist dependency), including `.appex` and `Frameworks/`. A map (e.g. HealthKit → keep healthkit, GameKit → gamecenter, CloudKit → icloud, WidgetKit → widgets, CoreLocation → location, Intents → siri, CoreSpotlight → spotlight, aps-environment → push) turns `aggressive` minus needed groups into the app profile, with a "kept because" reason per group. Optionally installs the app and boots.

## Memory measurement (`internal/iossim/memory.go`)
Sum RSS (and `footprint` when available) of the process tree under the device's `launchd_sim` on the host; exclude shared CoreSimulator services. Settle ~30 s after boot, take the median of 3 samples.

## Interaction with Pocketlab
WDA is stopped before slimming and restarted by the supervisor (marked "restarting", not a crash). OCR/screenshots unaffected. Keyboard, push (`simctl push` via apsd) and `simctl location` stay working via the keep list.

## Surface
- Control: `Slim, Unslim, SlimStatus, Create, Clone, Erase, Delete, Memory` (clone/erase/delete require shutdown; erase/delete clear slim + WDA state).
- REST: `POST /simulators`, `POST|GET /simulators/{udid}/slim`, `POST .../unslim`, `GET .../memory`, `POST .../clone {name,count,slim}`, `POST .../erase`, `DELETE /simulators/{udid}`; list gains `slim` and `memoryMB`.
- CLI: `pocketlab sim slim|unslim|clone|erase|delete|create|mem`, and `pocketlab sim farm --from <base> --count N --slim [--app] [--json] [--cleanup]` (exit 0 ok, 2 partial, 1 failure; `--strict`).
- MCP: `slim_simulator, unslim_simulator, create_simulator, clone_simulator, erase_simulator, delete_simulator, simulator_memory`; `boot_simulator` gains `slim` and `app` (slimming off by default).
- UI (`frontend/simulators.js`): Slim toggle + profile dropdown, memory badge ("1.2 GB, was 3.9 GB"), Clone/Erase/Delete with `PF.confirm`.

## Slices
| # | Slice | Effort | Acceptance |
|---|---|---|---|
| 1 | Spike + measurement (`memory.go`, `--discover`) | 1–2 d | Persistence behaviour documented; baselines for 2 runtimes |
| 2 | Slim engine + profiles + sidecar | 3 d | `minimal-safe` ≤ 55% of stock; `unslim` restores the original service set |
| 3 | CLI + REST + MCP for slim/unslim/mem | 1–2 d | Same JSON on all surfaces; existing boot/shutdown tests pass |
| 4 | UI toggle + memory badge | 1 d | |
| 5 | Create/clone/erase/delete everywhere | 2 d | |
| 6 | App-aware slimming | 2–3 d | HealthKit sample keeps healthkit; bare app gets `aggressive`; reasons printed |
| 7 | Farm + CI mode + docs | 1–2 d | |

Total: ~2–3 weeks, dominated by label research and validation.

## Validation gate (per runtime, before release)
Stock vs `minimal-safe` vs `aggressive` footprint; smoke on the slim sim: Settings, Safari page load, Start WDA + UI tree, typing in Safari URL bar, `simctl push`, `simctl location`, OCR, install + launch a sample app; reboot/erase/clone tests; 4 parallel slim sims.

## Risks / open questions
Apple renaming daemons per iOS release; apps implicitly depending on a slimmed service (share sheet, intents, widgets); `disable` not persisting or not stopping KeepAlive services; `bootout` hanging `bootstatus` (per-label timeout, leaf-first order); noisy RSS. Open: slim by default in CI? per-label overrides (v1: groups only)? should `erase` re-apply slim (`--keep-slim`)?

Key files: `internal/iossim/{control,routes,simctl}.go`, `internal/cli/cmds_misc.go`, `internal/mcpserver/mcpserver.go`, `frontend/simulators.js`.
