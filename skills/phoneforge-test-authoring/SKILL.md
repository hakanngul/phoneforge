---
name: phoneforge-test-authoring
description: Write, validate and run PhoneForge UI tests - `.mob` line scripts and JSON DSL scripts (version 0.2) - for Android and iOS apps, including selectors, waits, assertions, params, secrets, loops, and test projects. Use for "write a mob test", "automate this flow", "DSL script", "execute_dsl", "repeatable UI test", "regression test on device", or in Turkish "test senaryosu yaz", ".mob testi", "otomasyon scripti", "UI testi yaz".
---

# PhoneForge test authoring

Two equivalent script formats run on the same engine (`internal/dsl`):
- **`.mob`**: one action per line, compact, good for humans and for stored tests. Compiled to the JSON DSL.
- **JSON DSL** (`"version": "0.2"`): what `execute_dsl` takes; use it for steps `.mob` cannot express.

Syntax details and every action: `references/mob-syntax.md` and `references/dsl-actions.md`. Read them before
writing anything beyond the examples here.

## Workflow
1. **Explore the real screens first** (`phoneforge-device-control`): read `ui_tree` for each screen to get actual
   texts and ids. Never write selectors from imagination.
2. Draft the script. Prefer stable selectors in this order: `#accessibilityId` / `resource_id` > exact
   `"text"` > `~"contains"` > regex > `index`/coordinates (last resort, breaks across screen sizes).
3. Add a `wait_for` before every interaction that follows navigation or loading, and an `assert_exists` after
   every meaningful step. Do not use fixed `delay` except for animations you cannot observe.
4. **Validate before running**: `POST /api/v1/tests/validate?kind=mob` (body = script text; add
   `&projectId=<id>` when it uses `run`). It returns line-numbered errors. The CLI has no validate command:
   `phoneforge run file.mob` saves the script into the server test project `cli` and runs it.
5. Run on a device: `execute_dsl` (JSON), `phoneforge run flow.mob` (saved to server project `cli`, or `--project NAME`) / `phoneforge dsl flow.json`, or store it in a
   test project and use `test_run`.
6. Read the result per step (`status`, `code`, `error`, failure screenshot) and fix the *selector or wait*, not by
   adding sleeps. Re-run until it passes twice in a row.

## `.mob` example
```
# Param: email = qa@example.com
# Alerts: accept

app "com.example.app" fresh
wait_for ~"Sign in" timeout:10000
type ~"Email" → "${email}" clear
# type_secret takes a secret id from the credential store, never the password itself
type_secret ~"Password" → "login-password"
tap "Sign in" type:button
assert_exists ~"Welcome" timeout:5000
tap #profile_tab
scroll down to "Log out"
tap "Log out"
assert_not_exists ~"Welcome" timeout:3000
```
`→` may be written `->`. A comment must be on its own line; a trailing `# ...` after a statement is a syntax error. Inside a line `#name` is an accessibility id.
Header comments `# Param:`, `# Alerts: accept|dismiss`, and platform blocks `# ios` ... `# end` are meaningful.

## Same flow as JSON
```json
{"version":"0.2","alerts":"accept","params":{"email":"qa@example.com"},"steps":[
 {"action":"launch_app","bundleId":"com.example.app","fresh":true},
 {"action":"wait_for","selector":{"textContains":"Sign in"},"timeoutMs":10000},
 {"action":"type","selector":{"textContains":"Email"},"text":"${email}","clearFirst":true},
 {"action":"type_secret","selector":{"textContains":"Password"},"secretId":"login-password"},
 {"action":"tap","selector":{"text":"Sign in","type":"button"}},
 {"action":"assert_exists","selector":{"textContains":"Welcome"},"timeoutMs":5000}]}
```
Keys may be written snake_case or camelCase; **unknown keys and unknown actions are rejected**, so a typo fails
validation instead of being ignored.

## Failure handling and robustness
- `onFail` per step or script-wide: `abort` (default), `skip`, `retry` (+ `retries`, `retryDelayMs`),
  `replan` (stop with `needs_replan` so an agent can re-plan), `require_user`.
- `alerts: accept|dismiss` auto-handles system permission dialogs; `settle: true` waits for moving targets.
- `platform: "ios"|"android"` on a step (or `# ios` ... `# end` in `.mob`) restricts it to one platform;
  keep one script for both OSes and branch only where the UI differs.
- Control flow: `if_exists { } else { }`, `repeat N times { }`, `repeat while|until <selector> { }`.
- Data: `extract` into a variable, `${var}` anywhere in later steps; `params` supply defaults, override at run time.
- Modes: default `explore` returns debug info on failures and only the last `observe` runs; `deterministic`
  is for CI runs where you want strict behavior.
- In `type_secret` never put the real secret in the script; it references a stored secret id.

## Test projects
Stored under `~/.phoneforge/tests/<project>/` as `.mob` or `.json` files, managed through
`/api/v1/tests/projects` (REST) and MCP `test_list_projects`, `test_list_scripts`, `test_run`, `test_list_runs`.
Share steps with `run "./login.mob" email="a@b.c"` (same project only, max depth 4, no recursion);
parameters flow into the included script's `${...}`. History: `GET /api/v1/tests/runs`.

## Review checklist
- Every selector verified against a real tree dump; texts are exact (case, punctuation, localization).
- Wait before act; assert after act; no bare coordinates unless there is no tree.
- Secrets and credentials are ids, not literals; test data is a param.
- Script passes on a clean start (`fresh`) and does not depend on state left by a previous run.
- Failure of step N leaves a screenshot and a clear error code; destructive actions (uninstall, delete) are
  explicit and rare.
