---
name: phoneforge-smoke-test
description: Run a quick smoke test of a mobile app on a real device or simulator with PhoneForge - launch it, walk the main flows, screenshot every step, detect crashes, and write a pass/fail report (Markdown or HTML) in the working directory. Use for "smoke test", "sanity check", "does the app crash", "verify the app works", "test this build on the phone", or in Turkish "duman testi", "uygulama çöküyor mu", "telefonda hızlı test", "uygulamayı test et".
---

# PhoneForge smoke test

Goal: in a few minutes, prove the app launches and its critical paths work, with evidence. Not exhaustive QA.
Use the observe/act/verify loop from `phoneforge-device-control` for every interaction.

## Inputs to settle first (ask only what you cannot infer)
- Device id (`list_devices`), app id (bundle id / package; find with `list_apps`), build installed?
  (`install_app` takes an `.apk` path on the server machine.)
- The critical flows, e.g. "launch -> login -> home -> open item -> back". If the user gave none, discover
  them from the first screens and state your assumed list before running.
- Test credentials: only ones the user supplied. Never invent accounts or type real passwords from memory.

## Procedure
1. **Prepare**: iOS needs `start_bridge` first. Note the start time (ISO 8601 UTC) for the crash check.
   `stop_app` then `launch_app` for a cold start. Create `smoke-<app>-<YYYYmmdd-HHMM>/` in the working dir.
2. **Launch check**: wait up to ~10 s for the first meaningful screen (`ui_tree` with a `filter`, or DSL
   `wait_for`). Save `get_screenshot` as `01-launch.png`. FAIL if the app is not in the foreground
   (tree shows the launcher / another app) or a crash dialog appears.
3. **Walk each flow step by step**. For every step record: step name, action taken, expected result, observed
   result, PASS/FAIL, screenshot file (`NN-<slug>.png`). Verify with a tree check for specific expected text,
   not by eye alone. Handle permission prompts per the user's wishes (state what you chose).
4. **After each step, cheap crash check**: is the target app still foreground (expected text present in tree)?
   If the screen changed to the launcher or an OS "has stopped / keeps stopping" dialog, that is a crash.
5. **Crash scan at the end** (and immediately when a crash is suspected):
   `get_crash_reports` with `app=<bundle id or package>`, `since=<start time>`, `include_text: true`, `limit: 5`.
   Any report newer than the start time = FAIL for the step it coincides with; quote the exception/top frames.
6. **Stop on blockers**: if a step fails and later steps depend on it, mark them SKIPPED, do not guess.
   Retry a failed step once after re-reading the tree (transient loading), never more.
7. **Write the report** (below) and tell the user the path plus the headline result.

## Faster path when flows are known
Encode the flow as a DSL script and run it with `execute_dsl`; each failing step returns a screenshot and an
error code. Add `{"action":"screenshot"}` steps for evidence. Then still run the crash scan. Syntax:
`phoneforge-test-authoring`. CLI users: `phoneforge run flow.mob` (exit 1 if a step fails) and `phoneforge crashes --app <id>`.

## Report format
Write `smoke-report.md` (or `.html` if asked; embed images with relative `<img src>`) in the run folder:

```markdown
# Smoke test: <App name> <version/build>
- Device: <name, OS, id>   - Date: <ISO time>   - Duration: <mm:ss>
- Result: **PASS** | **FAIL** (<n> of <m> steps passed, <k> skipped)

| # | Step | Expected | Observed | Result | Evidence |
|---|------|----------|----------|--------|----------|
| 1 | Cold launch | Home/login visible | Login screen in 2.1 s | PASS | 01-launch.png |
| 2 | Log in | Welcome text | "Welcome, QA" | PASS | 02-login.png |
| 3 | Open cart | Cart title | Spinner never ends | FAIL | 03-cart.png |

## Crashes
None found since <time>.   (or: report name, process, exception, top 5 frames)

## Notes
Assumptions, permissions granted, skipped steps and why.
```
Overall PASS only if every non-skipped step passed and there are no crash reports in the window.
HTML variant: same table in a minimal self-contained page with thumbnails; no external assets.

## Pitfalls
- iOS screenshots are larger than point coordinates; do not tap from screenshot pixels.
- A "pass" needs an observed expected element, not just "no error returned" from a tap.
- Do not leave the device in a changed state without saying so (granted permissions, logged-in account,
  installed build).
- Keep screenshots to one per step; use filtered trees for everything else.
