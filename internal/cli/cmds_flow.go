package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"pocketlab/internal/mobflow"
)

// Exit codes of the flow commands: 0 ok, 1 validation failure, 2 any other error.
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
	}
	return &exitError{flowExitError, fmt.Errorf("flow: unknown subcommand %q (list, validate)", sub)}
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
	} else {
		return fmt.Errorf("%q is neither an existing file nor a case id <project>/<name>", pos[0])
	}
	if c.g.json {
		if errs == nil {
			errs = mobflow.ValidationErrors{}
		}
		b, _ := json.Marshal(struct {
			OK     bool                     `json:"ok"`
			Errors mobflow.ValidationErrors `json:"errors"`
		}{len(errs) == 0, errs})
		fmt.Fprintln(c.out, string(b))
	} else if len(errs) == 0 {
		fmt.Fprintf(c.out, "%s: ok\n", pos[0])
	} else {
		for _, e := range errs {
			fmt.Fprintf(c.errw, "%s: %s\n", pos[0], e)
		}
	}
	if len(errs) > 0 {
		return &exitError{flowExitInvalid, fmt.Errorf("%d validation error(s)", len(errs))}
	}
	return nil
}
