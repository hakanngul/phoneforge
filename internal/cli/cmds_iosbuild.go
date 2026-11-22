package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// wdaBuildStatus mirrors the server's build status (the fields the CLI uses).
type wdaBuildStatus struct {
	State     string `json:"state"`
	Phase     string `json:"phase"`
	Mode      string `json:"mode"`
	Team      string `json:"team"`
	RunnerApp string `json:"runnerApp"`
	XCTestRun string `json:"xctestrun"`
	LogFile   string `json:"logFile"`
	Issue     *struct {
		Code    string   `json:"code"`
		Message string   `json:"message"`
		Hint    string   `json:"hint"`
		Lines   []string `json:"lines"`
	} `json:"issue"`
}

func cmdIOS(c *call) error {
	fs := newFlagSet(c.name)
	sim := fs.Bool("sim", false, "build the simulator runner")
	team := fs.String("team", "", "Apple Team ID")
	install := fs.String("install", "", "install on this iPhone after building")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "build-wda" {
		return usagef("usage: phoneforge ios build-wda [--sim] [--install UDID] [--team TEAMID]")
	}
	if *sim && *install != "" {
		return usagef("--install needs a device build; simulators need no install (drop --sim or --install)")
	}
	ctx, cancel := c.ctxFor(45 * time.Minute)
	defer cancel()
	cl := c.client()

	body := map[string]any{"mode": "device"}
	if *sim {
		body["mode"] = "sim"
	}
	if *team != "" {
		body["teamId"] = *team
	}
	var raw json.RawMessage
	if err := cl.do(ctx, "POST", "/ios/wda/build", body, &raw); err != nil {
		return err
	}
	var st wdaBuildStatus
	_ = json.Unmarshal(raw, &st)
	if !c.g.json {
		fmt.Fprintf(c.errw, "Building WebDriverAgent (%s)...\n", st.Mode)
	}

	// Follow the live stream; finished state arrives as a "status" event.
	var last wdaBuildStatus
	err = cl.sse(ctx, "GET", "/ios/wda/build/stream", nil, func(event, data string) bool {
		switch event {
		case "log":
			var l string
			if json.Unmarshal([]byte(data), &l) == nil && !c.g.json {
				fmt.Fprintln(c.errw, l)
			}
		case "status":
			_ = json.Unmarshal([]byte(data), &last)
		}
		return true
	})
	if err != nil {
		return err
	}
	// The stream ends when the build does; read the final state.
	if err := cl.do(ctx, "GET", "/ios/wda/build?since=0", nil, &raw); err != nil {
		return err
	}
	_ = json.Unmarshal(raw, &last)
	if last.State != "succeeded" {
		if last.Issue != nil {
			msg := last.Issue.Message
			if len(last.Issue.Lines) > 0 {
				msg += "\n  " + strings.Join(last.Issue.Lines, "\n  ")
			}
			if last.Issue.Hint != "" {
				msg += "\n" + last.Issue.Hint
			}
			return fmt.Errorf("%s: %s", last.Issue.Code, msg)
		}
		return fmt.Errorf("build %s", firstNonEmpty(last.State, "did not finish"))
	}
	out := map[string]any{"state": last.State, "mode": last.Mode, "team": last.Team, "runnerApp": last.RunnerApp, "xctestrun": last.XCTestRun}
	if *install != "" {
		d, err := resolveDevice(ctx, cl, *install)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.errw, "Installing the runner on %s...\n", d.Name)
		var ir json.RawMessage
		if err := cl.do(ctx, "POST", devPath(d.ID, "/iosbridge/install"), nil, &ir); err != nil {
			return err
		}
		out["installedOn"] = d.ID
	}
	if c.g.json {
		return c.printJSON(out)
	}
	if last.Mode == "sim" {
		fmt.Fprintf(c.out, "WebDriverAgent built for the simulator: %s\n", last.XCTestRun)
	} else {
		fmt.Fprintf(c.out, "WebDriverAgent built: %s\n", last.RunnerApp)
		if *install != "" {
			fmt.Fprintln(c.out, "Installed. If starting WDA fails with an untrusted developer, trust your profile on the phone:")
			fmt.Fprintln(c.out, "Settings > General > VPN & Device Management.")
		} else {
			fmt.Fprintln(c.out, "Install it with: phoneforge ios build-wda --install <device>, or use Install helper in the app.")
		}
	}
	return nil
}
