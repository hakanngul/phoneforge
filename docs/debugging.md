# Debugging and crash reports

## Crash reports
`GET /api/v1/devices/{id}/crashes?app=com.example.app&since=2026-01-01T00:00:00Z&limit=20` lists reports; the `name` of a
row is used for `GET .../crashes/{name}` (plain text) and `DELETE .../crashes/{name}` (iOS devices and simulators).
Android reads the crash log buffer and DropBox through adb; tombstones appear only when `/data/tombstones` is readable.
Android timestamps use the device's UTC offset. Deleting Android reports is not possible (they live in system buffers).

## lldb sessions
Requirements: Xcode (lldb), and for the local machine `DevToolsSecurity -status` must report Developer mode enabled -
otherwise lldb blocks on a macOS authorization prompt and the attach times out with that hint.

```
POST /api/v1/debug/sessions {"deviceId":"sim-...","bundleId":"com.apple.Preferences"}            # attach to running app
POST /api/v1/debug/sessions {"deviceId":"ios-...","bundleId":"com.me.app","launch":true}         # launch suspended + attach
POST /api/v1/debug/sessions/{sid}/breakpoints {"file":"ViewController.m","line":42}              # or {"symbol":"-[Foo bar]"}
POST /api/v1/debug/sessions/{sid}/continue {"waitMs":5000}                                        # also step-over|step-into|step-out|interrupt
GET  /api/v1/debug/sessions/{sid}/state      GET .../backtrace      GET .../variables?frame=1
POST /api/v1/debug/sessions/{sid}/eval {"expression":"self.title"}
DELETE /api/v1/debug/sessions/{sid}                                                               # detach (app keeps running)
```
Real devices only accept development-signed apps with `get-task-allow` and need CoreDevice to see the phone (unlocked, paired,
Developer Mode, tunnel up). No raw lldb command endpoint exists on purpose; every parameter is single-line validated.
