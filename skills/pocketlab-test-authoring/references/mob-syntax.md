# .mob syntax (as implemented in internal/mobtest/mobscript.go)

One statement per line. Blank lines ignored. A line starting with `#` is a comment, except the recognized headers
`# Param: name = default`, `# Alerts: accept|dismiss`, and `# ios` / `# android` ... `# end` platform blocks
(also `# [ios]`). Steps inside a platform block get that `platform` unless they set one.
Statements start with an action keyword (any alias the DSL accepts: `tap`/`click`, `app`/`open_app`/`launch_app`,
`type`/`input_text`, `wait`/`delay`/`sleep`, `assert`/`assert_visible`, ...). Strings are double quoted with
`\"`, `\\`, `\n`, `\t` escapes. Errors are reported with line numbers; all errors are collected in one pass.

Comments are only allowed on their own line. The trailing `# ...` annotations in the statement list below are
explanation for you; do not copy them into a script (the parser rejects them).

## Predicates (what to match)
| Form | Meaning |
|---|---|
| `"Save"` | exact text |
| `~"Sav"` | text contains |
| `/"^Sa.e$"/` , `/"^sa"/i` | regex (trailing `i` = case-insensitive) |
| `#save_btn` | accessibility id (matches resource id / label) |
| `[2]` | pick the 3rd match (0-based index) |
| `type:button` | portable type: button, input, text, image, cell, scrollview, switch, slider, link, icon |
| `role:`, `label:`, `label_contains:`, `value:`, `value_contains:`, `resource_id:`, `text_starts_with:`, `text_regex:` | field matchers |
| `enabled:true` `visible:true` `selected:false` | state filters |
| `bounds:top_half` | region hint: top_half, bottom_half, left_half, right_half, center, top_left, top_right, bottom_left, bottom_right |
| `near "Email" direction:below max_distance:200` | element near an anchor (direction above/below/left/right/any) |
| `parent_of:"Delete"` | element that contains the described child |
Predicates combine on one line (all must match): `tap "Item" type:cell [1]`.
Coordinates instead of a predicate: `tap 100,200` or percent `tap 50%,25%`.

## Settings (key:value on any line)
`timeout:5000` (ms), `duration:500`, `on_fail:skip|retry|abort|replan|require_user`, `retries:2`,
`retry_delay:1000`, `fallback:abort`, `settle_ms:0`, `poll_interval:200`, `max_scrolls:10`, `max_swipes:8`,
`distance:short|medium|full` (swipe), `amount:small|page|full` (scroll), `direction:up|down|left|right`,
`scale:`, `lat:`, `lon:`, `expected:`, `property:`, `expected_value:`, `message:` (assertion message),
`id:` (step id), `store_as:`, `platform:ios|android`, `include:ui_tree,screenshot` (observe), `threshold:`.

## Statements
```
app "com.example.app" [fresh]          # launch (alias open_app); fresh = stop first
kill_app "com.example.app"             # stop; also: uninstall_app "id"
open_link "myapp://item/42"
tap <pred|x,y>        double_tap <pred>      long_press <pred> duration:800      two_finger_tap <pred>
type <pred> → "text" [clear] [dismiss]        # into a field; `type "text"` types into the focused field
type_secret <pred> → "secret-id"              # value looked up in the credential store
clear <pred>          hide_keyboard           copy_text <pred>      paste_text
press_key home|back|enter|delete|app_switch|recent_apps
swipe up|down|left|right [distance:short]     # or: swipe from:200,700 to:200,200 (see drag)
scroll down [in <pred>]                       # scroll a container
scroll down to "Privacy" [fully_visible]      # scroll until target visible (max_scrolls:)
scroll_until_visible "Privacy" down max_swipes:8
drag "Card" to "Trash"       drag 100,500 to 100,100
toggle "Wi-Fi" [on|off]      select_picker "Month" → "June"
wait_for ~"Done" timeout:8000                 # appear; `wait_for stable` / `wait_stable timeout:3000` = screen stops changing
delay 1000                                    # fixed pause (ms); last resort
assert_exists ~"Welcome" timeout:5000         # waits up to timeout
assert_not_exists "Spinner" timeout:5000      # waits for disappearance
assert_count type:cell expected:5
assert_property "Submit" property:enabled expected_value:true
assert_screen_changed
screenshot ["name.png"]      observe [ui_tree screenshot ocr]      checkpoint "after-login"
extract total from "Total" extract_regex:"\$([0-9.]+)"        # then ${total}
extract token = "literal"    extract hash screenshot
set_location 37.77,-122.42   reset_location   set_permissions com.example camera=allow photos=deny
biometrics match|nomatch|enroll|unenroll [face|finger]     fold closed|book|open|<angle>     siri "what time is it"
```
Not every action works on every driver (e.g. recording, accessibility `audit`, `toggle on/off`, biometrics are
unsupported on some devices); the step fails with an `UNSUPPORTED`/`EXECUTION_ERROR` message rather than being ignored.

## Blocks
```
if_exists "Allow" {
  tap "Allow"
} else {
  tap "Skip"
}
repeat 3 times {
  swipe left
}
repeat while "Load more" {
  tap "Load more"
}
repeat until "End of list" max:20 {
  scroll down
}
```
`if_exists` takes a predicate; `else` must be written `} else {`. `repeat while|until` also accept `js:"..."`.
Blocks must close with `}` on their own line.

## Include another script
`run "./login.mob" email="a@b.c" password="x"` inlines `login.mob` from the same project, binding its
`${email}`/`${password}` params. No directories, max nesting 4, no recursion. Requires a project context
(validate with `?projectId=`).

## Params
`# Param: email = qa@example.com` declares a default; `# Param: token` (no `=`) makes it required at run time.
Use `${email}` in any string. `${missing}` is an error at run time.
