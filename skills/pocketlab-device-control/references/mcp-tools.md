# Pocketlab MCP tools

Server `pocketlab`, endpoint `http://127.0.0.1:8700/mcp` (streamable HTTP). Every device tool takes `id`
(device id from `list_devices`) unless noted. Tools live in `internal/mcpserver/`.

## Devices and apps
| Tool | Args | Notes |
|---|---|---|
| `list_devices` | - | connected devices |
| `get_device` | id | platform, model, screen |
| `list_apps` | id | bundle ids / package names |
| `launch_app` / `stop_app` | id, bundleId | |
| `install_app` | id, path | `.apk` path on the server machine |
| `uninstall_app` | id, bundleId | destructive |
| `set_permission` | id, bundleId, permission, grant | e.g. `android.permission.CAMERA` |
| `open_url` | id, url | http(s) or app scheme |

## Observe
| Tool | Args | Notes |
|---|---|---|
| `ui_tree` | id, format=compact\|json, filter, max_nodes, include_offscreen | compact is default; use `filter` |
| `get_screenshot` | id | PNG image |
| `ocr_screen` | id, words | text lines with pixel boxes |
| `get_crash_reports` | id, app, limit, since (RFC 3339), name, include_text | `name` reads one full report |
| `get_clipboard` | id | |

## Input
| Tool | Args |
|---|---|
| `tap` / `double_tap` | id, x, y |
| `long_press` | id, x, y, ms (default 600) |
| `swipe` | id, x1, y1, x2, y2, durationMs (300) |
| `drag` | id, x1, y1, x2, y2, durationMs (500) |
| `pinch` | id, x, y, scale, durationMs |
| `type_text` | id, text (focused field) |
| `press_key` | id, key: home, back, enter, delete, app_switch (iOS home-button devices: double Home; Face ID iPads count as home-button, limitation) |
| `dismiss_keyboard` | id |
| `set_clipboard` | id, text |
| `ocr_tap` | id, text, index, exact, regex |

## Scripting and tests
| Tool | Args |
|---|---|
| `execute_dsl` | id, script (JSON string) |
| `test_list_projects` | - |
| `test_list_scripts` | projectId |
| `test_run` | projectId, script (file name), deviceId |
| `test_list_runs` | projectId (optional), limit |

## Bridge and simulators
| Tool | Args | Notes |
|---|---|---|
| `start_bridge` / `stop_bridge` / `bridge_status` | id | Android: UiAutomator2; iOS: WebDriverAgent (required for iOS control) |
| `ios_install_helper` | id | installs locally built WDA runner; explicit user action |
| `list_simulators` | - | all simulators, state, WDA status |
| `boot_simulator` / `shutdown_simulator` | udid (boot: optional `slim`, `profile`) | booted sim shows up as `sim-<udid>` |
| `slim_simulator` / `unslim_simulator` / `simulator_memory` | udid (slim: `profile`, `groups`, `dry_run`) | turns off background daemons to cut memory (macOS) |

## WebView / Chrome (Android)
`web_targets`, `web_navigate` (url), `web_eval` (script), `web_click` (selector), `web_type` (selector, text);
web tools also take an optional page `targetId`.

## Debugging (iOS, lldb)
`debug_attach` (launch=true to start suspended), `debug_breakpoint`, `debug_step`, `debug_eval`,
`debug_state`, `debug_detach`. Needs Xcode and Developer mode; see `docs/debugging.md`. Real devices accept only
development-signed debuggable apps. Android is not supported.

Errors come back as `HTTP <code>: <body>`; `501 UNSUPPORTED` means the device lacks that capability.
