# Plan: AI-recorded workflow cases with deterministic replay ("Flows", `.mobflow`)

Status: proposed (2026-11-22). Clean-room: parity with the competitor's public MCP tool surface only (`workflow_step_start`, `workflow_substep`, `workflow_assert`, `workflow_step_done`, `workflow_cache_script`).

## Goal
A human writes a natural-language test case. The first run is executed by an AI agent over MCP, reporting each step and finally submitting deterministic DSL. Later runs replay that DSL with no agent and no tokens. A failing replayed segment goes back to the agent alone (self-heal) and the cache is updated. CI runs replay-only.

## Concepts
- **Case** (`.mobflow`): human-authored, never contains generated DSL.
- **Cache** (`<name>.mobflow.cache.json`): agent-recorded segments keyed to a hash of each step.
- **Segment**: `{id, type, title, steps:[dsl], stepHash}` bound to a case step.
- **Run**: `mode = record | replay | heal`; status `running|passed|failed|cancelled`; per-step state `pending → running → passed|failed|skipped` (+ `healing → healed` in replay).
- **Artifacts**: per-step screenshots (files), agent transcript, DSL results, timings.

## Format
```yaml
version: 1
name: Checkout happy path
app: {bundleId: com.shop.app, platform: any}
device: {platform: any, minOS: "14"}
params:
  email: {default: "qa@example.com"}
  item:  {default: "Blue mug"}
secrets: [shop_password]            # ids only, resolved by the existing secret store
steps:
  - {id: s1, type: login,  title: Sign in,          do: "Sign in with ${email} and secret shop_password"}
  - {id: s2, type: act,    title: Add item to cart, do: "Search for ${item}, open it, tap Add to cart", expect: "Cart badge shows 1"}
  - {id: s3, type: assert, title: Cart total,       do: "Open the cart", expect: "A total price is displayed"}
```
`stepHash = sha256(type|do|expect|param names)` (title edits do not invalidate); `caseHash` covers app, secrets and step order.

## Storage
`~/.pocketlab/tests/<project>/<name>.mobflow` next to `.mob` tests, cache sidecar beside it, cache history `.cache.history/` (last 10), runs in `~/.pocketlab/tests/.flowruns/<runId>/` (run.json, step screenshots, agent.log), pruned like run history (500).

## Architecture
```
.mobflow ─► internal/mobflow.Store
UI / CLI / CI ─REST /api/v1/flows/*─► mobflow.Service (orchestrator, state machine, SSE)
   record/heal ─► aiconfig.Manager.StartRun(workflow prompt) ─► agent ─MCP workflow_* / execute_dsl─► /flows/runs/{id}/...
   replay      ─► Replayer: per segment dsl.ExecuteScript(deterministic, settle, params, secrets); on fail → heal(segment)
Service ─► events (activity log) + SSE /flows/runs/{id}/events (timeline, screenshots)
```

## MCP tools (`internal/mcpserver`, thin HTTP clients like the existing tools)
`workflow_step_start, workflow_substep, workflow_assert, workflow_step_done, workflow_cache_script` with the parity names/params (`case_id` = `<project>/<name>`, optional `run_id`), plus `workflow_get_case` and `workflow_list`. Unknown step ids, out-of-order and double-done calls return errors naming the valid ids; a run still open when the agent exits is closed as failed.

## Agent launch (record and heal)
- New `aiconfig.TypeWorkflow` with its own agent/model slot (settings migration).
- System prompt: get the case; per step start → act in small observed `execute_dsl` batches → substeps → assert/done; prefer predicates over coordinates, add `wait_for stable` after navigation, use `${param}` and `type_secret`, never print secrets; finish with `workflow_cache_script` (segment ids = step ids; on failure cache only passed segments).
- Heal prompt: failing segment DSL, error, failure screenshot, recent substeps; complete only that step and submit only that segment.
- `Manager` gets an `OnDone` hook so the Service can finalize runs; heal resumes the record session when possible.
- A per-device lock for the whole flow run (other runs on the device get 409).

## Cache validation and versioning
On submit: parse, `dsl.Validate` each segment as a deterministic script; segment ids ⊆ step ids; no `on_fail: require_user|replan`; assert steps must contain an assertion; reject literal secret values; `stepHash` set by the server. Invalidation per segment (stale/missing segments fall back to record for those steps only; a changed `caseHash` invalidates all). Atomic writes, previous version to history, heal bumps the version.

## Replay engine
Resolve params/secrets → run fresh segments in order → screenshot after each → synthesize substeps from step results so timelines match record runs. On failure: `--no-heal` (CI default) fails the step and skips the rest; with heal, start a heal run, continue with the healed segment, flag `healed:[ids]`, update the cache; a failed heal keeps the old cache. `needs_replan`/`needs_user` count as failures. Save-time normalisation injects `wait_for stable` after screen-changing actions; `flow lint` warns on raw-coordinate taps and sleeps over 2 s.

## REST (`/api/v1/flows`)
Cases CRUD + validate; cache get (with staleness map) / put / delete / history / restore; runs `POST {project, case, deviceId, mode: auto|record|replay, heal, params, agent}` → 202, list, get, SSE events, artifacts, cancel; agent-only step endpoints used by the MCP tools.

## UI and CLI
- Testing tab → "Flows" sub-tab: case list with cache badges (fresh / stale N / none), step editor, Run with agent / Replay / Replay + heal, live timeline (substeps, screenshots, durations, healed marker), cache viewer with diff/restore, history with screenshot compare.
- CLI: `pocketlab flow list|run|replay|record|cache|validate`, `replay --heal --param k=v --json --junit file`; exit 0 pass / 1 fail / 2 error; replay without `--heal` never contacts an agent.

## Slices
| # | Slice | Effort | Acceptance |
|---|---|---|---|
| 1 | Format + store + CRUD + `flow list/validate` | S | Round-trip; bad/duplicate ids rejected; `.mob` runs unaffected |
| 2 | Run model + MCP reporting tools + SSE | M | External agent drives a run end to end; `passed=false` fails it; per-step screenshots |
| 3 | Agent launch + recording + cache | M | Fake agent produces a cache and passing run; editing one step stales only its segment |
| 4 | Replay engine + CLI + CI doc | M | Replay passes with no agent binary present; broken segment fails with correct step + screenshot |
| 5 | Self-heal | M | Broken selector healed once; next replay passes without agent |
| 6 | UI | M/L | |
| 7 | Polish (lint, stabilisation, flakiness stats, failure webhook) | S/M | |

Total: ~3–4 engineer-weeks; slices 1–4 (CLI/CI value) ~1.5 weeks.

## Tests
Fake agent driving the MCP tools over httptest (happy path, assert failure, unknown step, early exit, bad DSL); replay with the existing fake device (fresh, stale, fail with/without heal, failed heal); hashing, cache atomicity/history, parser, state machine, device lock; CLI tests.

## Risks / open questions
Agent DSL quality (prompt + validation + lint + optional `--verify` replay); also capture every successful `execute_dsl` during a step as a cross-check; agents without headless mode need `agent: external` runs; one cache per platform (`cache.<platform>.json`) — decide before slice 3; heal must not weaken assertions (flag assert diffs for review, approval in CI); secrets in transcripts/screenshots; reuse Testing agent settings vs a new slot.

Key files: `internal/mobtest/{store,routes}.go`, `internal/aiconfig/runner.go`, `internal/mcpserver/mcpserver.go`, `internal/dsl/engine.go`.
