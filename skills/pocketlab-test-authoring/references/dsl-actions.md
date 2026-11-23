# JSON DSL reference (version 0.2, internal/dsl)

## Script
```json
{"version":"0.2",
 "defaults":{"settleMs":400,"retries":3,"timeoutMs":10000},
 "alerts":"accept", "settle":false, "mode":"explore",
 "onFail":"abort",
 "params":{"email":"qa@example.com","token":null},
 "steps":[{"action":"tap","selector":{"text":"OK"}}]}
```
- `version` optional; anything other than `0.2` is rejected. `mode`: `explore` (default; adds debug info to failures,
  runs only the last `observe`) or `deterministic`. `alerts`: `accept` | `dismiss`. `params`: a scalar default, or
  `null` for a required param; supply values at run time; use `${name}` in any string field.
- `defaults.settleMs` = pause after each action (default 400). `defaults.timeoutMs` = default `wait_for` wait.
- Keys can be camelCase or snake_case. Unknown keys / actions / strategies fail validation.

## Result
`{"passed":bool,"status":"passed|failed|needs_replan|needs_user","steps":[{index,id,action,status,error,code,
matched,durationMs,warnings,result,screenshot,debug}],"checkpoints":[...],"storage":{...},"totalMs":n}`.
Step `status`: `ok`, `failed`, `skipped`, `needs_replan`, `needs_user`. After a failure the remaining steps are
`skipped`. Error `code`: `NO_MATCH`, `AMBIGUOUS_MATCH`, `TIMEOUT`, `ASSERTION_FAILED`, `VALIDATION_ERROR`,
`INVALID_ACTION`, `CONTEXT_NOT_AVAILABLE`, `SAFETY_BLOCKED`, `EXECUTION_ERROR`. Failed steps carry a base64 PNG
`screenshot`.

## Selector (predicate); all given fields must match
`text`, `textContains`, `textStartsWith`, `textRegex` (Go regexp; `(?i)` for case-insensitive), `value`,
`valueContains`, `resourceId`, `accessibilityId`, `label`, `labelContains`, `role` (exact platform role),
`type` (button, input, text, image, cell, scrollview, switch, slider, link, icon, `*`), `enabled`, `visible`,
`selected`, `near` `{<predicate>, "direction":"above|below|left|right|any", "maxDistance":px}`, `boundsHint`
(top_half, bottom_half, left_half, right_half, center, top_left, top_right, bottom_left, bottom_right),
`parentOf` (selector), `index` (0-based pick among matches), `cssSelector` (web contexts; unsupported).
`step.coords` `{"x":100,"y":200}` / `{"xPercent":50,"yPercent":25}` / `[100,200]` replaces a selector.

## Actions (canonical name: common aliases)
**App**: `launch_app` (open_app, app) `bundleId`, `fresh`; `stop_app` (kill_app, close_app) `bundleId`;
`open_url` (open_link, deeplink) `url`; `install_app` `path` (server-side file); `uninstall_app` `bundleId`;
`grant_permission` / `revoke_permission` `bundleId`, `permission`; `set_permissions` `bundleId`, `permissions`
`{"camera":"allow|deny|unset"}`.
**Touch**: `tap` (click) selector|coords|x,y; `double_tap`; `long_press` + `ms`; `two_finger_tap`;
`pinch` + `scale`, `durationMs`; `swipe` `direction` + `distance` (short|medium|full), optionally from an element via
`from`/`fromCoords`, or `x1,y1,x2,y2`, `durationMs`; `scroll` `direction` (default down), `amount` (small|page|full),
`selector` (container), `toElement` `{"predicate":{...},"fullyVisible":bool}`, `maxScrolls`;
`scroll_until_visible` `selector`, `direction` (up|down), `maxSwipes` (8); `drag` `selector`|`from`|`fromCoords` to
`toElement`|`toSelector`|`toCoords`; `drag_path` `points` `[{x,y,durationMs}]`.
**Text/keys**: `type` (input_text, enter_text) `selector`?, `text`, `clearFirst`, `dismissKeyboard`;
`type_secret` `selector`?, `secretId`; `clear`; `dismiss_keyboard`; `copy_text`/`paste_text`; `set_clipboard`
`text`; `press_key` `key`: home, back, enter, delete, app_switch (alias recent_apps), ...; `navigate` `target`; `toggle`
`selector` (flips the control; explicit on/off is unsupported); `select_picker` `selector`, `value`.
**Waits**: `wait` (delay, sleep) `ms`/`durationMs`; `wait_for` `selector`, `timeoutMs`, `pollIntervalMs`,
`stable`; `wait_stable` (no selector = wait until screen stops changing); `checkpoint` `name`, `metadata`.
**Assertions**: `assert_exists` (assert, assert_visible) `selector`, `timeoutMs` (waits to appear);
`assert_not_exists` (assert_absent) waits to disappear; `assert_count` `expected`; `assert_property` `property`
(enabled, visible, focused, text, value, editable, scrollable, selected) + `expectedValue`;
`assert_screen_changed` `thresholdPercent`. `message` overrides the failure text.
**Read/observe**: `observe` `include` `["ui_tree","screenshot","installedApps","ocr"]`, `compact`, `storeAs`;
`screenshot` (inline in result); `extract` `extractKey` + `selector` (text) | `text` (literal) | `extractKind:
"screenshot"` (hash), `extractRegex` (first capture group); `ocr` `storeAs`; `ocr_tap` / `assert_text_ocr`
`text`, `index`, `exact`, `regex`, `timeoutMs`.
**Device**: `set_location` `lat`,`lon`; `reset_location`; `biometrics` `state` match|nomatch|enroll|unenroll;
`fold`; `siri` `prompt`; `eval_script`/`run_script`; `request_user_input`.
**Flow**: `if_exists` `selector`, `then`, `else`; `repeat` `times` | `while` selector | `until` selector, `body`,
`maxIterations`, optionally `condition` (JS).
**Web (Android Chrome/WebView over CDP)**: `web_navigate`, `web_click`, `web_type`, `web_eval`, `web_wait`,
`web_assert_text`; pair with MCP `web_targets`.
Not available: `record_start`/`record_stop`, `audit`, `perform_actions` (reported as unsupported).

## Per-step control
`id` (echoed in the result), `platform` (`ios`|`android`), `timeoutMs`, `settleMs`, `onFail`
(`"retry"` or `{"strategy":"retry","maxRetries":2,"retryDelayMs":1000,"fallbackStrategy":"skip"}`), `retries`,
`retryDelayMs`.

## Examples
Skip an optional dialog, scroll to a row, read a value, assert it:
```json
{"steps":[
 {"action":"if_exists","selector":{"text":"Not now"},"then":[{"action":"tap","selector":{"text":"Not now"}}]},
 {"action":"scroll_until_visible","selector":{"text":"Total"},"direction":"down","maxSwipes":6},
 {"action":"extract","extractKey":"total","selector":{"textContains":"$"},"extractRegex":"\\$([0-9.]+)"},
 {"action":"assert_property","selector":{"text":"Pay"},"property":"enabled","expectedValue":"true"},
 {"action":"screenshot","id":"checkout"}]}
```
Platform-specific back navigation, retry flaky tap:
```json
{"steps":[
 {"action":"press_key","key":"back","platform":"android"},
 {"action":"tap","selector":{"text":"Back","type":"button"},"platform":"ios"},
 {"action":"tap","selector":{"text":"Refresh"},"onFail":{"strategy":"retry","maxRetries":2}}]}
```
