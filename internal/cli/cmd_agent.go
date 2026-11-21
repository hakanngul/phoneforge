package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// agentRenderer turns streamed run events into compact terminal lines.
type agentRenderer struct {
	w        io.Writer
	gotFinal bool
}

func (r *agentRenderer) printf(format string, a ...any) { fmt.Fprintf(r.w, format, a...) }

// stdoutLine renders one stdout line from an agent CLI. Claude emits
// stream-json; anything else is shown as plain text.
func (r *agentRenderer) stdoutLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var m struct {
		Type    string `json:"type"`
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &m) != nil || m.Type == "" {
		r.printf("%s\n", line)
		return
	}
	switch m.Type {
	case "assistant":
		var blocks []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}
		if json.Unmarshal(m.Message.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					r.printf("%s\n", t)
				}
			case "tool_use":
				r.printf("▸ %s\n", formatToolCall(b.Name, b.Input))
			}
		}
	case "user":
		var blocks []struct {
			Type    string          `json:"type"`
			IsError bool            `json:"is_error"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(m.Message.Content, &blocks) != nil {
			return
		}
		for _, b := range blocks {
			if b.Type == "tool_result" && b.IsError {
				r.printf("  ! %s\n", truncate(contentText(b.Content), 160))
			}
		}
	case "result":
		r.gotFinal = true
		if m.IsError {
			r.printf("✗ %s\n", truncate(m.Result, 400))
		} else {
			r.printf("✓ done: %s\n", strings.TrimSpace(m.Result))
		}
	}
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var out []string
		for _, p := range parts {
			out = append(out, p.Text)
		}
		return strings.Join(out, " ")
	}
	return string(raw)
}

var toolPrefix = func(name string) string {
	if strings.HasPrefix(name, "mcp__") {
		if i := strings.Index(name[5:], "__"); i >= 0 {
			return name[5+i+2:]
		}
	}
	return name
}

func formatToolCall(name string, in map[string]any) string {
	keys := sortedKeys(in)
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		if k == "deviceId" || k == "device_id" || k == "id" && len(in) > 1 {
			continue
		}
		switch v := in[k].(type) {
		case string:
			parts = append(parts, fmt.Sprintf("%s=%q", k, truncate(v, 40)))
		default:
			b, _ := json.Marshal(v)
			parts = append(parts, fmt.Sprintf("%s=%s", k, truncate(string(b), 40)))
		}
	}
	return truncate(toolPrefix(name)+"("+strings.Join(parts, " ")+")", 140)
}

// step renders one built-in agent Step frame.
func (r *agentRenderer) step(data string) (finished bool, failed bool) {
	var s struct {
		Type   string         `json:"type"`
		Text   string         `json:"text"`
		Tool   string         `json:"tool"`
		Args   map[string]any `json:"args"`
		Result string         `json:"result"`
	}
	if json.Unmarshal([]byte(data), &s) != nil {
		return false, false
	}
	switch s.Type {
	case "thought":
		r.printf("%s\n", strings.TrimSpace(s.Text))
	case "action":
		r.printf("▸ %s\n", formatToolCall(s.Tool, s.Args))
	case "observation":
		if s.Result != "" {
			r.printf("  ← %s\n", truncate(s.Result, 120))
		}
	case "done":
		r.printf("✓ done: %s\n", strings.TrimSpace(s.Text))
		return true, false
	case "error":
		r.printf("✗ %s\n", strings.TrimSpace(s.Text))
		return true, true
	}
	return false, false
}

func cmdAgent(c *call) error {
	fs := newFlagSet(c.name)
	model := fs.String("model", "", "")
	agentName := fs.String("agent", "", "")
	maxSteps := fs.Int("max-steps", 0, "")
	session := fs.String("session", "", "")
	pos, err := c.parse(fs, c.args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usagef("usage: phoneforge agent \"prompt\" [--model haiku] [--agent claude]")
	}
	prompt := strings.Join(pos, " ")
	ctx, cancel := c.ctxFor(0)
	defer cancel()
	id, err := c.target(ctx)
	if err != nil {
		return err
	}
	cl := c.client()

	restore, err := applyAgentModel(ctx, c, *model)
	if err != nil {
		return err
	}
	var start struct {
		ID      string `json:"id"`
		Builtin bool   `json:"builtin"`
	}
	body := map[string]any{"agent": *agentName, "type": "automation", "deviceId": id, "prompt": prompt, "sessionId": *session}
	if *model != "" {
		body["model"] = *model
	}
	err = cl.do(ctx, "POST", "/ai/runs", body, &start)
	restore() // the server has already built the command line; put settings back
	if err != nil {
		return err
	}

	rd := &agentRenderer{w: c.out}
	if start.Builtin || start.ID == "" {
		return runBuiltinAgent(c, ctx, rd, id, prompt, *maxSteps)
	}
	if !c.g.json {
		fmt.Fprintf(c.errw, "agent run %s started on %s (Ctrl-C cancels)\n", start.ID, id)
	}
	result := ""
	errMsg := ""
	err = cl.sse(ctx, "GET", "/ai/runs/"+start.ID+"/events", nil, func(_, data string) bool {
		var ev struct {
			Type string `json:"type"`
			Data string `json:"data"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return true
		}
		if c.g.json {
			fmt.Fprintln(c.out, data)
		}
		switch ev.Type {
		case "stdout":
			if !c.g.json {
				rd.stdoutLine(ev.Data)
			}
		case "stderr":
			if !c.g.json && strings.TrimSpace(ev.Data) != "" {
				fmt.Fprintf(c.errw, "  ! %s\n", truncate(ev.Data, 200))
			}
		case "error":
			errMsg = ev.Data
			if !c.g.json {
				fmt.Fprintf(c.errw, "✗ %s\n", ev.Data)
			}
		case "done":
			result = ev.Data
			return false
		}
		return true
	})
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_ = cl.do(cctx, "POST", "/ai/runs/"+start.ID+"/cancel", nil, nil)
		fmt.Fprintln(c.errw, "cancelled")
		return &exitError{ExitError, errors.New("agent run cancelled")}
	}
	if err != nil {
		return err
	}
	if result != "ok" {
		if errMsg == "" {
			errMsg = result
		}
		return fmt.Errorf("agent run %s: %s", firstNonEmpty(result, "ended"), errMsg)
	}
	return nil
}

func runBuiltinAgent(c *call, ctx context.Context, rd *agentRenderer, id, prompt string, maxSteps int) error {
	body := map[string]any{"task": prompt}
	if maxSteps > 0 {
		body["maxSteps"] = maxSteps
	}
	failed := false
	done := false
	err := c.client().sse(ctx, "POST", devPath(id, "/agent/run"), body, func(_, data string) bool {
		if c.g.json {
			fmt.Fprintln(c.out, data)
			var s struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal([]byte(data), &s)
			if s.Type == "done" || s.Type == "error" {
				done, failed = true, s.Type == "error"
				return false
			}
			return true
		}
		fin, bad := rd.step(data)
		if fin {
			done, failed = true, bad
			return false
		}
		return true
	})
	if ctx.Err() != nil {
		fmt.Fprintln(c.errw, "cancelled")
		return &exitError{ExitError, errors.New("agent run cancelled")}
	}
	if err != nil {
		return err
	}
	if failed || !done {
		return errors.New("agent run did not finish successfully")
	}
	return nil
}

// applyAgentModel temporarily sets the automation model in the server's AI
// settings (the run API has no model field) and returns a func that restores it.
func applyAgentModel(ctx context.Context, c *call, model string) (func(), error) {
	if model == "" {
		return func() {}, nil
	}
	cl := c.client()
	var orig map[string]any
	if err := cl.do(ctx, "GET", "/ai/settings", nil, &orig); err != nil {
		return nil, err
	}
	next := map[string]any{}
	for k, v := range orig {
		next[k] = v
	}
	auto, _ := orig["automation"].(map[string]any)
	na := map[string]any{}
	for k, v := range auto {
		na[k] = v
	}
	na["model"] = model
	next["automation"] = na
	if err := cl.do(ctx, "PUT", "/ai/settings", next, nil); err != nil {
		return nil, err
	}
	return func() {
		rctx, rc := context.WithTimeout(context.Background(), 5*time.Second)
		defer rc()
		if err := cl.do(rctx, "PUT", "/ai/settings", orig, nil); err != nil {
			fmt.Fprintf(c.errw, "warning: could not restore AI settings: %v\n", err)
		}
	}, nil
}
