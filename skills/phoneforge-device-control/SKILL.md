---
name: phoneforge-device-control
description: Drive a real Android/iOS phone, tablet, emulator or simulator through PhoneForge (MCP tools or the phoneforge CLI) - observe the screen, tap, swipe, type, launch apps, verify results. Use when the user says "on the phone", "on the device", "tap", "swipe", "open the app", "take a screenshot", "ui tree", "run this on my iPhone/Android", or in Turkish "telefonda", "uygulamayı aç", "ekran görüntüsü", "cihazda dene", "butona tıkla", "kaydır".
---

# PhoneForge device control

PhoneForge is a local server (`http://127.0.0.1:8700`) that exposes connected devices over REST and an
MCP endpoint (`/mcp`, server name `phoneforge`). This skill is the core observe -> act -> verify loop.
Full tool list: `references/mcp-tools.md`. Shell equivalents: see the `phoneforge-cli` skill.

## 0. Preflight
1. `list_devices` (CLI: `phoneforge devices`). If the call fails with connection refused, the server is not
   running: start it with `go run ./cmd/phoneforged -addr 127.0.0.1:8700` (or the built `phoneforged`) and retry.
2. Pick the device id. One device: use it. Several: ask the user which one, never guess. Simulators appear as
   `sim-<udid>`; shut-down simulators are only visible in `list_simulators` until `boot_simulator`.
3. `get_device` to learn platform and screen size.
4. Bridge status (`bridge_status`):
   - **iOS (real or simulator)**: `start_bridge` is required before screenshots, ui_tree or input work
     (it starts WebDriverAgent). If it says the helper is missing, tell the user to run `ios_install_helper`
     / the install step in `docs/ios-setup.md`; that is an explicit user action.
   - **Android**: works over adb already; `start_bridge` (UiAutomator2) gives a richer tree and gestures.

## 1. Observe: tree first, screenshot when visual
- Default to `ui_tree` (compact text). One line per node:
  `[7] Button "Sign in" id=login_btn @(540,1620) 880x132 clickable`
  Header says `screen WxH px` (Android pixels) or `pt` (iOS points). `@(x,y)` is the tap center in that unit.
- Always pass `filter` once you know what you look for (`filter: "Sign in"`); it matches text, label and id,
  case-insensitive, and also returns the clickable ancestor. Output is capped (default 300 nodes / 20k chars);
  a "truncated" footer means narrow the filter, do not raise limits blindly.
- Use `get_screenshot` when layout, colors, images, icons without labels, or canvas content matter, and
  as proof for the user. Screenshots cost far more context than a filtered tree.
- No accessibility tree (games, canvas, some WebViews, Flutter without semantics): `ocr_screen`
  (text + pixel boxes) and `ocr_tap` (find text, tap its center). WebViews on Android: `web_targets`,
  `web_click`, `web_type`, `web_eval`.

## 2. Act
- **Coordinates come from the latest tree, in the unit of the header** (iOS points, Android pixels).
  Never reuse coordinates from an older screen or from a screenshot's pixel size on iOS (screenshots are
  2-3x the point size).
- `tap` x,y at the node center. `double_tap`, `long_press` (`ms`), `drag`, `pinch` (scale >1 zooms in).
- `swipe` x1,y1 -> x2,y2. To scroll content *down* (see lower items) swipe *up*: start near 75% height,
  end near 25%, same x, `durationMs` 300-500.
- `type_text` types into the focused field: tap the field first, confirm it is `focused`/`editable`, then type.
  Use `execute_dsl` with `clearFirst` to replace existing text. Secrets: never type passwords from chat;
  use DSL `type_secret` with a stored `secretId`.
- `press_key`: `home`, `back` (Android), `enter`, `delete`. Use `dismiss_keyboard` when the keyboard hides targets.
- `open_url` for deep links and http(s) URLs. `set_clipboard` / `get_clipboard` for long text.

## 3. Verify after every action
After each state-changing action, re-observe with a narrow check (`ui_tree` with a `filter` for the text you
expect next). Do not chain more than 2-3 blind actions. If nothing changed: wait 1-2 s and re-check once (loading),
then reassess instead of repeating the same tap.

Waiting for loads: poll `ui_tree filter=<expected text>` every ~1 s up to ~10 s, or use a DSL
`wait_for` step (`timeoutMs`) which polls for you.

## 4. Launching and finding apps
`list_apps` returns bundle ids (iOS) / package names (Android). Pick the exact id; if several match
a name, show them to the user. `launch_app`, `stop_app`. For a clean start: `stop_app` then `launch_app`
(DSL: `launch_app` with `"fresh": true`). `install_app` takes a path to an `.apk` on the *server* machine.
`uninstall_app` and `set_permission` change device state: only do them when asked.

## 5. Interruptions
- **Permission / system alert** (Allow, While Using the App, Don't Allow): it appears in the next tree. Tap the
  option the user asked for; if they did not say, prefer the least-privilege choice and tell them.
  For scripted runs, set `"alerts": "accept"` or `"dismiss"` at the top of the DSL script.
- **Keyboard covering a button**: `dismiss_keyboard`, then re-read the tree.
- **Wrong screen / lost**: `press_key back` (Android) or tap the nav back button (iOS); last resort
  `press_key home` then `launch_app` again.
- **App crashed or left the foreground**: `get_crash_reports` (`app` filter, `limit`), report the top frame
  to the user, then relaunch.
- **Bridge dead** (calls time out, tree empty on iOS): `stop_bridge`, `start_bridge`, retry once.

## 6. Scrolling to find things
Loop: tree with filter -> not found -> swipe one page -> tree again. Stop after ~8 swipes or when the screen
stops changing (same tree twice). In DSL this is one step: `scroll_until_visible` with a selector.

## 7. Batch with execute_dsl for speed
Once the path is known, send several steps in one `execute_dsl` call (the `script` argument is a JSON string):
```json
{"version":"0.2","steps":[
 {"action":"launch_app","bundleId":"com.example.app"},
 {"action":"wait_for","selector":{"textContains":"Sign in"},"timeoutMs":8000},
 {"action":"tap","selector":{"text":"Sign in"}},
 {"action":"assert_exists","selector":{"textContains":"Welcome"},"timeoutMs":5000}]}
```
The result lists per-step status, error code (`NO_MATCH`, `TIMEOUT`, `ASSERTION_FAILED`...) and a screenshot of the
failing step. Selector taps resolve coordinates for you, so no tree read is needed. Full syntax:
the `phoneforge-test-authoring` skill. Keep exploring unknown screens step by step instead.

## 8. CLI alternative
Same loop from a shell (set `PHONEFORGE_DEVICE` once, or pass `-d <id>`; `-d` matches id/name exactly, by prefix or by substring and exits 3 when ambiguous, e.g. `-d iphone` with a simulator booted: use `-d "iphone 15"` or the id):
`phoneforge devices`, `phoneforge tree --filter "Sign in"`, `phoneforge tap --text "Sign in"`,
`phoneforge tap 540 1620`, `phoneforge wait --text "Home"`, `phoneforge swipe up` (finger direction), `phoneforge type "hello"`, `phoneforge key back`,
`phoneforge launch <id>`, `phoneforge screenshot -o shot.png`. See `phoneforge-cli`.

## Rules of thumb
- Read before acting, verify after acting; narrow trees, few screenshots.
- Ask before destructive actions: uninstall, deleting data, purchases, sending messages, changing settings.
- Report what you saw (screen text, ids) rather than assuming; say plainly when a step failed.
