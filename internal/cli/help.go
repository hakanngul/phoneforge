package cli

import (
	"fmt"
	"strings"
)

func rootHelp() string {
	var sb strings.Builder
	sb.WriteString(`phoneforge - command-line client for a running PhoneForge server

Usage:
  phoneforge [global flags] <command> [args]

Global flags (accepted before or after the command):
  --server URL       server base URL (env PHONEFORGE_URL, default ` + DefaultServer + `)
  -d, --device ID    device id, serial, name or unique prefix like "iphone" / "sm-g"
                     (env PHONEFORGE_DEVICE; optional when exactly one device is connected)
  --json             machine-readable output
  --timeout DUR      operation timeout, e.g. 30s (per-command default otherwise)

Commands:
`)
	for _, c := range commands {
		fmt.Fprintf(&sb, "  %-11s %s\n", c.name, c.summary)
	}
	sb.WriteString(`
Coordinates are in the units the server's tap endpoint uses: iOS points,
Android pixels. 'phoneforge tree' prints them as @(x,y) tap centers.

Exit codes: 0 ok, 1 error, 2 usage, 3 device not found/ambiguous,
            4 server unreachable (start phoneforged).

Run 'phoneforge help <command>' for details.
`)
	return sb.String()
}

const coordNote = "Coordinates: iOS points, Android pixels (same units as 'tree' @(x,y))."

var helpText = map[string]string{
	"devices": `Usage: phoneforge devices [--json]

List devices known to the server: id, name, platform, OS version, state.`,

	"device": `Usage: phoneforge device info [-d DEVICE] [--json]

Show details for one device (id, name, platform, model, OS, serial, state).`,

	"screenshot": `Usage: phoneforge screenshot [-o file.png] [-d DEVICE]

Capture the screen as PNG. Default file: ./screenshot-<timestamp>.png.
"-o -" writes the PNG to stdout (refused when stdout is a terminal).`,

	"tree": `Usage: phoneforge tree [--filter TEXT] [--max-nodes N] [--offscreen] [--json]

Print the UI tree as compact text, one line per meaningful node:
  [12] Button "Settings" id=com.apple.settings @(201,337) 370x53 clickable
@(x,y) is the tap center. --filter keeps nodes whose text/label/id contains TEXT
(case-insensitive) plus their clickable ancestor. --json prints the raw hierarchy.
` + coordNote,

	"tap": `Usage: phoneforge tap X Y
       phoneforge tap --text TEXT [--index N] [--ocr]
       phoneforge tap --id RESOURCE_ID [--index N]

Tap coordinates, or find an element in the UI tree and tap its center.
  --text TEXT   case-insensitive match on text/label (exact matches rank first)
  --id ID       resource id, exact or the part after "/" (e.g. "btn_ok")
  --index N     pick the Nth match (default 0)
  --ocr         with --text: if the tree has no match, fall back to on-screen
                text recognition (POST /ocr/devices/{id}/tap)
` + coordNote,

	"longpress": `Usage: phoneforge longpress X Y [--ms N]

Press and hold at a point (default 800 ms).
` + coordNote,

	"swipe": `Usage: phoneforge swipe X1 Y1 X2 Y2 [--ms N]
       phoneforge swipe up|down|left|right [--ms N]

Swipe between two points (default 300 ms). A direction swipes the finger that
way across the middle of the screen: "swipe up" moves the finger up (scrolls
content down), "swipe left" moves it left (next page).
` + coordNote,

	"type": `Usage: phoneforge type "text"

Type text into the focused field. Use "--" before text starting with "-":
  phoneforge type -- "-5 degrees"`,

	"key": `Usage: phoneforge key <name>

Press a key: home, back, enter, delete, and (where the driver supports them)
volume_up, volume_down. Unsupported keys are reported by the server.`,

	"apps": `Usage: phoneforge apps [--filter TEXT] [--json]

List installed apps (bundle id / package, name, version).`,

	"launch": `Usage: phoneforge launch <bundle-id|package>

Launch an app.`,

	"stop": `Usage: phoneforge stop <bundle-id|package>

Stop (terminate) an app.`,

	"install": `Usage: phoneforge install <file.apk|file.ipa|dir.app>

Install an app. APK/IPA files are uploaded to the server. An .app bundle
(directory) is installed from its path, so the server must run on this machine.`,

	"uninstall": `Usage: phoneforge uninstall <bundle-id|package>

Remove an app from the device.`,

	"open": `Usage: phoneforge open <url>

Open a URL or deep link on the device.`,

	"ocr": `Usage: phoneforge ocr [--find TEXT] [--exact] [--regex] [--case-sensitive] [--words] [--json]

Recognise on-screen text. Without --find, list all text boxes. With --find,
list matches only (exit 1 if none). Boxes are in screenshot pixels, which may
be larger than the point/pixel space used by tap; use 'tap --text TEXT --ocr'
to tap recognised text with the right scaling.`,

	"wait": `Usage: phoneforge wait --text TEXT | --id RESOURCE_ID [--gone] [--ocr] [--timeout 10s]

Poll the UI tree until the element appears (or, with --gone, disappears).
--ocr also polls OCR when the tree has no match (--text only).
Exit 0 when satisfied, 1 on timeout. Default timeout 10s; --interval sets the poll period.`,

	"dsl": `Usage: phoneforge dsl <file.json|->

Execute a DSL document (JSON, "-" reads stdin) on the device and print one line
per step. Exit 1 if the run did not pass. --json prints the raw result.`,

	"run": `Usage: phoneforge run <file.mob|file.json> [--project NAME]

Run a test script. The script is saved into a server-side test project
(default "cli", created on first use) as its base name, run on the device, and
the per-step result printed. Exit 1 if it did not pass.`,

	"logs": `Usage: phoneforge logs [-f] [--seconds N] [--grep TEXT]

Print device log lines. Without -f, collect for --seconds (default 3) and exit.
With -f, follow until Ctrl-C. --grep filters lines (case-insensitive).`,

	"record": `Usage: phoneforge record [--seconds N] [-o file.mp4]

Record the screen (default 10 s) and save the MP4 (default ./recording-<ts>.mp4).`,

	"crashes": `Usage: phoneforge crashes [--app BUNDLE] [--limit N] [--json]

List crash reports on the device, newest first (default limit 20).`,

	"crash": `Usage: phoneforge crash <name> [-o file]

Print one crash report (names come from 'phoneforge crashes').`,

	"bridge": `Usage: phoneforge bridge start|stop|status [-d DEVICE]

Control the on-device automation bridge: UiAutomator2 on Android, WebDriverAgent
on iOS. 'start' can take a minute on iOS.`,

	"sims": `Usage: phoneforge sims [--json]

List iOS simulators with state, runtime and WDA state.`,

	"sim": `Usage: phoneforge sim boot|shutdown <udid|name>

Boot or shut down an iOS simulator. The name may be a unique prefix.`,

	"agent": `Usage: phoneforge agent "prompt" [--model haiku] [--agent NAME] [--max-steps N]

Start an AI run (type "automation") on the device and stream it compactly:
plain lines are the agent's thoughts, "▸ tool(args)" are actions, and
"✓ done: ..." the result. Ctrl-C cancels the run on the server.
  --model M     model for this run (applied by briefly setting the server's
                automation model; settings are restored right after start)
  --agent NAME  agent CLI (default: the one chosen in AI settings)
  --session ID  resume an earlier Claude Code session
  --max-steps N step limit for the built-in agent (ignored by other agents)
With --json, raw events are printed one per line.`,

	"server": `Usage: phoneforge server status|stop

status: show server URL, status, version and device count (GET /health).
stop:   ask the server to shut down gracefully (POST /server/shutdown); a
        desktop app attached to it quits as well.`,

	"version": `Usage: phoneforge version

Print the CLI version.`,

	"help": `Usage: phoneforge help [command]

Show general help, or help for one command. 'phoneforge <command> --help' works too.`,
}

func helpFor(name string, _ []string) string {
	t, ok := helpText[name]
	if !ok {
		return rootHelp()
	}
	return t + "\n\nGlobal flags: --server URL, -d/--device ID, --json, --timeout DUR (see 'phoneforge help').\n"
}

func cmdHelp(c *call) error {
	if len(c.args) == 0 {
		fmt.Fprint(c.out, rootHelp())
		return nil
	}
	name := c.args[0]
	if name == "-h" || name == "--help" {
		fmt.Fprint(c.out, rootHelp())
		return nil
	}
	if _, ok := helpText[name]; !ok {
		return usagef("no help for unknown command %q", name)
	}
	fmt.Fprint(c.out, helpFor(name, nil))
	return nil
}
