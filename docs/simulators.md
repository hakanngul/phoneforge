# Simulators: lifecycle, app-aware slimming, farms and CI

macOS only. Slimming itself is described in [cli.md](cli.md) and measured in
[plans/simulator-slimming-spike.md](plans/simulator-slimming-spike.md).

## Lifecycle

| Action | CLI | REST | MCP |
|---|---|---|---|
| Create | `sim create --type iPhone-17 [--runtime "iOS 27.2"] [--name N] [--slim] [--boot]` | `POST /simulators` `{name,deviceType,runtime,slim,profile,app,install,boot}` | `create_simulator` |
| Types and runtimes | | `GET /simulators/catalog` | |
| Clone | `sim clone X [--name N] [--count N] [--slim] [--shutdown]` | `POST /simulators/{udid}/clone` `{name,count,slim,shutdown,boot}` | `clone_simulator` |
| Erase | `sim erase X --force [--keep-slim]` | `POST /simulators/{udid}/erase` `{keepSlim,shutdown}` | `erase_simulator` |
| Delete | `sim delete X --force` | `DELETE /simulators/{udid}?shutdown=1` | `delete_simulator` |

- Clone, erase and delete need the simulator shut down. The REST/MCP default is a `SIMULATOR_BOOTED` (409) error;
  `shutdown` (CLI: `--shutdown` for clone, `--force` for erase/delete) shuts it down first and stops its WDA.
- `simctl clone` does not carry launchd's disabled set, so a clone of a slimmed simulator is slimmed again with the
  source's profile (inheriting its stock baseline for the "was N GB" tooltip). `--slim`, `--profile`, `--app` override;
  `"slim": false` turns it off. A clone that was only booted to be slimmed is shut down again unless `--boot`.
- Erase keeps launchd's disabled set. Without `--keep-slim` the slim record is reverted first (the simulator is booted
  briefly to re-enable its labels); with it the record stays and is re-applied after the next boot.
- Delete also forgets the slim record, the WDA desired state and the leftover launchd state directory.
- Erase and delete are irreversible and have no undo; the CLI requires `--force`, the UI asks for confirmation.

## App-aware slimming

`pocketlab sim slim X --app path/to/My.app [--install] [--boot]` (REST `app`, `install`, `boot`; also on create,
clone and farm). Pocketlab reads the bundle (Info.plist `UIBackgroundModes` and `NS*UsageDescription`, entitlements from
the code signature blob / `__TEXT,__entitlements` / `codesign -d --entitlements :-`, linked frameworks via Mach-O
`LC_LOAD_DYLIB`, also in `PlugIns/*.appex` and `Frameworks/`), maps the signals through `slim/appmap.json` to profile groups
(HealthKit, GameKit, CloudKit/iCloud, Photos, WidgetKit, CoreLocation, Intents, CoreSpotlight, Contacts/EventKit, ...) and slims
to **aggressive minus the groups the app needs**. The recorded profile is named `app`; the result lists a
`keptBecause` line per group and `informational` signals that need no group (push: `apsd` is always kept).
A bare app gets the full aggressive profile. An unreadable bundle is a 400 `INVALID_REQUEST`.

## Farms

```
pocketlab sim farm --from "Base" --count 4 --slim [--profile aggressive] [--app My.app] [--shutdown] [--strict] [--json]
pocketlab sim farm --cleanup
```

Clones the (shut down) base N times, boots each clone, slims it, optionally installs the app and prints the UDIDs.
Exit code 0 = all ready, 2 = partial, 1 = nothing is ready. `--strict` turns a partial result into exit 1 and deletes
the run's clones. Clones are named `pl-farm-<base>-<n>` and recorded in `~/.pocketlab/iossim-farm.json` before they are
booted; `--cleanup` deletes only simulators in that record. `--json` prints the REST body of `POST /simulators/farm`.

## CI example (GitHub Actions, macOS runner)

```yaml
jobs:
  ui-tests:
    runs-on: macos-15
    steps:
      - uses: actions/checkout@v4
      - name: Build the app for the simulator
        run: xcodebuild -scheme MyApp -sdk iphonesimulator -derivedDataPath build build
      - name: Start Pocketlab
        run: |
          pocketlabd & sleep 3
          pocketlab ios build-wda --sim
      - name: Farm of slim simulators
        id: farm
        run: |
          pocketlab sim create --type iPhone-17 --name base
          pocketlab sim farm --from base --count 2 --slim --strict \
            --app build/Build/Products/Debug-iphonesimulator/MyApp.app --install --json > farm.json
          echo "udids=$(jq -r '[.sims[].udid] | join(" ")' farm.json)" >> "$GITHUB_OUTPUT"
      - name: Run tests against ${{ steps.farm.outputs.udids }}
        run: ./run-ui-tests.sh ${{ steps.farm.outputs.udids }}
      - name: Cleanup
        if: always()
        run: pocketlab sim farm --cleanup
```

Hosted macOS runners have limited RAM; two slim simulators fit where one stock simulator (about 10 GB) barely does.
