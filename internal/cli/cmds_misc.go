package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"pocketlab/internal/dsl"
	"pocketlab/internal/iossim"

	"pocketlab/internal/version"
)

// ---- dsl / run ----

func formatSteps(w io.Writer, steps []dsl.StepResult, indent string) {
	for _, s := range steps {
		mark := "ok  "
		switch s.Status {
		case "ok":
		case "skipped":
			mark = "skip"
		case "failed":
			mark = "FAIL"
		default:
			mark = strings.ToUpper(truncate(s.Status, 4))
		}
		line := fmt.Sprintf("%s%s %2d %s", indent, mark, s.Index, s.Action)
		if s.ID != "" {
			line += " [" + s.ID + "]"
		}
		line += fmt.Sprintf(" (%dms)", s.DurationMs)
		if s.Error != "" {
			line += ": " + truncate(s.Error, 200)
			if s.Code != "" {
				line += " [" + s.Code + "]"
			}
		}
		fmt.Fprintln(w, line)
		formatSteps(w, s.ThenResults, indent+"    ")
		formatSteps(w, s.ElseResults, indent+"    ")
		formatSteps(w, s.BodyResults, indent+"    ")
	}
}

func printDSLResult(c *call, raw json.RawMessage, res dsl.Result, what string) error {
	if c.g.json {
		if err := c.printRaw(raw); err != nil {
			return err
		}
	} else {
		formatSteps(c.out, res.Steps, "")
		status := res.Status
		if status == "" {
			status = map[bool]string{true: "passed", false: "failed"}[res.Passed]
		}
		fmt.Fprintf(c.out, "%s %s in %dms\n", what, status, res.TotalMs)
	}
	if !res.Passed {
		return &exitError{ExitError, fmt.Errorf("%s did not pass", what)}
	}
	return nil
}

func readInput(c *call, path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(io.LimitReader(c.env.Stdin, 8<<20))
	}
	return os.ReadFile(path)
}

func cmdDSL(c *call) error {
	pos, err := c.parse(newFlagSet(c.name), c.args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: pocketlab dsl <file.json|->")
	}
	body, err := readInput(c, pos[0])
	if err != nil {
		return fmt.Errorf("dsl: %w", err)
	}
	if !json.Valid(body) {
		return usagef("dsl: %s is not valid JSON", pos[0])
	}
	ctx, cancel := c.ctxFor(10 * time.Minute)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.client().do(ctx, "POST", devPath(id, "/dsl/execute"), []byte(body), &raw); err != nil {
		return err
	}
	var res dsl.Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("bad DSL result: %w", err)
	}
	return printDSLResult(c, raw, res, "dsl")
}

var scriptNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func cmdRun(c *call) error {
	fs := newFlagSet(c.name)
	project := fs.String("project", "cli", "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: pocketlab run <file.mob|file.json> [--project NAME]")
	}
	ext := strings.ToLower(filepath.Ext(pos[0]))
	if ext != ".mob" && ext != ".json" {
		return usagef("run: script must end in .mob or .json")
	}
	body, err := os.ReadFile(pos[0])
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	name := scriptNameRe.ReplaceAllString(filepath.Base(pos[0]), "_")
	if name == "" || name[0] == '.' || name[0] == '_' || name[0] == '-' {
		name = "s" + name
	}
	ctx, cancel := c.ctxFor(15 * time.Minute)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	cl := c.client()
	var projects []struct{ ID, Name string }
	if err := cl.do(ctx, "GET", "/tests/projects", nil, &projects); err != nil {
		return err
	}
	pid := ""
	for _, p := range projects {
		if p.Name == *project {
			pid = p.ID
			break
		}
	}
	if pid == "" {
		var p struct{ ID string }
		if err := cl.do(ctx, "POST", "/tests/projects", map[string]string{"name": *project}, &p); err != nil {
			return err
		}
		pid = p.ID
	}
	sp := "/tests/projects/" + url.PathEscape(pid) + "/scripts/" + url.PathEscape(name)
	if _, _, err := cl.raw(ctx, "PUT", sp, body); err != nil {
		return err
	}
	var raw json.RawMessage
	if err := cl.do(ctx, "POST", sp+"/run", map[string]string{"deviceId": id}, &raw); err != nil {
		return err
	}
	var rec struct {
		Passed bool       `json:"passed"`
		Result dsl.Result `json:"result"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return fmt.Errorf("bad run result: %w", err)
	}
	rec.Result.Passed = rec.Passed
	return printDSLResult(c, raw, rec.Result, "run "+filepath.Base(pos[0]))
}

// ---- logs / record ----

func cmdLogs(c *call) error {
	fs := newFlagSet(c.name)
	follow := fs.Bool("f", false, "")
	fs.BoolVar(follow, "follow", false, "")
	secs := fs.Int("seconds", 3, "")
	grep := fs.String("grep", "", "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if err := noArgs(c, pos); err != nil {
		return err
	}
	ctx, cancel := c.ctxFor(0)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	sctx := ctx
	if !*follow {
		var scancel context.CancelFunc
		sctx, scancel = context.WithTimeout(ctx, time.Duration(*secs)*time.Second)
		defer scancel()
	}
	lg := strings.ToLower(*grep)
	err = c.client().sse(sctx, "GET", devPath(id, "/logs"), nil, func(_, data string) bool {
		if lg != "" && !strings.Contains(strings.ToLower(data), lg) {
			return true
		}
		fmt.Fprintln(c.out, data)
		return true
	})
	if err != nil && !(errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
		return err
	}
	return nil
}

func cmdRecord(c *call) error {
	fs := newFlagSet(c.name)
	secs := fs.Int("seconds", 10, "")
	out := fs.String("o", "", "")
	fs.StringVar(out, "output", "", "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if err := noArgs(c, pos); err != nil {
		return err
	}
	if *secs <= 0 {
		return usagef("record: --seconds must be > 0")
	}
	ctx, cancel := c.ctxFor(time.Duration(*secs)*time.Second + 2*time.Minute)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	data, _, err := c.client().raw(ctx, "GET", fmt.Sprintf("%s?seconds=%d", devPath(id, "/recording"), *secs), nil)
	if err != nil {
		return err
	}
	path := *out
	if path == "" {
		path = "recording-" + time.Now().Format("20060102-150405") + ".mp4"
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	if c.g.json {
		return c.printJSON(map[string]any{"path": path, "bytes": len(data), "seconds": *secs})
	}
	fmt.Fprintf(c.out, "saved %s (%d bytes, %ds)\n", path, len(data), *secs)
	return nil
}

// ---- crashes ----

type crashRow struct {
	Name          string    `json:"name"`
	Source        string    `json:"source"`
	App           string    `json:"app"`
	BundleID      string    `json:"bundleId"`
	Timestamp     time.Time `json:"timestamp"`
	ExceptionType string    `json:"exceptionType"`
	Signal        string    `json:"signal"`
	Kind          string    `json:"kind"`
}

func cmdCrashes(c *call) error {
	fs := newFlagSet(c.name)
	app := fs.String("app", "", "")
	limit := fs.Int("limit", 20, "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if err := noArgs(c, pos); err != nil {
		return err
	}
	ctx, cancel := c.ctxFor(60 * time.Second)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	q := url.Values{}
	if *app != "" {
		q.Set("app", *app)
	}
	if *limit > 0 {
		q.Set("limit", fmt.Sprint(*limit))
	}
	path := devPath(id, "/crashes")
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var raw json.RawMessage
	if err := c.client().do(ctx, "GET", path, nil, &raw); err != nil {
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	var rows []crashRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(c.out, "no crash reports")
		return nil
	}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		exc := firstNonEmpty(r.ExceptionType, r.Signal)
		out = append(out, []string{r.Timestamp.Local().Format("2006-01-02 15:04:05"), dash(r.Kind), dash(firstNonEmpty(r.BundleID, r.App)), dash(truncate(exc, 40)), r.Name})
	}
	table(c.out, []string{"TIME", "KIND", "APP", "EXCEPTION", "NAME"}, out)
	return nil
}

func cmdCrash(c *call) error {
	fs := newFlagSet(c.name)
	out := fs.String("o", "", "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: pocketlab crash <name> [-o file]")
	}
	ctx, cancel := c.ctxFor(60 * time.Second)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	b, _, err := c.client().raw(ctx, "GET", devPath(id, "/crashes/"+url.PathEscape(pos[0])), nil)
	if err != nil {
		return err
	}
	if *out != "" {
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			return err
		}
		return c.actionResult(fmt.Sprintf("saved %s (%d bytes)", *out, len(b)))
	}
	if c.g.json {
		return c.printJSON(map[string]string{"name": pos[0], "report": string(b)})
	}
	_, err = c.out.Write(b)
	return err
}

// ---- bridge ----

func cmdBridge(c *call) error {
	pos, err := c.parse(newFlagSet(c.name), c.args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: pocketlab bridge start|stop|status")
	}
	method, suffix, def := "GET", "/bridge", 20*time.Second
	switch pos[0] {
	case "status":
	case "start":
		method, suffix, def = "POST", "/bridge/start", 3*time.Minute
	case "stop":
		method, suffix, def = "POST", "/bridge/stop", 60*time.Second
	default:
		return usagef("bridge: unknown action %q (want start, stop or status)", pos[0])
	}
	ctx, cancel := c.ctxFor(def)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := c.client().do(ctx, method, devPath(id, suffix), nil, &raw); err != nil {
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	fmt.Fprint(c.out, formatBridge(m))
	return nil
}

func formatBridge(m map[string]any) string {
	var sb strings.Builder
	for _, k := range []string{"platform", "supported", "running", "state", "installed", "port", "sessionId", "wdaUrl", "mjpegUrl", "lastError", "error"} {
		v, ok := m[k]
		if !ok {
			continue
		}
		if s, isStr := v.(string); isStr && s == "" {
			continue
		}
		if f, isNum := v.(float64); isNum && f == 0 {
			continue
		}
		fmt.Fprintf(&sb, "%-10s %v\n", k+":", v)
	}
	if sb.Len() == 0 {
		return "no bridge information\n"
	}
	return sb.String()
}

// ---- simulators ----

type simRow struct {
	UDID     string `json:"udid"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Runtime  string `json:"runtime"`
	Platform string `json:"platform"`
	DeviceID string `json:"deviceId"`
	WDA      struct {
		State string `json:"state"`
	} `json:"wda"`
}

func listSims(ctx context.Context, c *call) ([]simRow, json.RawMessage, error) {
	var raw json.RawMessage
	if err := c.client().do(ctx, "GET", "/simulators", nil, &raw); err != nil {
		return nil, nil, err
	}
	var env struct {
		Simulators []simRow `json:"simulators"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, nil, err
	}
	return env.Simulators, raw, nil
}

func cmdSims(c *call) error {
	pos, err := c.parse(newFlagSet(c.name), c.args)
	if err != nil {
		return err
	}
	if err := noArgs(c, pos); err != nil {
		return err
	}
	ctx, cancel := c.ctxFor(60 * time.Second)
	defer cancel()
	sims, raw, err := listSims(ctx, c)
	if err != nil {
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	if len(sims) == 0 {
		fmt.Fprintln(c.out, "no simulators")
		return nil
	}
	rows := make([][]string, 0, len(sims))
	for _, s := range sims {
		rows = append(rows, []string{s.UDID, s.Name, s.State, s.Runtime, dash(s.WDA.State)})
	}
	table(c.out, []string{"UDID", "NAME", "STATE", "RUNTIME", "WDA"}, rows)
	return nil
}

func pickSim(sims []simRow, want string) (simRow, error) {
	lw := strings.ToLower(strings.TrimSpace(want))
	for _, step := range []func(s simRow) bool{
		func(s simRow) bool { return strings.ToLower(s.UDID) == lw || strings.ToLower(s.Name) == lw },
		func(s simRow) bool {
			return strings.HasPrefix(strings.ToLower(s.UDID), lw) || strings.HasPrefix(strings.ToLower(s.Name), lw)
		},
	} {
		var hit []simRow
		for _, s := range sims {
			if step(s) {
				hit = append(hit, s)
			}
		}
		if len(hit) == 1 {
			return hit[0], nil
		}
		if len(hit) > 1 {
			var names []string
			for _, s := range hit {
				names = append(names, fmt.Sprintf("  %s  (%s)", s.UDID, s.Name))
			}
			return simRow{}, deviceErrf("%q matches %d simulators; use the UDID:\n%s", want, len(hit), strings.Join(names, "\n"))
		}
	}
	return simRow{}, deviceErrf("simulator %q not found (see 'pocketlab sims')", want)
}

const simUsage = "usage: pocketlab sim boot|shutdown|unslim|mem <udid|name>   |   pocketlab sim slim <udid|name> [--profile minimal-safe|aggressive|custom] [--groups a,b] [--dry-run]"

func cmdSim(c *call) error {
	fs := newFlagSet(c.name)
	profile := fs.String("profile", "", "slim profile: minimal-safe (default), aggressive or custom")
	groups := fs.String("groups", "", "comma-separated slim groups (implies --profile custom)")
	dry := fs.Bool("dry-run", false, "slim: show what would be disabled without changing anything")
	discover := fs.Bool("discover", false, "slim: list launchd labels with memory (diagnostic, runs locally)")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	verbs := map[string]bool{"boot": true, "shutdown": true, "slim": true, "unslim": true, "mem": true}
	if len(pos) != 2 || !verbs[pos[0]] {
		return usagef("%s", simUsage)
	}
	verb := pos[0]
	if (*profile != "" || *groups != "" || *dry || *discover) && verb != "slim" {
		return usagef("--profile, --groups, --dry-run and --discover only apply to 'sim slim'")
	}
	ctx, cancel := c.ctxFor(5 * time.Minute)
	defer cancel()
	if verb == "slim" && *discover {
		return simDiscover(ctx, c, pos[1])
	}
	sims, _, err := listSims(ctx, c)
	if err != nil {
		return err
	}
	s, err := pickSim(sims, pos[1])
	if err != nil {
		return err
	}
	base := "/simulators/" + url.PathEscape(s.UDID)
	var raw json.RawMessage
	switch verb {
	case "slim":
		body := map[string]any{"dryRun": *dry}
		if *profile != "" {
			body["profile"] = *profile
		}
		if *groups != "" {
			var gs []string
			for _, g := range strings.Split(*groups, ",") {
				if g = strings.TrimSpace(g); g != "" {
					gs = append(gs, g)
				}
			}
			body["groups"] = gs
		}
		if err := c.client().do(ctx, "POST", base+"/slim", body, &raw); err != nil {
			return err
		}
		if c.g.json {
			return c.printRaw(raw)
		}
		return printSlimResult(c, s, raw)
	case "unslim":
		if err := c.client().do(ctx, "POST", base+"/unslim", nil, &raw); err != nil {
			return err
		}
		if c.g.json {
			return c.printRaw(raw)
		}
		var r struct {
			Enabled  []string `json:"enabled"`
			Failed   []string `json:"failed"`
			Rebooted bool     `json:"rebooted"`
		}
		_ = json.Unmarshal(raw, &r)
		fmt.Fprintf(c.out, "%s (%s): unslimmed, %d labels re-enabled", s.Name, s.UDID, len(r.Enabled))
		if len(r.Failed) > 0 {
			fmt.Fprintf(c.out, ", %d failed", len(r.Failed))
		}
		if r.Rebooted {
			fmt.Fprint(c.out, ", rebooted")
		}
		fmt.Fprintln(c.out)
		return nil
	case "mem":
		if err := c.client().do(ctx, "GET", base+"/memory", nil, &raw); err != nil {
			return err
		}
		if c.g.json {
			return c.printRaw(raw)
		}
		var m struct {
			RSSMB       float64 `json:"rssMB"`
			FootprintMB float64 `json:"footprintMB"`
			ProcCount   int     `json:"procCount"`
		}
		_ = json.Unmarshal(raw, &m)
		fmt.Fprintf(c.out, "%s (%s): footprint %.0f MB, RSS %.0f MB (overcounts shared pages), %d processes\n", s.Name, s.UDID, m.FootprintMB, m.RSSMB, m.ProcCount)
		return nil
	}
	if err := c.client().do(ctx, "POST", base+"/"+verb, nil, &raw); err != nil {
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	var after simRow
	_ = json.Unmarshal(raw, &after)
	fmt.Fprintf(c.out, "%s (%s): %s\n", s.Name, s.UDID, firstNonEmpty(after.State, verb))
	return nil
}

func printSlimResult(c *call, s simRow, raw json.RawMessage) error {
	var r struct {
		Profile    string   `json:"profile"`
		Groups     []string `json:"groups"`
		Applied    []string `json:"applied"`
		Missing    []string `json:"missing"`
		Failed     []string `json:"failed"`
		Protected  []string `json:"protected"`
		DryRun     bool     `json:"dryRun"`
		BaselineMB float64  `json:"baselineMB"`
	}
	_ = json.Unmarshal(raw, &r)
	verb := "disabled"
	if r.DryRun {
		verb = "would disable"
	}
	fmt.Fprintf(c.out, "%s (%s): profile %s (%s)\n", s.Name, s.UDID, r.Profile, strings.Join(r.Groups, ", "))
	fmt.Fprintf(c.out, "  %s %d labels, %d missing, %d failed, %d protected by the keep list\n", verb, len(r.Applied), len(r.Missing), len(r.Failed), len(r.Protected))
	if r.BaselineMB > 0 {
		fmt.Fprintf(c.out, "  footprint before: %.0f MB (check 'pocketlab sim mem' in ~30 s)\n", r.BaselineMB)
	}
	if r.DryRun {
		for _, l := range r.Applied {
			fmt.Fprintln(c.out, "    "+l)
		}
	}
	for _, l := range r.Failed {
		fmt.Fprintln(c.out, "  failed: "+l)
	}
	return nil
}

// simDiscover is the hidden `sim slim <udid> --discover`: label inventory of a
// booted simulator joined with per-process memory (runs locally via simctl).
func simDiscover(ctx context.Context, c *call, want string) error {
	sims, err := iossim.ListAll(ctx, iossim.ExecRunner)
	if err != nil {
		return err
	}
	rows := make([]simRow, 0, len(sims))
	byUDID := map[string]iossim.Sim{}
	for _, s := range sims {
		rows = append(rows, simRow{UDID: s.UDID, Name: s.Name, State: s.State, Runtime: s.Runtime, Platform: s.Platform})
		byUDID[s.UDID] = s
	}
	row, err := pickSim(rows, want)
	if err != nil {
		return err
	}
	sim := byUDID[row.UDID]
	if !sim.Booted() || sim.Platform != "iOS" {
		return deviceErrf("%s is not a booted iOS simulator", row.Name)
	}
	labels, err := iossim.Discover(ctx, iossim.ExecRunner, iossim.ExecHost, sim.UDID, sim.RuntimeID)
	if err != nil {
		return err
	}
	if c.g.json {
		return c.printJSON(labels)
	}
	var total float64
	out := make([][]string, 0, len(labels))
	for _, l := range labels {
		total += l.FootprintMB
		flag := ""
		if l.Kept {
			flag = "keep"
		} else if l.Disabled {
			flag = "disabled"
		}
		out = append(out, []string{fmt.Sprintf("%.1f", l.FootprintMB), fmt.Sprintf("%.1f", l.RSSMB), l.Domain, strings.Join(l.Groups, ","), flag, l.Label})
	}
	table(c.out, []string{"FOOTPRINT MB", "RSS MB", "DOMAIN", "GROUPS", "FLAG", "LABEL"}, out)
	fmt.Fprintf(c.out, "\n%d labels, %.0f MB attributed to labeled processes\n", len(labels), total)
	return nil
}

// ---- version ----

func cmdVersion(c *call) error {
	pos, err := c.parse(newFlagSet(c.name), c.args)
	if err != nil {
		return err
	}
	if err := noArgs(c, pos); err != nil {
		return err
	}
	if c.g.json {
		return c.printJSON(map[string]string{"version": version.Version})
	}
	fmt.Fprintf(c.out, "pocketlab %s\n", version.Version)
	return nil
}

var _ = sort.Strings
