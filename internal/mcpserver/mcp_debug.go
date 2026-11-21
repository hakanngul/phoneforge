package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// maxCrashText bounds crash text returned inline by get_crash_reports.
const maxCrashText = 12000

// registerCrashAndDebugTools adds get_crash_reports and the debug_* tools.
func registerCrashAndDebugTools(s *server.MCPServer, c *client, id mcp.ToolOption) {
	// Attach can take up to ~90s (CoreDevice + lldb), so use a longer client.
	slow := &client{base: c.base, http: c.http}
	slowHTTP := *c.http
	slowHTTP.Timeout = 3 * time.Minute
	slow.http = &slowHTTP

	sess := mcp.WithString("session", mcp.Required(), mcp.Description("Debug session id from debug_attach"))
	sessPath := func(req mcp.CallToolRequest, suffix string) string {
		return "/debug/sessions/" + url.PathEscape(req.GetString("session", "")) + suffix
	}

	s.AddTool(mcp.NewTool("get_crash_reports",
		mcp.WithDescription("List crash reports from a device (iOS .ips/.crash, Android logcat crash buffer + DropBox). Newest first. Pass name to read one report's full text, or include_text to inline the text (truncated) of the listed reports."),
		id,
		mcp.WithString("app", mcp.Description("Only reports whose bundle id / package / process contains this text")),
		mcp.WithNumber("limit", mcp.Description("Maximum number of reports (default 10)")),
		mcp.WithString("since", mcp.Description("Only reports at or after this RFC 3339 timestamp")),
		mcp.WithString("name", mcp.Description("Fetch the full text of this report name (from a previous listing)")),
		mcp.WithBoolean("include_text", mcp.Description("Include each report's text (truncated) in the listing"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			base := devPath(req, "/crashes")
			if name := req.GetString("name", ""); name != "" {
				return c.text(ctx, "GET", base+"/"+url.PathEscape(name), "")
			}
			q := url.Values{}
			if v := req.GetString("app", ""); v != "" {
				q.Set("app", v)
			}
			if v := req.GetString("since", ""); v != "" {
				q.Set("since", v)
			}
			limit := req.GetInt("limit", 10)
			if limit <= 0 {
				limit = 10
			}
			q.Set("limit", strconv.Itoa(limit))
			body, code, err := c.do(ctx, "GET", base+"?"+q.Encode(), "", "")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if code < 200 || code > 299 {
				return mcp.NewToolResultError(fmt.Sprintf("HTTP %d: %s", code, body)), nil
			}
			if !req.GetBool("include_text", false) {
				return mcp.NewToolResultText(string(body)), nil
			}
			var reports []map[string]any
			if json.Unmarshal(body, &reports) != nil {
				return mcp.NewToolResultText(string(body)), nil
			}
			for _, r := range reports {
				name, _ := r["name"].(string)
				txt, tcode, terr := c.do(ctx, "GET", base+"/"+url.PathEscape(name), "", "")
				if terr != nil || tcode < 200 || tcode > 299 {
					r["textError"] = fmt.Sprintf("could not read report (HTTP %d)", tcode)
					continue
				}
				t := string(txt)
				if len(t) > maxCrashText {
					t = t[:maxCrashText] + "\n...[truncated]"
				}
				r["text"] = t
			}
			out, _ := json.Marshal(reports)
			return mcp.NewToolResultText(string(out)), nil
		})

	s.AddTool(mcp.NewTool("debug_attach",
		mcp.WithDescription("Start an lldb debug session on an iOS device or simulator: attach to a running app, or launch it suspended under the debugger (launch=true). The process is left stopped; set breakpoints, then debug_step continue. Real devices only accept development-signed debuggable apps. Android is not supported yet."),
		id,
		mcp.WithString("bundle_id", mcp.Description("App bundle id (attach to the running app, or launch it)")),
		mcp.WithNumber("pid", mcp.Description("Attach to this process id instead of a bundle id")),
		mcp.WithString("process_name", mcp.Description("Attach by process name")),
		mcp.WithBoolean("launch", mcp.Description("Launch the app suspended and attach (needs bundle_id)")),
		mcp.WithArray("args", mcp.WithStringItems(), mcp.Description("Launch arguments")),
		mcp.WithObject("env", mcp.Description("Launch environment variables (string map)"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			body := map[string]any{"deviceId": req.GetString("id", ""), "launch": req.GetBool("launch", false)}
			if v := req.GetString("bundle_id", ""); v != "" {
				body["bundleId"] = v
			}
			if v := req.GetInt("pid", 0); v > 0 {
				body["pid"] = v
			}
			if v := req.GetString("process_name", ""); v != "" {
				body["processName"] = v
			}
			if a := req.GetStringSlice("args", nil); len(a) > 0 {
				body["args"] = a
			}
			if e, ok := req.GetArguments()["env"].(map[string]any); ok {
				body["env"] = e
			}
			b, _ := json.Marshal(body)
			return slow.text(ctx, "POST", "/debug/sessions", string(b))
		})

	s.AddTool(mcp.NewTool("debug_breakpoint",
		mcp.WithDescription("Manage breakpoints in a debug session: action=set (file+line or symbol, optional condition), remove (breakpoint_id) or list."),
		sess,
		mcp.WithString("action", mcp.Required(), mcp.Description("set | remove | list")),
		mcp.WithString("file", mcp.Description("Source file for set (with line)")),
		mcp.WithNumber("line", mcp.Description("Line number for set")),
		mcp.WithString("symbol", mcp.Description("Function/symbol name for set")),
		mcp.WithString("condition", mcp.Description("Optional lldb condition expression")),
		mcp.WithNumber("breakpoint_id", mcp.Description("Breakpoint id for remove"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			switch req.GetString("action", "") {
			case "set":
				body := map[string]any{}
				if v := req.GetString("file", ""); v != "" {
					body["file"] = v
				}
				if v := req.GetInt("line", 0); v > 0 {
					body["line"] = v
				}
				if v := req.GetString("symbol", ""); v != "" {
					body["symbol"] = v
				}
				if v := req.GetString("condition", ""); v != "" {
					body["condition"] = v
				}
				b, _ := json.Marshal(body)
				return c.text(ctx, "POST", sessPath(req, "/breakpoints"), string(b))
			case "remove":
				return c.text(ctx, "DELETE", sessPath(req, "/breakpoints/"+strconv.Itoa(req.GetInt("breakpoint_id", 0))), "")
			case "list":
				return c.text(ctx, "GET", sessPath(req, "/breakpoints"), "")
			}
			return mcp.NewToolResultError("action must be set, remove or list"), nil
		})

	s.AddTool(mcp.NewTool("debug_step",
		mcp.WithDescription("Resume execution: action=continue, over, into, out or interrupt. Steps wait for the next stop (wait_ms, default 8000); continue returns immediately unless wait_ms is set (then waits for a breakpoint hit)."),
		sess,
		mcp.WithString("action", mcp.Required(), mcp.Description("continue | over | into | out | interrupt")),
		mcp.WithNumber("wait_ms", mcp.Description("How long to wait for the process to stop again"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			paths := map[string]string{"continue": "/continue", "over": "/step-over", "into": "/step-into", "out": "/step-out", "interrupt": "/interrupt"}
			p, ok := paths[strings.ToLower(req.GetString("action", ""))]
			if !ok {
				return mcp.NewToolResultError("action must be continue, over, into, out or interrupt"), nil
			}
			return slow.text(ctx, "POST", sessPath(req, p), jsonBody("waitMs", req.GetInt("wait_ms", 0)))
		})

	s.AddTool(mcp.NewTool("debug_eval",
		mcp.WithDescription("Evaluate an expression in the stopped process (lldb `expression`), optionally in a given frame."),
		sess,
		mcp.WithString("expression", mcp.Required(), mcp.Description("Expression, e.g. `counter` or `[obj description]`")),
		mcp.WithNumber("frame", mcp.Description("Frame index (default: selected frame)"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			body := map[string]any{"expression": req.GetString("expression", "")}
			if _, ok := req.GetArguments()["frame"]; ok {
				body["frame"] = req.GetInt("frame", 0)
			}
			b, _ := json.Marshal(body)
			return c.text(ctx, "POST", sessPath(req, "/eval"), string(b))
		})

	s.AddTool(mcp.NewTool("debug_state",
		mcp.WithDescription("Debug session state: process state, stop reason and top frame; optionally the backtrace and local variables of a frame."),
		sess,
		mcp.WithBoolean("backtrace", mcp.Description("Include the backtrace")),
		mcp.WithBoolean("variables", mcp.Description("Include local variables")),
		mcp.WithNumber("frame", mcp.Description("Frame index for variables (default: selected frame)"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			b, code, err := c.do(ctx, "GET", sessPath(req, "/state"), "", "")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if code < 200 || code > 299 {
				return mcp.NewToolResultError(fmt.Sprintf("HTTP %d: %s", code, b)), nil
			}
			var out map[string]json.RawMessage
			if json.Unmarshal(b, &out) != nil {
				return mcp.NewToolResultText(string(b)), nil
			}
			add := func(key, path string) {
				rb, rc, rerr := c.do(ctx, "GET", path, "", "")
				switch {
				case rerr != nil:
					out[key+"Error"], _ = json.Marshal(rerr.Error())
				case rc < 200 || rc > 299:
					out[key+"Error"], _ = json.Marshal(string(rb))
				default:
					out[key] = rb
				}
			}
			if req.GetBool("backtrace", false) {
				add("backtrace", sessPath(req, "/backtrace"))
			}
			if req.GetBool("variables", false) {
				q := ""
				if _, ok := req.GetArguments()["frame"]; ok {
					q = "?frame=" + strconv.Itoa(req.GetInt("frame", 0))
				}
				add("variables", sessPath(req, "/variables"+q))
			}
			res, _ := json.Marshal(out)
			return mcp.NewToolResultText(string(res)), nil
		})

	s.AddTool(mcp.NewTool("debug_detach",
		mcp.WithDescription("Detach from the process (it keeps running) and end the debug session."),
		sess),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "DELETE", sessPath(req, ""), "")
		})
}
