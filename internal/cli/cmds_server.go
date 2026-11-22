package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"phoneforge/internal/lifecycle"
	"phoneforge/internal/procutil"
)

// serverStatus mirrors GET /server/status.
type serverStatus struct {
	Running    bool      `json:"running"`
	PID        int       `json:"pid"`
	Version    string    `json:"version"`
	Addr       string    `json:"addr"`
	StartedAt  time.Time `json:"startedAt"`
	UptimeSec  int64     `json:"uptimeSec"`
	StartedVia string    `json:"startedVia"`
	Owner      string    `json:"owner"`
	ParentPID  int       `json:"parentPid"`
	ParentCmd  string    `json:"parentCmd"`
	Home       string    `json:"home"`
	EventsFile string    `json:"eventsFile"`
}

func cmdServer(c *call) error {
	fs := newFlagSet(c.name)
	limit := fs.Int("limit", 20, "")
	bin := fs.String("bin", "", "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: phoneforge server status|history|start|stop")
	}
	switch pos[0] {
	case "stop":
		ctx, cancel := c.ctxFor(10 * time.Second)
		defer cancel()
		if err := c.client().do(ctx, "POST", "/server/shutdown", nil, nil); err != nil {
			return err
		}
		return c.actionResult("server stopping")
	case "status":
		return serverStatusCmd(c)
	case "history":
		return serverHistoryCmd(c, *limit)
	case "start":
		return serverStartCmd(c, *bin)
	}
	return usagef("usage: phoneforge server status|history|start|stop")
}

func isUnreachable(err error) bool {
	var ee *exitError
	return errors.As(err, &ee) && ee.code == ExitUnreachable
}

func serverStatusCmd(c *call) error {
	ctx, cancel := c.ctxFor(10 * time.Second)
	defer cancel()
	var raw json.RawMessage
	err := c.client().do(ctx, "GET", "/server/status", nil, &raw)
	var ae *apiError
	if errors.As(err, &ae) && (ae.Status == 404 || ae.Status == 405) {
		return legacyStatus(c, ctx) // older server without lifecycle tracking
	}
	if err != nil {
		if isUnreachable(err) {
			printOffline(c)
		}
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	var s serverStatus
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "server:      %s\nstatus:      running\npid:         %d\nversion:     %s\naddr:        %s\nstarted:     %s (up %s)\nstarted via: %s (owner %s)\n",
		c.client().base, s.PID, s.Version, s.Addr, s.StartedAt.Local().Format("2006-01-02 15:04:05"), fmtDur(s.UptimeSec), dash(s.StartedVia), dash(s.Owner))
	if s.ParentPID > 0 {
		fmt.Fprintf(c.out, "parent:      %d %s\n", s.ParentPID, s.ParentCmd)
	}
	fmt.Fprintf(c.out, "event log:   %s\n", s.EventsFile)
	return nil
}

func legacyStatus(c *call, ctx context.Context) error {
	var raw json.RawMessage
	if err := c.client().do(ctx, "GET", "/health", nil, &raw); err != nil {
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	var h struct {
		Status      string `json:"status"`
		Version     string `json:"version"`
		DeviceCount int    `json:"deviceCount"`
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "server:  %s\nstatus:  %s\nversion: %s\ndevices: %d\n", c.client().base, h.Status, h.Version, h.DeviceCount)
	return nil
}

// localHome is the state directory, honouring PHONEFORGE_HOME.
func localHome(c *call) string {
	if h := c.env.getenv("PHONEFORGE_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".phoneforge")
	}
	return ".phoneforge"
}

// printOffline tells what the local lifecycle log knows when the server is down.
func printOffline(c *call) {
	home := localHome(c)
	h := lifecycle.ReadHistory(home, 1)
	if c.g.json {
		return
	}
	if len(h) == 0 {
		fmt.Fprintf(c.out, "server is not running (no lifecycle log in %s)\n", home)
		return
	}
	e := h[0]
	fmt.Fprintf(c.out, "server is not running. Last recorded event (%s): %s\n  %s\n  log: %s\n",
		e.Time.Local().Format("2006-01-02 15:04:05"), e.Event, eventDetail(e), filepath.Join(home, lifecycle.EventsFile))
}

func serverHistoryCmd(c *call, limit int) error {
	if limit < 1 {
		return usagef("--limit must be positive")
	}
	ctx, cancel := c.ctxFor(10 * time.Second)
	defer cancel()
	var res struct {
		Events []lifecycle.Event `json:"events"`
	}
	var raw json.RawMessage
	err := c.client().do(ctx, "GET", fmt.Sprintf("/server/history?limit=%d", limit), nil, &raw)
	offline := false
	switch {
	case err == nil:
		_ = json.Unmarshal(raw, &res)
	case isUnreachable(err):
		offline = true
		res.Events = lifecycle.ReadHistory(localHome(c), limit)
		b, _ := json.Marshal(map[string]any{"events": res.Events})
		raw = b
	default:
		return err
	}
	if c.g.json {
		return c.printRaw(raw)
	}
	if offline {
		fmt.Fprintf(c.out, "(server is not running; read from %s)\n", filepath.Join(localHome(c), lifecycle.EventsFile))
	}
	if len(res.Events) == 0 {
		fmt.Fprintln(c.out, "no lifecycle events recorded yet")
		return nil
	}
	rows := make([][]string, 0, len(res.Events))
	for i := len(res.Events) - 1; i >= 0; i-- { // oldest first
		e := res.Events[i]
		mark := " "
		if e.Unexpected() {
			mark = "!"
		}
		rows = append(rows, []string{mark, e.Time.Local().Format("2006-01-02 15:04:05"), e.Event, fmt.Sprint(e.PID), dash(e.StartedVia), eventDetail(e)})
	}
	table(c.out, []string{"", "TIME", "EVENT", "PID", "VIA", "REASON"}, rows)
	return nil
}

func eventDetail(e lifecycle.Event) string {
	s := e.Reason
	if e.Origin != "" && !strings.Contains(s, e.Origin) {
		s += " [origin: " + e.Origin + "]"
	}
	if e.Event == lifecycle.KindStart && e.ParentCmd != "" {
		s += fmt.Sprintf(" [parent %d %s]", e.ParentPID, e.ParentCmd)
	}
	return s
}

func fmtDur(sec int64) string {
	d := time.Duration(sec) * time.Second
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", sec)
}

// serverStartCmd launches phoneforged detached and waits for it to answer /health.
func serverStartCmd(c *call, bin string) error {
	hctx, hcancel := c.ctxFor(3 * time.Second)
	err := c.client().do(hctx, "GET", "/health", nil, nil)
	hcancel()
	if err == nil {
		return c.actionResult("server already running at " + c.client().base)
	}
	if !isUnreachable(err) {
		return err
	}
	u, perr := url.Parse(c.client().base)
	if perr != nil {
		return usagef("bad --server URL: %v", perr)
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return usagef("can only start a local server (--server points at %s)", host)
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(host, "8700")
	}
	path, err := findServerBinary(bin)
	if err != nil {
		return err
	}
	home := localHome(c)
	_ = os.MkdirAll(home, 0o700)
	logPath := filepath.Join(home, "phoneforged.log")
	lf, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", logPath, err)
	}
	defer lf.Close()
	cmd := exec.Command(path, "-addr", addr)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.Env = append(os.Environ(), fmt.Sprintf("PHONEFORGE_STARTED_VIA=phoneforge server start (cli pid %d)", os.Getpid()))
	if tok := c.env.getenv("PHONEFORGE_TOKEN"); tok != "" {
		cmd.Args = append(cmd.Args, "-token", tok)
	}
	procutil.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", path, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case werr := <-exited:
			return fmt.Errorf("phoneforged exited right after start (%v); see %s", werr, logPath)
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		pctx, pcancel := context.WithTimeout(c.ctx, 2*time.Second)
		perr := c.client().do(pctx, "GET", "/health", nil, nil)
		pcancel()
		if perr == nil {
			if c.g.json {
				return c.printJSON(map[string]any{"ok": true, "pid": cmd.Process.Pid, "addr": addr, "log": logPath})
			}
			fmt.Fprintf(c.out, "server started (pid %d) on %s\nlog: %s\n", cmd.Process.Pid, addr, logPath)
			return nil
		}
	}
	return fmt.Errorf("phoneforged did not become healthy within 20s; see %s", logPath)
}

func findServerBinary(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	name := "phoneforged"
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("phoneforged not found next to this binary or in PATH; pass --bin PATH")
}
