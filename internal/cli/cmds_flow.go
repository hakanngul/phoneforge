package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"time"

	"pocketlab/internal/mobflow"
)

// Exit codes of the flow commands: 0 ok (run passed), 1 validation failure or a
// run that failed or was cancelled, 2 any other error. A server that cannot be
// reached keeps the global code 4.
const (
	flowExitInvalid = 1
	flowExitError   = 2
)

func flowErr(err error) error {
	if err == nil {
		return nil
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return err
	}
	return &exitError{flowExitError, err}
}

func cmdFlow(c *call) error {
	if len(c.args) == 0 {
		return errHelp
	}
	sub, rest := c.args[0], c.args[1:]
	switch sub {
	case "-h", "--help", "help":
		return errHelp
	case "list":
		return flowErr(flowList(c, rest))
	case "validate":
		return flowErr(flowValidate(c, rest))
	case "run":
		return flowErr(flowRun(c, rest, "run", ""))
	case "replay":
		return flowErr(flowRun(c, rest, "replay", "replay"))
	case "record":
		return flowErr(flowRun(c, rest, "record", "record"))
	case "cache":
		return flowErr(flowCache(c, rest))
	}
	return &exitError{flowExitError, fmt.Errorf("flow: unknown subcommand %q (list, validate, run, replay, record, cache)", sub)}
}

type flowInfo struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	BundleID string `json:"bundleId"`
	Platform string `json:"platform"`
	Steps    int    `json:"steps"`
	Valid    bool   `json:"valid"`
	Cache    struct {
		Present bool `json:"present"`
		Fresh   int  `json:"fresh"`
		Stale   int  `json:"stale"`
		Missing int  `json:"missing"`
	} `json:"cache"`
}

func flowList(c *call, args []string) error {
	fs := newFlagSet("flow list")
	project := fs.String("project", "", "")
	pos, err := c.parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("usage: pocketlab flow list [--project P] [--json]")
	}
	ctx, cancel := c.ctxFor(30 * time.Second)
	defer cancel()
	path := "/flows/cases"
	if *project != "" {
		path += "?project=" + url.QueryEscape(*project)
	}
	b, _, err := c.client().raw(ctx, "GET", path, nil)
	if err != nil {
		return err
	}
	if c.g.json {
		fmt.Fprintln(c.out, string(b))
		return nil
	}
	var list []flowInfo
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("bad flow list from server: %w", err)
	}
	if len(list) == 0 {
		fmt.Fprintln(c.out, "no flows")
		return nil
	}
	rows := make([][]string, 0, len(list))
	for _, f := range list {
		cache := "none"
		if f.Cache.Present {
			cache = fmt.Sprintf("fresh %d, stale %d, missing %d", f.Cache.Fresh, f.Cache.Stale, f.Cache.Missing)
		}
		valid := "ok"
		if !f.Valid {
			valid = "invalid"
		}
		rows = append(rows, []string{f.ID, truncate(f.Title, 40), dash(f.BundleID), dash(f.Platform), strconv.Itoa(f.Steps), valid, cache})
	}
	table(c.out, []string{"ID", "TITLE", "APP", "PLATFORM", "STEPS", "STATE", "CACHE"}, rows)
	return nil
}

func flowValidate(c *call, args []string) error {
	fs := newFlagSet("flow validate")
	pos, err := c.parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: pocketlab flow validate <file.mobflow|project/name>")
	}
	var errs mobflow.ValidationErrors
	var warnings []mobflow.Warning
	if _, serr := os.Stat(pos[0]); serr == nil {
		src, rerr := os.ReadFile(pos[0])
		if rerr != nil {
			return rerr
		}
		_, errs = mobflow.Parse(src)
	} else if project, name, ok := mobflow.SplitCaseID(pos[0]); ok {
		ctx, cancel := c.ctxFor(30 * time.Second)
		defer cancel()
		var e struct {
			Errors mobflow.ValidationErrors `json:"errors"`
		}
		if err := c.client().do(ctx, "GET", "/flows/cases/"+url.PathEscape(project)+"/"+url.PathEscape(name), nil, &e); err != nil {
			return err
		}
		errs = e.Errors
		// Lint warnings never fail validation; an older server without the
		// endpoint simply has none.
		var l struct {
			Warnings []mobflow.Warning `json:"warnings"`
		}
		if lerr := c.client().do(ctx, "GET", "/flows/cases/"+url.PathEscape(project)+"/"+url.PathEscape(name)+"/lint", nil, &l); lerr == nil {
			warnings = l.Warnings
		}
	} else {
		return fmt.Errorf("%q is neither an existing file nor a case id <project>/<name>", pos[0])
	}
	if c.g.json {
		if errs == nil {
			errs = mobflow.ValidationErrors{}
		}
		if warnings == nil {
			warnings = []mobflow.Warning{}
		}
		b, _ := json.Marshal(struct {
			OK       bool                     `json:"ok"`
			Errors   mobflow.ValidationErrors `json:"errors"`
			Warnings []mobflow.Warning        `json:"warnings"`
		}{len(errs) == 0, errs, warnings})
		fmt.Fprintln(c.out, string(b))
	} else {
		if len(errs) == 0 {
			fmt.Fprintf(c.out, "%s: ok\n", pos[0])
		}
		for _, e := range errs {
			fmt.Fprintf(c.errw, "%s: %s\n", pos[0], e)
		}
		for _, w := range warnings {
			fmt.Fprintf(c.errw, "%s: warning: %s\n", pos[0], w)
		}
	}
	if len(errs) > 0 {
		return &exitError{flowExitInvalid, fmt.Errorf("%d validation error(s)", len(errs))}
	}
	return nil
}

// flowPollInterval is how often a running run is polled (a var for tests).
var flowPollInterval = 500 * time.Millisecond

// defaultRunTimeout bounds `flow run|replay|record` unless --timeout is given.
const defaultRunTimeout = 30 * time.Minute

type paramList []string

func (p *paramList) String() string { return "" }
func (p *paramList) Set(v string) error {
	*p = append(*p, v)
	return nil
}

func parseParams(list []string) (map[string]string, error) {
	if len(list) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, kv := range list {
		k, v, ok := cutParam(kv)
		if !ok {
			return nil, usagef("--param %q must be name=value", kv)
		}
		out[k] = v
	}
	return out, nil
}

func cutParam(s string) (k, v string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return s[:i], s[i+1:], i > 0
		}
	}
	return "", "", false
}

func caseURL(project, name string) string {
	return "/flows/cases/" + url.PathEscape(project) + "/" + url.PathEscape(name)
}

func flowRun(c *call, args []string, cmd, fixedMode string) error {
	fs := newFlagSet("flow " + cmd)
	mode := fs.String("mode", "auto", "")
	agent := fs.String("agent", "", "")
	heal := fs.Bool("heal", false, "")
	junit := fs.String("junit", "", "")
	var params paramList
	fs.Var(&params, "param", "")
	pos, err := c.parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: pocketlab flow %s <project/name> [--device D] [--param k=v]...", cmd)
	}
	m := *mode
	if fixedMode != "" {
		m = fixedMode
	}
	switch m {
	case "auto", "record", "replay":
	default:
		return usagef("--mode must be auto, record or replay")
	}
	if *heal && m == "record" {
		return usagef("--heal applies to replay and auto runs, not record")
	}
	project, name, ok := mobflow.SplitCaseID(pos[0])
	if !ok {
		return usagef("%q is not a case id <project>/<name>", pos[0])
	}
	pm, err := parseParams(params)
	if err != nil {
		return err
	}

	ctx, cancel := c.ctxFor(defaultRunTimeout)
	defer cancel()
	devCtx, devCancel := c.ctxFor(30 * time.Second)
	devID, err := c.target(devCtx)
	devCancel()
	if err != nil {
		return err
	}
	var start struct {
		Run *mobflow.Run `json:"run"`
	}
	if err := c.client().do(ctx, "POST", "/flows/runs", mobflow.StartRunRequest{
		Project: project, Case: name, DeviceID: devID, Mode: m, Heal: *heal, Params: pm, Agent: *agent,
	}, &start); err != nil {
		return err
	}
	if start.Run == nil {
		return errors.New("server answered without a run")
	}
	id := start.Run.ID
	if !c.g.json {
		extra := ""
		if start.Run.Heal {
			extra = ", heal"
		}
		fmt.Fprintf(c.out, "Run %s of %s on %s (%s%s)\n", id, start.Run.CaseID, devID, start.Run.Mode, extra)
	}

	run, ferr := c.followRun(ctx, id)
	interrupted := ""
	if ferr != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			interrupted = "timed out"
		case c.ctx.Err() != nil:
			interrupted = "interrupted"
		default:
			return ferr
		}
		// Stop the run on the server so the device is released.
		bg, bgCancel := context.WithTimeout(context.Background(), 10*time.Second)
		var cr *mobflow.Run
		if cerr := c.client().do(bg, "POST", "/flows/runs/"+url.PathEscape(id)+"/cancel", nil, &cr); cerr == nil && cr != nil {
			run = cr
		}
		bgCancel()
		if run == nil {
			return fmt.Errorf("run %s %s and could not be cancelled", id, interrupted)
		}
	}
	if *junit != "" {
		if err := c.writeJUnit(*junit, run); err != nil {
			return fmt.Errorf("junit: %w", err)
		}
	}
	if c.g.json {
		if err := c.printJSON(run); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(c.out, "Result: %s", run.Status)
		if len(run.Healed) > 0 {
			fmt.Fprintf(c.out, " (healed: %s)", joinIDs(run.Healed))
		}
		if interrupted != "" {
			fmt.Fprintf(c.out, " (%s)", interrupted)
		}
		if run.Error != "" {
			fmt.Fprintf(c.out, " - %s", run.Error)
		}
		fmt.Fprintln(c.out)
	}
	if run.Status != mobflow.RunPassed {
		return &exitError{flowExitInvalid, fmt.Errorf("run %s %s", id, run.Status)}
	}
	return nil
}

func joinIDs(ids []string) string {
	out := ""
	for i, s := range ids {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// followRun polls a run until it reaches a terminal status, printing one line
// per step as it finishes.
func (c *call) followRun(ctx context.Context, id string) (*mobflow.Run, error) {
	printed := map[string]string{}
	for {
		var run mobflow.Run
		if err := c.client().do(ctx, "GET", "/flows/runs/"+url.PathEscape(id), nil, &run); err != nil {
			return nil, err
		}
		if !c.g.json {
			for _, st := range run.Steps {
				if printed[st.ID] == st.State {
					continue
				}
				if line := stepLine(st); line != "" {
					fmt.Fprintln(c.out, line)
					printed[st.ID] = st.State
				}
			}
		}
		if run.Status != mobflow.RunRunning {
			return &run, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(flowPollInterval):
		}
	}
}

func stepLine(st mobflow.StepRun) string {
	dur := fmt.Sprintf("%.1fs", float64(st.DurationMs)/1000)
	title := st.Title
	if title == "" {
		title = st.Type
	}
	switch st.State {
	case mobflow.StepPassed:
		return fmt.Sprintf("  PASS   %s %s (%s)", st.ID, title, dur)
	case mobflow.StepHealed:
		return fmt.Sprintf("  HEALED %s %s (%s)", st.ID, title, dur)
	case mobflow.StepFailed:
		l := fmt.Sprintf("  FAIL   %s %s (%s)", st.ID, title, dur)
		if st.Error != "" {
			l += ": " + st.Error
		}
		return l
	case mobflow.StepSkipped:
		return fmt.Sprintf("  SKIP   %s %s", st.ID, title)
	case mobflow.StepHealing:
		return fmt.Sprintf("  HEAL   %s %s: replay failed, asking the agent to repair it", st.ID, title)
	}
	return ""
}

func flowCache(c *call, args []string) error {
	fs := newFlagSet("flow cache")
	platform := fs.String("platform", "", "")
	pos, err := c.parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 3 {
		return usagef("usage: pocketlab flow cache <project/name> [show|history|restore <entry>|delete] [--platform android|ios]")
	}
	project, name, ok := mobflow.SplitCaseID(pos[0])
	if !ok {
		return usagef("%q is not a case id <project>/<name>", pos[0])
	}
	action := "show"
	if len(pos) > 1 {
		action = pos[1]
	}
	base := caseURL(project, name) + "/cache"
	q := ""
	if *platform != "" {
		q = "?platform=" + url.QueryEscape(*platform)
	}
	ctx, cancel := c.ctxFor(30 * time.Second)
	defer cancel()
	switch action {
	case "show":
		if len(pos) > 2 {
			return usagef("flow cache show takes no argument")
		}
		b, _, err := c.client().raw(ctx, "GET", base+q, nil)
		if err != nil {
			return err
		}
		if c.g.json {
			fmt.Fprintln(c.out, string(b))
			return nil
		}
		return printCacheView(c, pos[0], b)
	case "history":
		b, _, err := c.client().raw(ctx, "GET", base+"/history"+q, nil)
		if err != nil {
			return err
		}
		if c.g.json {
			fmt.Fprintln(c.out, string(b))
			return nil
		}
		var h []mobflow.HistoryEntry
		if err := json.Unmarshal(b, &h); err != nil {
			return fmt.Errorf("bad history from server: %w", err)
		}
		if len(h) == 0 {
			fmt.Fprintln(c.out, "no history")
			return nil
		}
		rows := make([][]string, 0, len(h))
		for _, e := range h {
			rows = append(rows, []string{e.ID, "v" + strconv.Itoa(e.Version), e.ArchivedAt.Local().Format("2006-01-02 15:04:05")})
		}
		table(c.out, []string{"ENTRY", "VERSION", "ARCHIVED"}, rows)
		return nil
	case "restore":
		if len(pos) != 3 {
			return usagef("usage: pocketlab flow cache <project/name> restore <entry>")
		}
		b, _, err := c.client().raw(ctx, "POST", base+"/history/"+url.PathEscape(pos[2])+"/restore"+q, nil)
		if err != nil {
			return err
		}
		if c.g.json {
			fmt.Fprintln(c.out, string(b))
			return nil
		}
		return printCacheView(c, pos[0], b)
	case "delete":
		if len(pos) != 2 {
			return usagef("usage: pocketlab flow cache <project/name> delete")
		}
		if _, _, err := c.client().raw(ctx, "DELETE", base+q, nil); err != nil {
			return err
		}
		if c.g.json {
			fmt.Fprintln(c.out, `{"ok":true}`)
		} else {
			fmt.Fprintf(c.out, "%s: cache deleted (archived, restorable)\n", pos[0])
		}
		return nil
	}
	return usagef("unknown cache action %q (show, history, restore, delete)", action)
}

func printCacheView(c *call, id string, b []byte) error {
	var v struct {
		Cache     *mobflow.Cache    `json:"cache"`
		Staleness map[string]string `json:"staleness"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("bad cache from server: %w", err)
	}
	if v.Cache == nil {
		fmt.Fprintf(c.out, "%s: no cache\n", id)
		return nil
	}
	fmt.Fprintf(c.out, "%s: cache v%d, updated %s\n", id, v.Cache.Version, v.Cache.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
	segs := map[string]mobflow.Segment{}
	for _, sg := range v.Cache.Segments {
		segs[sg.ID] = sg
	}
	ids := make([]string, 0, len(v.Staleness))
	for k := range v.Staleness {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	rows := make([][]string, 0, len(ids))
	for _, sid := range ids {
		sg, has := segs[sid]
		n, healed := "-", ""
		if has {
			n = strconv.Itoa(len(sg.Steps))
			if sg.HealedAt != nil {
				healed = sg.HealedAt.Local().Format("2006-01-02 15:04")
			}
		}
		rows = append(rows, []string{sid, v.Staleness[sid], n, dash(healed)})
	}
	table(c.out, []string{"STEP", "STATE", "DSL STEPS", "HEALED"}, rows)
	return nil
}
