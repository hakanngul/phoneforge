// Package mcpserver exposes PhoneForge's HTTP API as MCP tools.
package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"phoneforge/internal/device"
	"phoneforge/internal/uitree"
)

type client struct {
	base string
	http *http.Client
}

// do performs a request and returns the body and status.
func (c *client) do(ctx context.Context, method, path, contentType string, body string) ([]byte, int, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	return b, resp.StatusCode, err
}

func jsonBody(pairs ...any) string {
	var sb strings.Builder
	sb.WriteByte('{')
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			sb.WriteByte(',')
		}
		switch v := pairs[i+1].(type) {
		case string:
			fmt.Fprintf(&sb, "%q:%s", pairs[i], quote(v))
		default:
			fmt.Fprintf(&sb, "%q:%v", pairs[i], v)
		}
	}
	sb.WriteByte('}')
	return sb.String()
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (c *client) text(ctx context.Context, method, path, body string) (*mcp.CallToolResult, error) {
	b, code, err := c.do(ctx, method, path, "application/json", body)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if code < 200 || code > 299 {
		return mcp.NewToolResultError(fmt.Sprintf("HTTP %d: %s", code, b)), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

// uiTreeResult fetches the raw tree from the REST API and renders it as
// compact text (default) or size-limited JSON.
func uiTreeResult(ctx context.Context, c *client, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	format := strings.ToLower(req.GetString("format", "compact"))
	if format != "compact" && format != "json" {
		return mcp.NewToolResultError(`format must be "compact" or "json"`), nil
	}
	b, code, err := c.do(ctx, "GET", devPath(req, "/ui-tree"), "application/json", "")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if code < 200 || code > 299 {
		return mcp.NewToolResultError(fmt.Sprintf("HTTP %d: %s", code, b)), nil
	}
	if format == "json" {
		return mcp.NewToolResultText(uitree.Truncate(string(b), uitree.DefaultMaxChars,
			"Use format=compact (default) with `filter` instead.")), nil
	}
	var root device.Element
	if err := json.Unmarshal(b, &root); err != nil {
		return mcp.NewToolResultError("cannot parse ui-tree response: " + err.Error()), nil
	}
	return mcp.NewToolResultText(uitree.Format(root, uitree.Options{
		DeviceID:         req.GetString("id", ""),
		Filter:           req.GetString("filter", ""),
		MaxNodes:         req.GetInt("max_nodes", 0),
		IncludeOffscreen: req.GetBool("include_offscreen", false),
	})), nil
}

func devPath(req mcp.CallToolRequest, suffix string) string {
	return "/devices/" + url.PathEscape(req.GetString("id", "")) + suffix
}

// New builds an MCP server whose tools call the PhoneForge HTTP API at
// apiBaseURL (for example "http://127.0.0.1:8700/api/v1").
func New(apiBaseURL, version string) *server.MCPServer {
	c := &client{base: strings.TrimRight(apiBaseURL, "/"), http: &http.Client{Timeout: 60 * time.Second}}
	// Test runs can take minutes; give them their own client timeout.
	runner := &client{base: c.base, http: &http.Client{Timeout: 11 * time.Minute}}
	s := server.NewMCPServer("phoneforge", version, server.WithToolCapabilities(true))

	id := mcp.WithString("id", mcp.Required(), mcp.Description("Device id from list_devices"))
	num := func(n, d string) mcp.ToolOption { return mcp.WithNumber(n, mcp.Required(), mcp.Description(d)) }

	s.AddTool(mcp.NewTool("list_devices", mcp.WithDescription("List connected devices")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "GET", "/devices", "")
		})
	s.AddTool(mcp.NewTool("get_device", mcp.WithDescription("Get device info"), id),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "GET", devPath(req, ""), "")
		})
	s.AddTool(mcp.NewTool("list_apps", mcp.WithDescription("List installed apps on a device (bundle id / package name)"), id),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "GET", devPath(req, "/apps"), "")
		})
	s.AddTool(mcp.NewTool("get_screenshot", mcp.WithDescription("Capture a PNG screenshot"), id),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			b, code, err := c.do(ctx, "GET", devPath(req, "/screenshot"), "", "")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if code < 200 || code > 299 {
				return mcp.NewToolResultError(fmt.Sprintf("HTTP %d: %s", code, b)), nil
			}
			return mcp.NewToolResultImage("screenshot", base64.StdEncoding.EncodeToString(b), "image/png"), nil
		})
	s.AddTool(mcp.NewTool("ui_tree",
		mcp.WithDescription("Get the on-screen UI elements. Default format=compact returns one line per meaningful node, e.g. `[12] Button \"Settings\" id=com.apple.settings @(201,337) 370x53 clickable`, where @(x,y) is the tap center (points on iOS, pixels on Android) that you can pass straight to tap. PREFER compact: it is small enough to use on every screen. When looking for a specific element use `filter` (case-insensitive substring of text/label/id) to get only matches plus their clickable ancestor. format=json returns the full raw hierarchy (very large, truncated at ~20k chars) and should rarely be needed. Offscreen nodes are hidden unless include_offscreen=true."),
		id,
		mcp.WithString("format", mcp.Enum("compact", "json"), mcp.Description("compact (default, recommended) or json (raw, truncated)")),
		mcp.WithString("filter", mcp.Description("Only return nodes whose text/label/id contains this substring (case-insensitive), plus their nearest clickable ancestor")),
		mcp.WithNumber("max_nodes", mcp.Description("Maximum nodes to return in compact format (default 300)")),
		mcp.WithBoolean("include_offscreen", mcp.Description("Include nodes outside the visible screen (default false)"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return uiTreeResult(ctx, c, req)
		})
	s.AddTool(mcp.NewTool("tap", mcp.WithDescription("Tap at screen coordinates"), id, num("x", "X pixel"), num("y", "Y pixel")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/tap"), jsonBody("x", req.GetInt("x", 0), "y", req.GetInt("y", 0)))
		})
	s.AddTool(mcp.NewTool("swipe", mcp.WithDescription("Swipe between two points"), id,
		num("x1", "Start X"), num("y1", "Start Y"), num("x2", "End X"), num("y2", "End Y"),
		mcp.WithNumber("durationMs", mcp.Description("Duration in ms (default 300)"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/swipe"), jsonBody(
				"x1", req.GetInt("x1", 0), "y1", req.GetInt("y1", 0),
				"x2", req.GetInt("x2", 0), "y2", req.GetInt("y2", 0),
				"durationMs", req.GetInt("durationMs", 300)))
		})
	s.AddTool(mcp.NewTool("type_text", mcp.WithDescription("Type text into the focused field"), id,
		mcp.WithString("text", mcp.Required(), mcp.Description("Text to type"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/type"), jsonBody("text", req.GetString("text", "")))
		})
	s.AddTool(mcp.NewTool("press_key", mcp.WithDescription("Press a key: home, back, enter, delete"), id,
		mcp.WithString("key", mcp.Required(), mcp.Description("Key name"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/press-key"), jsonBody("key", req.GetString("key", "")))
		})
	bid := mcp.WithString("bundleId", mcp.Required(), mcp.Description("App bundle id / package name"))
	s.AddTool(mcp.NewTool("launch_app", mcp.WithDescription("Launch an app"), id, bid),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/launch-app"), jsonBody("bundleId", req.GetString("bundleId", "")))
		})
	s.AddTool(mcp.NewTool("stop_app", mcp.WithDescription("Stop an app"), id, bid),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/stop-app"), jsonBody("bundleId", req.GetString("bundleId", "")))
		})
	s.AddTool(mcp.NewTool("execute_dsl", mcp.WithDescription("Execute a PhoneForge DSL script (JSON) on a device"), id,
		mcp.WithString("script", mcp.Required(), mcp.Description("DSL script as a JSON string"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/dsl/execute"), req.GetString("script", ""))
		})
	s.AddTool(mcp.NewTool("double_tap", mcp.WithDescription("Double-tap at screen coordinates"), id, num("x", "X pixel"), num("y", "Y pixel")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/double-tap"), jsonBody("x", req.GetInt("x", 0), "y", req.GetInt("y", 0)))
		})
	s.AddTool(mcp.NewTool("long_press", mcp.WithDescription("Long-press at screen coordinates"), id, num("x", "X pixel"), num("y", "Y pixel"),
		mcp.WithNumber("ms", mcp.Description("Hold duration in ms (default 600)"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/long-press"), jsonBody("x", req.GetInt("x", 0), "y", req.GetInt("y", 0), "ms", req.GetInt("ms", 600)))
		})
	s.AddTool(mcp.NewTool("drag", mcp.WithDescription("Press at one point, drag to another and release"), id,
		num("x1", "Start X"), num("y1", "Start Y"), num("x2", "End X"), num("y2", "End Y"),
		mcp.WithNumber("durationMs", mcp.Description("Duration in ms (default 500)"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/drag"), jsonBody(
				"x1", req.GetInt("x1", 0), "y1", req.GetInt("y1", 0),
				"x2", req.GetInt("x2", 0), "y2", req.GetInt("y2", 0),
				"durationMs", req.GetInt("durationMs", 500)))
		})
	s.AddTool(mcp.NewTool("open_url", mcp.WithDescription("Open a URL or deep link on the device"), id,
		mcp.WithString("url", mcp.Required(), mcp.Description("http(s) URL or app scheme"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/open-url"), jsonBody("url", req.GetString("url", "")))
		})
	s.AddTool(mcp.NewTool("dismiss_keyboard", mcp.WithDescription("Hide the on-screen keyboard"), id),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/dismiss-keyboard"), "")
		})
	s.AddTool(mcp.NewTool("install_app", mcp.WithDescription("Install an app package (.apk) that exists on the PhoneForge server machine"), id,
		mcp.WithString("path", mcp.Required(), mcp.Description("Local file path on the server machine"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/install-app"), jsonBody("path", req.GetString("path", "")))
		})
	s.AddTool(mcp.NewTool("uninstall_app", mcp.WithDescription("Uninstall an app"), id, bid),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "DELETE", devPath(req, "/apps/"+url.PathEscape(req.GetString("bundleId", ""))), "")
		})
	s.AddTool(mcp.NewTool("set_permission", mcp.WithDescription("Grant or revoke a runtime permission for an app"), id, bid,
		mcp.WithString("permission", mcp.Required(), mcp.Description("Permission name, e.g. android.permission.CAMERA")),
		mcp.WithBoolean("grant", mcp.Required(), mcp.Description("true to grant, false to revoke"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/permissions"), jsonBody(
				"bundleId", req.GetString("bundleId", ""), "permission", req.GetString("permission", ""),
				"grant", req.GetBool("grant", false)))
		})
	s.AddTool(mcp.NewTool("get_clipboard", mcp.WithDescription("Read the device clipboard text"), id),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "GET", devPath(req, "/clipboard"), "")
		})
	s.AddTool(mcp.NewTool("set_clipboard", mcp.WithDescription("Set the device clipboard text"), id,
		mcp.WithString("text", mcp.Required(), mcp.Description("Clipboard text"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/clipboard"), jsonBody("text", req.GetString("text", "")))
		})

	// Live-control bridge: UiAutomator2 (Android) or WebDriverAgent (iOS).
	s.AddTool(mcp.NewTool("start_bridge", mcp.WithDescription("Start the live-control bridge: UiAutomator2 on Android (faster, richer UI tree and gestures), or WebDriverAgent on iOS (required for iOS screenshots/UI tree/input; the helper must be installed first, see ios_install_helper). Uses the packages already installed on the phone."), id),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/bridge/start"), "")
		})
	s.AddTool(mcp.NewTool("stop_bridge", mcp.WithDescription("Stop the live-control bridge (UiAutomator2 on Android, WebDriverAgent on iOS)"), id),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/bridge/stop"), "")
		})
	s.AddTool(mcp.NewTool("bridge_status", mcp.WithDescription("Bridge status: supported, running, installed, lastError; Android adds port/sessionId, iOS adds state (stopped|starting|running|error), wdaUrl, mjpegUrl"), id),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "GET", devPath(req, "/bridge"), "")
		})
	s.AddTool(mcp.NewTool("ios_install_helper", mcp.WithDescription("Install the locally built WebDriverAgent runner onto an iOS device (explicit user action; the runner must be built first with scripts/build-wda.sh)."), id),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/iosbridge/install"), "")
		})
	// iOS Simulators: boot/shutdown. Booted simulators appear in list_devices
	// as "sim-<udid>"; use start_bridge on them to start WebDriverAgent.
	simID := mcp.WithString("udid", mcp.Required(), mcp.Description("Simulator udid from list_simulators"))
	simPath := func(req mcp.CallToolRequest, suffix string) string {
		return "/simulators/" + url.PathEscape(req.GetString("udid", "")) + suffix
	}
	s.AddTool(mcp.NewTool("list_simulators", mcp.WithDescription("List all iOS/watchOS simulators (booted or not) with runtime, state and WDA status")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "GET", "/simulators", "")
		})
	s.AddTool(mcp.NewTool("boot_simulator", mcp.WithDescription("Boot a simulator and wait until ready; it then appears in list_devices as sim-<udid>. Call start_bridge on that device id to enable screenshots-with-input, UI tree and gestures (WebDriverAgent)."), simID),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return runner.text(ctx, "POST", simPath(req, "/boot"), "")
		})
	s.AddTool(mcp.NewTool("shutdown_simulator", mcp.WithDescription("Stop WebDriverAgent (if running) and shut a simulator down"), simID),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", simPath(req, "/shutdown"), "")
		})
	s.AddTool(mcp.NewTool("pinch", mcp.WithDescription("Two-finger pinch around a point; scale>1 zooms in, <1 zooms out"), id,
		num("x", "Center X"), num("y", "Center Y"), num("scale", "Scale factor (>0)"),
		mcp.WithNumber("durationMs", mcp.Description("Duration in ms (default 500)"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", devPath(req, "/pinch"), jsonBody(
				"x", req.GetInt("x", 0), "y", req.GetInt("y", 0),
				"scale", req.GetFloat("scale", 1), "durationMs", req.GetInt("durationMs", 500)))
		})

	// Test projects and .mob scripts.
	pid := mcp.WithString("projectId", mcp.Required(), mcp.Description("Test project id from test_list_projects"))
	s.AddTool(mcp.NewTool("test_list_projects", mcp.WithDescription("List test projects")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "GET", "/tests/projects", "")
		})
	s.AddTool(mcp.NewTool("test_list_scripts", mcp.WithDescription("List the scripts (.mob or .json) of a test project"), pid),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "GET", "/tests/projects/"+url.PathEscape(req.GetString("projectId", ""))+"/scripts", "")
		})
	s.AddTool(mcp.NewTool("test_run", mcp.WithDescription("Run a stored test script on a device and return the per-step result (also recorded in run history)"), pid,
		mcp.WithString("script", mcp.Required(), mcp.Description("Script file name, e.g. login.mob")),
		mcp.WithString("deviceId", mcp.Required(), mcp.Description("Device id from list_devices"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path := "/tests/projects/" + url.PathEscape(req.GetString("projectId", "")) + "/scripts/" +
				url.PathEscape(req.GetString("script", "")) + "/run"
			b, code, err := runner.do(ctx, "POST", path, "application/json", jsonBody("deviceId", req.GetString("deviceId", "")))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if code < 200 || code > 299 {
				return mcp.NewToolResultError(fmt.Sprintf("HTTP %d: %s", code, b)), nil
			}
			return mcp.NewToolResultText(string(stripScreenshots(b))), nil
		})
	s.AddTool(mcp.NewTool("test_list_runs", mcp.WithDescription("List recent test runs, newest first"),
		mcp.WithString("projectId", mcp.Description("Only runs of this project")),
		mcp.WithNumber("limit", mcp.Description("Max runs to return"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			q := url.Values{}
			if p := req.GetString("projectId", ""); p != "" {
				q.Set("projectId", p)
			}
			if n := req.GetInt("limit", 0); n > 0 {
				q.Set("limit", fmt.Sprint(n))
			}
			path := "/tests/runs"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			return c.text(ctx, "GET", path, "")
		})

	// On-screen text recognition (OCR).
	boolOpt := func(n, d string) mcp.ToolOption { return mcp.WithBoolean(n, mcp.Description(d)) }
	s.AddTool(mcp.NewTool("ocr_screen", mcp.WithDescription("Screenshot the device and recognize on-screen text; returns lines with pixel bounding boxes"), id,
		boolOpt("words", "Return word-level boxes instead of lines")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", "/ocr/devices/"+url.PathEscape(req.GetString("id", "")), jsonBody("words", req.GetBool("words", false)))
		})
	s.AddTool(mcp.NewTool("ocr_tap", mcp.WithDescription("Find on-screen text with OCR and tap its centre (useful for canvas/game/WebView UIs with no accessibility tree)"), id,
		mcp.WithString("text", mcp.Required(), mcp.Description("Text to find (case-insensitive substring unless exact/regex)")),
		mcp.WithNumber("index", mcp.Description("Which match to tap, 0-based (default 0)")),
		boolOpt("exact", "Whole-text match"), boolOpt("regex", "Treat text as a regular expression")),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "POST", "/ocr/devices/"+url.PathEscape(req.GetString("id", ""))+"/tap", jsonBody(
				"text", req.GetString("text", ""), "index", req.GetInt("index", 0),
				"exact", req.GetBool("exact", false), "regex", req.GetBool("regex", false)))
		})

	// Web (Chrome / WebView) automation over CDP (Android only).
	target := mcp.WithString("targetId", mcp.Description("Page target id from web_targets (default: first page)"))
	webCall := func(path string, fields map[string]string) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			body := map[string]string{}
			for arg, key := range fields {
				body[key] = req.GetString(arg, "")
			}
			if t := req.GetString("targetId", ""); t != "" {
				body["targetId"] = t
			}
			b, _ := json.Marshal(body)
			return c.text(ctx, "POST", "/web/devices/"+url.PathEscape(req.GetString("id", ""))+path, string(b))
		}
	}
	s.AddTool(mcp.NewTool("web_targets", mcp.WithDescription("List debuggable Chrome/WebView page targets on an Android device"), id),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return c.text(ctx, "GET", "/web/devices/"+url.PathEscape(req.GetString("id", ""))+"/targets", "")
		})
	s.AddTool(mcp.NewTool("web_navigate", mcp.WithDescription("Navigate a Chrome/WebView page to a URL"), id, target,
		mcp.WithString("url", mcp.Required(), mcp.Description("URL to load"))),
		webCall("/navigate", map[string]string{"url": "url"}))
	s.AddTool(mcp.NewTool("web_eval", mcp.WithDescription("Evaluate JavaScript in a Chrome/WebView page and return the result"), id, target,
		mcp.WithString("script", mcp.Required(), mcp.Description("JavaScript expression"))),
		webCall("/eval", map[string]string{"script": "script"}))
	s.AddTool(mcp.NewTool("web_click", mcp.WithDescription("Click the first element matching a CSS selector in a page"), id, target,
		mcp.WithString("selector", mcp.Required(), mcp.Description("CSS selector"))),
		webCall("/click", map[string]string{"selector": "selector"}))
	s.AddTool(mcp.NewTool("web_type", mcp.WithDescription("Type text into the element matching a CSS selector"), id, target,
		mcp.WithString("selector", mcp.Required(), mcp.Description("CSS selector")),
		mcp.WithString("text", mcp.Required(), mcp.Description("Text to type"))),
		webCall("/type", map[string]string{"selector": "selector", "text": "text"}))
	registerCrashAndDebugTools(s, c, id)
	return s
}

// stripScreenshots drops (large) base64 "screenshot" fields from a JSON body.
func stripScreenshots(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return b
	}
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case map[string]any:
			delete(t, "screenshot")
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		}
	}
	walk(v)
	out, err := json.Marshal(v)
	if err != nil {
		return b
	}
	return out
}

// Handler returns an http.Handler serving s over MCP streamable HTTP.
func Handler(s *server.MCPServer) http.Handler {
	return server.NewStreamableHTTPServer(s)
}
