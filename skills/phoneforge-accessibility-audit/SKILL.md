---
name: phoneforge-accessibility-audit
description: Audit a mobile app's accessibility on a real device or simulator with PhoneForge by inspecting the UI tree on every key screen - missing labels, unlabeled buttons, small tap targets (under 44pt iOS / 48dp Android), unnamed inputs - and report findings with severity. Use for "accessibility audit", "a11y check", "VoiceOver/TalkBack review", "tap target size", or in Turkish "erişilebilirlik denetimi", "erişilebilirlik kontrolü", "etiketi eksik butonlar", "dokunma alanı küçük mü".
---

# PhoneForge accessibility audit

PhoneForge exposes the platform accessibility tree, so most structural issues can be found without a screen reader.
It does not measure color contrast or simulate VoiceOver/TalkBack focus order; say so in the report and
recommend a manual pass for those. Observation tools: see `phoneforge-device-control`.

## Get the units right
`ui_tree` prints `screen WxH px` (Android) or `pt` (iOS) and each node as `@(cx,cy) WxH`.
- iOS sizes are in **points**: minimum tap target **44 x 44 pt** (Apple HIG).
- Android sizes are in **pixels**: convert with density. `dp = px / (densityDpi / 160)`; read density from
  `get_device` if present, else ask the user, else assume 2.75-3.0 (state the assumption). Minimum **48 x 48 dp**.
Compute the threshold in the tree's own unit before comparing.

## Procedure
1. Preflight and bridge as in device-control (iOS needs `start_bridge`). `launch_app` the target app.
2. Build a screen list: start screen plus every main tab/destination reachable in a few taps. For each screen:
   scroll once or twice to cover below-the-fold content (swipe, re-read the tree) until the tree stops changing.
3. On each screen call `ui_tree` **without** `filter` (use `max_nodes: 500`; if truncated, also read with
   `include_offscreen: true` only when needed). Save a `get_screenshot` for the report.
4. Evaluate every node line against the rules below. Record: screen, element line (role, text, id, size),
   rule, severity, suggested fix.
5. For a flagged element, double check with `ui_tree filter=<id or text>` before reporting, so the report
   quotes the exact node.
6. Write `a11y-report.md` (or HTML) and summarize counts by severity.

## Rules and severity
| Rule | How to detect in the compact tree | Severity |
|---|---|---|
| Interactive element has no accessible name | `clickable` or `editable` node with no quoted name and no `desc=`; Image/Icon/Button with only `id=` | High |
| Icon-only button named by a generic id (`btn1`, `icon`, `ImageButton`) | name empty or equals role | High |
| Text field without a label/hint | `editable` node with empty name | High |
| Tap target too small | clickable node W or H below minimum (44pt / 48dp) | Medium (High if primary action or W,H < 24) |
| Targets overlapping or touching | two clickable nodes whose rectangles overlap or gap < 8 | Medium |
| Duplicate names for different actions | several clickables with the same name on one screen ("Edit", "Edit") | Medium |
| Disabled control with no visible reason | `disabled` node, check screenshot | Low |
| Name is a raw id / developer string | name looks like `ic_close_24`, `view_3`, snake_case | Medium |
| Very long accessible name (> 100 chars, clipped in tree) | name ends with `...` | Low |
| Image that carries text but has no name | screenshot shows text, tree node unnamed (try `ocr_screen`) | Medium |
Mark each finding `confirmed` (read from the tree) or `needs manual check` (inferred from a screenshot).
Only flag what you can show; do not invent contrast numbers.

## Report format
```markdown
# Accessibility audit: <App> (<platform>, <device>) - <date>
Scope: <n> screens. Not covered: color contrast, dynamic type, screen-reader focus order, motion.
Summary: High <a> | Medium <b> | Low <c>

## Screen: Login (screenshot: a11y/01-login.png)
| Severity | Rule | Element | Fix |
|---|---|---|---|
| High | No accessible name | Button id=btn_eye @(330,412) 40x40 | Set contentDescription/accessibilityLabel "Show password" |
| Medium | Tap target 40x40pt < 44pt | same | Enlarge hit area to 44x44pt |
```
Order screens as visited; end with a prioritized fix list (High first, grouped by pattern, since one shared
component often causes many findings). Reference WCAG 2.5.5/2.5.8 (target size) and 4.1.2 (name, role, value)
by number only when relevant.

## Notes
- Android: UiAutomator2 bridge (`start_bridge`) yields a richer tree with more attributes; mention if used.
- Unnamed decorative images are fine; only flag images that convey meaning or are interactive.
- Keep the audit read-only: do not change settings, fill real forms or submit anything. Ask before logging in.
