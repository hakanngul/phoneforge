// Package cli implements the phoneforge command-line client: argument parsing,
// device resolution, HTTP calls to a PhoneForge server and output formatting.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// DefaultServer is used when neither --server nor PHONEFORGE_URL is set.
const DefaultServer = "http://127.0.0.1:8700"

// Exit codes.
const (
	ExitOK          = 0
	ExitError       = 1
	ExitUsage       = 2
	ExitDevice      = 3
	ExitUnreachable = 4
)

// Env carries the process environment so the CLI is testable.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
}

func (e Env) getenv(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}

// exitError carries a specific exit code.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func usagef(format string, a ...any) error {
	return &exitError{ExitUsage, fmt.Errorf(format, a...)}
}

func deviceErrf(format string, a ...any) error {
	return &exitError{ExitDevice, fmt.Errorf(format, a...)}
}

var errHelp = errors.New("help requested")

// globals are the flags accepted before and after every command.
type globals struct {
	server  string
	device  string
	json    bool
	timeout time.Duration
}

func (g *globals) bind(fs *flag.FlagSet) {
	fs.StringVar(&g.server, "server", g.server, "")
	fs.StringVar(&g.device, "device", g.device, "")
	fs.StringVar(&g.device, "d", g.device, "")
	fs.BoolVar(&g.json, "json", g.json, "")
	fs.DurationVar(&g.timeout, "timeout", g.timeout, "")
}

// call is the per-invocation state handed to a command.
type call struct {
	ctx  context.Context
	g    *globals
	env  Env
	out  io.Writer
	errw io.Writer
	name string // command name as typed (for flag sets / help)
	args []string
	cl   *client
}

type command struct {
	name    string
	summary string
	run     func(c *call) error
}

var commands []command

func init() {
	commands = []command{
		{"devices", "list connected devices", cmdDevices},
		{"device", "device info", cmdDevice},
		{"screenshot", "capture the screen to a PNG", cmdScreenshot},
		{"tree", "print the UI tree (compact text)", cmdTree},
		{"tap", "tap coordinates, or an element by text/id", cmdTap},
		{"longpress", "long-press at coordinates", cmdLongPress},
		{"swipe", "swipe between points or in a direction", cmdSwipe},
		{"type", "type text into the focused field", cmdType},
		{"key", "press a hardware/navigation key", cmdKey},
		{"apps", "list installed apps", cmdApps},
		{"launch", "launch an app", cmdLaunch},
		{"stop", "stop an app", cmdStop},
		{"install", "install an .apk / .ipa / .app", cmdInstall},
		{"uninstall", "uninstall an app", cmdUninstall},
		{"open", "open a URL or deep link", cmdOpen},
		{"ocr", "recognise on-screen text", cmdOCR},
		{"wait", "wait for text/element to appear", cmdWait},
		{"dsl", "execute DSL actions from a JSON file", cmdDSL},
		{"run", "run a .mob (or DSL .json) test script", cmdRun},
		{"logs", "stream device logs", cmdLogs},
		{"record", "record the screen to MP4", cmdRecord},
		{"crashes", "list crash reports", cmdCrashes},
		{"crash", "print one crash report", cmdCrash},
		{"bridge", "start|stop|status of the UIA2 / WDA bridge", cmdBridge},
		{"ios", "ios build-wda: build WebDriverAgent on this Mac", cmdIOS},
		{"sims", "list iOS simulators", cmdSims},
		{"sim", "boot|shutdown a simulator", cmdSim},
		{"agent", "run an AI agent on the device and stream it", cmdAgent},
		{"server", "server status|history|start|stop", cmdServer},
		{"version", "print the CLI version", cmdVersion},
		{"help", "show help", cmdHelp},
	}
}

func findCommand(name string) *command {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}
	return nil
}

// Main runs the CLI and returns the process exit code.
func Main(ctx context.Context, args []string, env Env) int {
	if env.Stdout == nil {
		env.Stdout = io.Discard
	}
	if env.Stderr == nil {
		env.Stderr = io.Discard
	}
	g := &globals{server: DefaultServer}
	if v := env.getenv("PHONEFORGE_URL"); v != "" {
		g.server = v
	}
	g.device = env.getenv("PHONEFORGE_DEVICE")

	// Global flags before the command.
	fs := newFlagSet("phoneforge")
	g.bind(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(env.Stdout, rootHelp())
			return ExitOK
		}
		return report(env, g, usagef("%v", err))
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprint(env.Stderr, rootHelp())
		return ExitUsage
	}
	name := rest[0]
	if name == "-h" || name == "--help" {
		fmt.Fprint(env.Stdout, rootHelp())
		return ExitOK
	}
	cmd := findCommand(name)
	if cmd == nil {
		return report(env, g, usagef("unknown command %q (try 'phoneforge help')", name))
	}
	c := &call{ctx: ctx, g: g, env: env, out: env.Stdout, errw: env.Stderr, name: name, args: rest[1:]}
	err := cmd.run(c)
	if errors.Is(err, errHelp) {
		fmt.Fprint(env.Stdout, helpFor(name, c.args))
		return ExitOK
	}
	if err != nil {
		return report(env, g, err)
	}
	return ExitOK
}

func report(env Env, g *globals, err error) int {
	code := ExitError
	var ee *exitError
	if errors.As(err, &ee) {
		code = ee.code
	}
	var ae *apiError
	if errors.As(err, &ae) && ae.Code == "DEVICE_NOT_FOUND" {
		code = ExitDevice
	}
	if g.json {
		b, _ := json.Marshal(map[string]any{"error": err.Error(), "exitCode": code})
		fmt.Fprintln(env.Stderr, string(b))
	} else {
		fmt.Fprintf(env.Stderr, "phoneforge: %s\n", err)
		if code == ExitUsage {
			fmt.Fprintln(env.Stderr, "Run 'phoneforge help' for usage.")
		}
	}
	return code
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parse parses interspersed flags and positionals. Everything after a bare "--"
// is positional. Global flags are accepted on every command.
func (c *call) parse(fs *flag.FlagSet, args []string) ([]string, error) {
	c.g.bind(fs)
	var tail []string
	for i, a := range args {
		if a == "--" {
			tail = args[i+1:]
			args = args[:i]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, errHelp
			}
			return nil, usagef("%s: %v", c.name, err)
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return append(pos, tail...), nil
}

// ctxFor derives the operation context. def is the default timeout (0 = none);
// an explicit --timeout always wins.
func (c *call) ctxFor(def time.Duration) (context.Context, context.CancelFunc) {
	d := def
	if c.g.timeout > 0 {
		d = c.g.timeout
	}
	if d <= 0 {
		return context.WithCancel(c.ctx)
	}
	return context.WithTimeout(c.ctx, d)
}

func (c *call) client() *client {
	if c.cl == nil {
		c.cl = newClient(c.g.server, c.env.getenv("PHONEFORGE_TOKEN"))
	}
	return c.cl
}

// target resolves the --device flag/env into a device and returns its id.
func (c *call) target(ctx context.Context) (string, error) {
	d, err := resolveDevice(ctx, c.client(), c.g.device)
	if err != nil {
		return "", err
	}
	return d.ID, nil
}

func (c *call) printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(c.out, string(b))
	return nil
}

func (c *call) printRaw(raw []byte) error {
	var buf strings.Builder
	var v any
	if json.Unmarshal(raw, &v) != nil {
		fmt.Fprintln(c.out, strings.TrimRight(string(raw), "\n"))
		return nil
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	buf.Write(b)
	fmt.Fprintln(c.out, buf.String())
	return nil
}

// noArgs rejects stray positionals.
func noArgs(c *call, pos []string) error {
	if len(pos) > 0 {
		return usagef("%s: unexpected argument %q", c.name, pos[0])
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

var _ = os.Stdout
