package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// slimSpecBody adds the slim-related request fields shared by create, clone and farm.
func slimSpecBody(f *simFlags, body map[string]any) {
	if *f.slim {
		body["slim"] = true
	}
	if *f.profile != "" {
		body["profile"] = *f.profile
	}
	if *f.groups != "" {
		body["groups"] = splitGroups(*f.groups)
	}
	if *f.app != "" {
		body["app"] = absApp(*f.app)
	}
	if *f.install {
		body["install"] = true
	}
}

type simResult struct {
	Simulator simRow `json:"simulator"`
	Slim      *struct {
		Profile string   `json:"profile"`
		Applied []string `json:"applied"`
	} `json:"slim"`
	Installed bool   `json:"installed"`
	Error     string `json:"error"`
}

func printSimResult(c *call, r simResult) {
	if r.Error != "" {
		fmt.Fprintf(c.out, "%s (%s): FAILED: %s\n", r.Simulator.Name, r.Simulator.UDID, r.Error)
		return
	}
	extra := ""
	if r.Slim != nil {
		extra += fmt.Sprintf(", slimmed (%s, %d labels)", r.Slim.Profile, len(r.Slim.Applied))
	}
	if r.Installed {
		extra += ", app installed"
	}
	fmt.Fprintf(c.out, "%s (%s): %s%s\n", r.Simulator.Name, r.Simulator.UDID, r.Simulator.State, extra)
}

func simCreate(ctx context.Context, c *call, f *simFlags) error {
	if *f.typ == "" {
		return usagef("sim create: --type is required (e.g. --type iPhone-17); list them with: curl %s/api/v1/simulators/catalog", c.client().base)
	}
	body := map[string]any{"name": *f.name, "deviceType": *f.typ, "runtime": *f.runtime}
	if *f.boot {
		body["boot"] = true
	}
	slimSpecBody(f, body)
	var raw json.RawMessage
	if err := c.client().do(ctx, "POST", "/simulators", body, &raw); err != nil {
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	var r simResult
	_ = json.Unmarshal(raw, &r)
	printSimResult(c, r)
	return nil
}

func simClone(ctx context.Context, c *call, f *simFlags, s simRow) error {
	body := map[string]any{"name": *f.name, "count": *f.count}
	if *f.boot {
		body["boot"] = true
	}
	if *f.shutdown {
		body["shutdown"] = true
	}
	slimSpecBody(f, body)
	var raw json.RawMessage
	if err := c.client().do(ctx, "POST", "/simulators/"+url.PathEscape(s.UDID)+"/clone", body, &raw); err != nil {
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	var r struct {
		Results []simResult `json:"results"`
	}
	_ = json.Unmarshal(raw, &r)
	for _, x := range r.Results {
		printSimResult(c, x)
	}
	return nil
}

func simEraseDelete(ctx context.Context, c *call, f *simFlags, s simRow, verb string) error {
	if !*f.force {
		what := "erase all content and settings of"
		if verb == "delete" {
			what = "permanently delete"
		}
		return usagef("sim %s would %s %q (%s) and cannot be undone: pass --force to confirm", verb, what, s.Name, s.UDID)
	}
	var raw json.RawMessage
	var err error
	if verb == "erase" {
		err = c.client().do(ctx, "POST", "/simulators/"+url.PathEscape(s.UDID)+"/erase", map[string]any{"keepSlim": *f.keepSlim, "shutdown": true}, &raw)
	} else {
		err = c.client().do(ctx, "DELETE", "/simulators/"+url.PathEscape(s.UDID)+"?shutdown=1", nil, &raw)
	}
	if err != nil {
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	past := "erased"
	if verb == "delete" {
		past = "deleted"
	}
	fmt.Fprintf(c.out, "%s (%s): %s\n", s.Name, s.UDID, past)
	return nil
}

type farmSimRow struct {
	UDID      string  `json:"udid"`
	Name      string  `json:"name"`
	Booted    bool    `json:"booted"`
	Slimmed   bool    `json:"slimmed"`
	Profile   string  `json:"profile"`
	Installed bool    `json:"installed"`
	MemoryMB  float64 `json:"memoryMB"`
	Error     string  `json:"error"`
}

// simFarm: `sim farm --from X --count N ...` and `sim farm --cleanup`.
// Exit codes: 0 all clones ok, 2 partial, 1 failure.
func simFarm(ctx context.Context, c *call, f *simFlags) error {
	if *f.cleanup {
		var raw json.RawMessage
		if err := c.client().do(ctx, "DELETE", "/simulators/farm", nil, &raw); err != nil {
			return err
		}
		if c.g.json {
			return c.printRaw(raw)
		}
		var r struct {
			Deleted []string          `json:"deleted"`
			Failed  map[string]string `json:"failed"`
		}
		_ = json.Unmarshal(raw, &r)
		fmt.Fprintf(c.out, "farm cleanup: %d simulators deleted, %d failed\n", len(r.Deleted), len(r.Failed))
		for u, e := range r.Failed {
			fmt.Fprintf(c.out, "  %s: %s\n", u, e)
		}
		if len(r.Failed) > 0 {
			return &exitError{2, fmt.Errorf("farm cleanup incomplete")}
		}
		return nil
	}
	if *f.from == "" || *f.count < 1 {
		return usagef("sim farm: --from <udid|name> and --count N (>=1) are required")
	}
	sims, _, err := listSims(ctx, c)
	if err != nil {
		return err
	}
	base, err := pickSim(sims, *f.from)
	if err != nil {
		return err
	}
	body := map[string]any{"from": base.UDID, "count": *f.count}
	if *f.shutdown {
		body["shutdownBase"] = true
	}
	if *f.strict {
		body["strict"] = true
	}
	slimSpecBody(f, body)
	var raw json.RawMessage
	if err := c.client().do(ctx, "POST", "/simulators/farm", body, &raw); err != nil {
		return err
	}
	var r struct {
		Status     string       `json:"status"`
		Requested  int          `json:"requested"`
		Succeeded  int          `json:"succeeded"`
		RolledBack bool         `json:"rolledBack"`
		Sims       []farmSimRow `json:"sims"`
	}
	_ = json.Unmarshal(raw, &r)
	if c.g.json {
		if err := c.printRaw(raw); err != nil {
			return err
		}
	} else {
		rows := make([][]string, 0, len(r.Sims))
		for _, s := range r.Sims {
			state := "ok"
			if s.Error != "" {
				state = "FAILED: " + s.Error
			}
			prof := ""
			if s.Slimmed {
				prof = s.Profile
			}
			rows = append(rows, []string{dash(s.UDID), s.Name, fmt.Sprint(s.Booted), dash(prof), state})
		}
		table(c.out, []string{"UDID", "NAME", "BOOTED", "SLIM", "RESULT"}, rows)
		fmt.Fprintf(c.out, "\nfarm %s: %d of %d ready", r.Status, r.Succeeded, r.Requested)
		if r.RolledBack {
			fmt.Fprint(c.out, " (strict: rolled back, clones deleted)")
		}
		fmt.Fprintln(c.out)
		var ids []string
		for _, s := range r.Sims {
			if s.Error == "" && s.UDID != "" {
				ids = append(ids, s.UDID)
			}
		}
		if len(ids) > 0 && !r.RolledBack {
			fmt.Fprintln(c.out, strings.Join(ids, " "))
			fmt.Fprintln(c.out, "clean up with: pocketlab sim farm --cleanup")
		}
	}
	switch r.Status {
	case "ok":
		return nil
	case "partial":
		return &exitError{2, fmt.Errorf("farm: only %d of %d simulators are ready", r.Succeeded, r.Requested)}
	}
	return &exitError{ExitError, fmt.Errorf("farm: no simulator is ready")}
}
