# Flows in CI

A flow (`.mobflow`) is recorded once by an agent. After that it replays from the cached DSL with no agent and no tokens, which is what CI should do.

## Run the server headless

```sh
pocketlabd &                     # listens on 127.0.0.1:8700 by default
pocketlab devices                # the runner needs a connected device or booted simulator
```

Flows live under `~/.pocketlab/tests/<project>/` (`<name>.mobflow` plus `<name>.mobflow.cache.json`). Commit both files to your repo and copy them there before the run, or record on a developer machine and ship the cache.

## Replay

```sh
pocketlab flow replay shop/checkout -d "$DEVICE" --param email=ci@example.com --junit flow-results.xml
```

- `--junit FILE` writes one testsuite per flow and one testcase per step. The screenshot of a failed step is saved in `flow-results.screenshots/` next to the report.
- `--json` prints the final run as JSON instead of the step lines.
- `--timeout 10m` stops the run on the server when it takes too long (default 30m).
- Secrets referenced by the case are read from the Pocketlab secret store on the runner; params come from the case defaults or `--param`.

Replay without `--heal` never contacts an agent: if a segment is missing or stale, or a step fails, the step is reported failed, the rest are skipped and the run fails. Stale or missing segments fail immediately, before anything touches the device, with a message such as `stale segments: s2; record or heal needed`. Re-record on a developer machine (`pocketlab flow record`) and commit the new cache.

`--heal` lets an agent repair a failing segment and update the cache. It needs a configured workflow agent, spends tokens and changes the cache file, so keep it for a scheduled job that opens a pull request, not for gating builds.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | run passed |
| 1 | run failed or was cancelled (also: `flow validate` found errors) |
| 2 | error: bad arguments, unknown case or device, server refused the run (for example device busy) |
| 4 | server not reachable |

## GitHub Actions

```yaml
jobs:
  flows:
    runs-on: macos-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install Pocketlab
        run: ./scripts/install-pocketlab.sh      # your way of getting pocketlab and pocketlabd on PATH
      - name: Start the server and a simulator
        run: |
          pocketlabd > pocketlabd.log 2>&1 &
          sleep 3
          pocketlab sim boot "iPhone 17"
      - name: Replay checkout flow
        run: pocketlab flow replay shop/checkout -d "iPhone 17" --junit flow-results.xml
      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: flow-results
          path: |
            flow-results.xml
            flow-results.screenshots/
            pocketlabd.log
      - uses: mikepenz/action-junit-report@v4
        if: always()
        with:
          report_paths: flow-results.xml
```

Check `pocketlab help flow` for the full option list, `pocketlab flow cache shop/checkout` to see which segments are fresh, and `pocketlab flow validate shop/checkout` for lint warnings (taps on raw coordinates, fixed sleeps over 2 s).

`pocketlab flow lint shop/checkout [--strict]` prints only the lint warnings (exit 0; with `--strict` exit 1 when there are any). `GET /api/v1/flows/cases/shop/checkout/stats` returns pass rate, heal count and per-step failure counts over the last 20 runs (`?limit=N`).

Every finished flow run also posts an event on the server event bus, so a webhook with the filter `Flow run` (or level `error` for failures only) is notified. The message ends with `[run=<id> status=<s> failedStep=<step> healed=<ids>]`.
