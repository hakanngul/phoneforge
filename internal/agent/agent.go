// Package agent runs an LLM tool-use loop that drives a device.Device to
// accomplish a natural-language task.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"pocketlab/internal/agent/llm"
	"pocketlab/internal/device"
	"pocketlab/internal/uitree"
)

// Step is one recorded event of an agent run.
type Step struct {
	// Type is one of thought|action|observation|done|error.
	Type   string         `json:"type"`
	Text   string         `json:"text,omitempty"`
	Tool   string         `json:"tool,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
	Result string         `json:"result,omitempty"`
}

// Result is the outcome of an agent run.
type Result struct {
	Task  string `json:"task"`
	Steps []Step `json:"steps"`
	// Done is true only if the model finished by calling done().
	Done  bool   `json:"done"`
	Error string `json:"error,omitempty"`
}

const (
	defaultMaxSteps = 15
	// keepTurns is the number of most recent assistant/user turn pairs kept
	// in the transcript (the first message, holding the task, always stays).
	keepTurns    = 8
	maxTreeNodes = 150
	maxTreeChars = 12000
	maxTextLen   = 80
)

// ErrNoProvider is the message reported when no LLM is configured.
const ErrNoProvider = "no LLM provider configured (set ANTHROPIC_API_KEY or run Ollama)"

// selectClient is a seam for tests.
var selectClient = llm.FromEnv

// settleDelay is how long to wait after a device action so the UI can update.
var settleDelay = 700 * time.Millisecond

// selectClientModel is selectClient with a model override (a seam for tests).
var selectClientModel = llm.FromEnvModel

// Available reports which LLM provider is configured via the environment
// ("anthropic" or "ollama"). It returns ("", false) if none is.
func Available() (provider string, ok bool) {
	_, p, ok := selectClient()
	return p, ok
}

// Run drives dev to accomplish task using the configured LLM provider. It
// performs at most maxSteps model turns (default 15 if <= 0) and calls emit,
// if non-nil, for every Step as it happens. If no provider is configured it
// returns a Result with Error set and a nil error.
func Run(ctx context.Context, dev device.Device, task string, maxSteps int, emit func(Step)) (Result, error) {
	return RunModel(ctx, dev, task, maxSteps, "", emit)
}

// RunModel is Run with an optional model override for the selected provider
// (an Ollama model name or an Anthropic model id). Empty keeps the default.
func RunModel(ctx context.Context, dev device.Device, task string, maxSteps int, model string, emit func(Step)) (Result, error) {
	var (
		client llm.Client
		ok     bool
	)
	if strings.TrimSpace(model) == "" {
		client, _, ok = selectClient()
	} else {
		client, _, ok = selectClientModel(model)
	}
	if !ok {
		res := Result{Task: task, Steps: []Step{}, Error: ErrNoProvider}
		s := Step{Type: "error", Text: ErrNoProvider}
		res.Steps = append(res.Steps, s)
		if emit != nil {
			emit(s)
		}
		return res, nil
	}
	return runWith(ctx, dev, client, task, maxSteps, emit)
}

const systemPrompt = `You are a mobile device automation agent. You control a real phone through tools to accomplish the user's task.
Each turn you receive the current UI tree (one line per element: [index] Role "label" id=... @(x,y) WxH flags, where @(x,y) is the tap center) and possibly a screenshot.
Call exactly one tool per turn. Prefer tap_text when an element has visible text or a label; otherwise tap the @(x,y) center of the element.
Use launch_app to open apps (list_apps shows installed bundle ids), type_text after focusing a text field, press_key for home/back/enter/delete/app_switch, and swipe to scroll. double_tap, long_press, drag and open_url are also available (they may be unsupported on some devices).
If something did not work, look at the new UI state and try a different approach. When the task is complete (or impossible), call done with a short summary.`

func toolDefs() []llm.Tool {
	obj := func(props map[string]any, req ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if len(req) > 0 {
			s["required"] = req
		}
		return s
	}
	num := map[string]any{"type": "integer"}
	str := map[string]any{"type": "string"}
	return []llm.Tool{
		{Name: "screenshot", Description: "Capture a fresh screenshot of the screen.", Schema: obj(map[string]any{})},
		{Name: "ui_tree", Description: "Return the current UI elements as compact text lines.", Schema: obj(map[string]any{})},
		{Name: "tap", Description: "Tap at pixel coordinates.", Schema: obj(map[string]any{"x": num, "y": num}, "x", "y")},
		{Name: "tap_text", Description: "Tap the center of the first element whose text or label contains the given text (case-insensitive).", Schema: obj(map[string]any{"text": str}, "text")},
		{Name: "swipe", Description: "Swipe from (x1,y1) to (x2,y2) over durationMs milliseconds.", Schema: obj(map[string]any{"x1": num, "y1": num, "x2": num, "y2": num, "durationMs": num}, "x1", "y1", "x2", "y2")},
		{Name: "double_tap", Description: "Double-tap at pixel coordinates.", Schema: obj(map[string]any{"x": num, "y": num}, "x", "y")},
		{Name: "long_press", Description: "Long-press at pixel coordinates for ms milliseconds (default 600).", Schema: obj(map[string]any{"x": num, "y": num, "ms": num}, "x", "y")},
		{Name: "drag", Description: "Press at (x1,y1), drag to (x2,y2) over durationMs milliseconds and release (e.g. to move sliders or reorder items).", Schema: obj(map[string]any{"x1": num, "y1": num, "x2": num, "y2": num, "durationMs": num}, "x1", "y1", "x2", "y2")},
		{Name: "open_url", Description: "Open a URL or deep link (http, https or app scheme) on the device.", Schema: obj(map[string]any{"url": str}, "url")},
		{Name: "type_text", Description: "Type text into the focused field.", Schema: obj(map[string]any{"text": str}, "text")},
		{Name: "press_key", Description: "Press a key: home, back, enter, delete or app_switch (recent apps).", Schema: obj(map[string]any{"key": map[string]any{"type": "string", "enum": []string{"home", "back", "enter", "delete", "app_switch"}}}, "key")},
		{Name: "launch_app", Description: "Launch an app by bundle id / package name.", Schema: obj(map[string]any{"bundleId": str}, "bundleId")},
		{Name: "list_apps", Description: "List installed apps (bundle ids and names).", Schema: obj(map[string]any{})},
		{Name: "done", Description: "Finish the task. Call when the task is complete or cannot be completed.", Schema: obj(map[string]any{"summary": str}, "summary")},
	}
}

// runWith is Run with an injected LLM client.
func runWith(ctx context.Context, dev device.Device, client llm.Client, task string, maxSteps int, emit func(Step)) (Result, error) {
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	res := Result{Task: task, Steps: []Step{}}
	record := func(s Step) {
		res.Steps = append(res.Steps, s)
		if emit != nil {
			emit(s)
		}
	}
	fail := func(err error) (Result, error) {
		res.Error = err.Error()
		record(Step{Type: "error", Text: res.Error})
		return res, err
	}

	vision := llm.SupportsVision(client)
	tools := toolDefs()
	msgs := []llm.Message{{
		Role: "user",
		Content: append([]llm.Block{{Type: llm.BlockText, Text: "Task: " + task}},
			observe(ctx, dev, vision)...),
	}}

	for i := 0; i < maxSteps; i++ {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		reply, err := client.Chat(ctx, systemPrompt, msgs, tools)
		if err != nil {
			return fail(fmt.Errorf("llm: %w", err))
		}
		if strings.TrimSpace(reply.Text) != "" {
			record(Step{Type: "thought", Text: strings.TrimSpace(reply.Text)})
		}

		asst := llm.Message{Role: "assistant"}
		if strings.TrimSpace(reply.Text) != "" {
			asst.Content = append(asst.Content, llm.Block{Type: llm.BlockText, Text: reply.Text})
		}

		if len(reply.ToolCalls) == 0 {
			if len(asst.Content) == 0 {
				asst.Content = []llm.Block{{Type: llm.BlockText, Text: "(no response)"}}
			}
			user := llm.Message{Role: "user", Content: append([]llm.Block{{
				Type: llm.BlockText,
				Text: "You must call a tool. Call done(summary) if the task is finished.",
			}}, observe(ctx, dev, vision)...)}
			msgs = appendTurn(msgs, asst, user)
			continue
		}

		for j := range reply.ToolCalls {
			if reply.ToolCalls[j].ID == "" {
				reply.ToolCalls[j].ID = fmt.Sprintf("call_%d_%d", i, j)
			}
			tc := reply.ToolCalls[j]
			asst.Content = append(asst.Content, llm.Block{Type: llm.BlockToolUse, ID: tc.ID, Name: tc.Name, Input: tc.Args})
		}

		call := reply.ToolCalls[0]
		if call.Name == "done" {
			summary, _ := call.Args["summary"].(string)
			res.Done = true
			record(Step{Type: "done", Text: summary, Tool: "done", Args: call.Args})
			return res, nil
		}

		record(Step{Type: "action", Tool: call.Name, Args: call.Args})
		out, execErr := execTool(ctx, dev, call.Name, call.Args, vision)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fail(ctxErr)
		}
		isErr := execErr != nil
		if isErr {
			out = "error: " + execErr.Error()
		}
		record(Step{Type: "observation", Tool: call.Name, Result: out})

		if err := sleepCtx(ctx, settleDelay); err != nil {
			return fail(err)
		}

		var results []llm.Block
		results = append(results, llm.Block{Type: llm.BlockToolResult, ID: call.ID, Name: call.Name, Content: out, IsError: isErr})
		for _, extra := range reply.ToolCalls[1:] {
			results = append(results, llm.Block{Type: llm.BlockToolResult, ID: extra.ID, Name: extra.Name,
				Content: "skipped: only one tool call is executed per turn", IsError: true})
		}
		user := llm.Message{Role: "user", Content: append(results, observe(ctx, dev, vision)...)}
		msgs = appendTurn(msgs, asst, user)
	}

	res.Error = fmt.Sprintf("max steps (%d) reached without done()", maxSteps)
	record(Step{Type: "error", Text: res.Error})
	return res, nil
}

// appendTurn adds an assistant/user pair, strips stale observations from
// older messages, and bounds the transcript length keeping the first message.
func appendTurn(msgs []llm.Message, asst, user llm.Message) []llm.Message {
	for i := range msgs {
		kept := msgs[i].Content[:0:0]
		for _, b := range msgs[i].Content {
			if b.Transient {
				continue
			}
			b.Image = nil
			kept = append(kept, b)
		}
		msgs[i].Content = kept
	}
	msgs = append(msgs, asst, user)
	if max := 1 + 2*keepTurns; len(msgs) > max {
		trimmed := make([]llm.Message, 0, max)
		trimmed = append(trimmed, msgs[0])
		trimmed = append(trimmed, msgs[len(msgs)-2*keepTurns:]...)
		msgs = trimmed
	}
	return msgs
}

// observe builds the transient blocks describing the current screen.
func observe(ctx context.Context, dev device.Device, vision bool) []llm.Block {
	var blocks []llm.Block
	if root, err := dev.UITree(ctx); err != nil {
		blocks = append(blocks, llm.Block{Type: llm.BlockText, Transient: true, Text: "Current UI tree unavailable: " + err.Error()})
	} else {
		blocks = append(blocks, llm.Block{Type: llm.BlockText, Transient: true, Text: "Current UI tree:\n" + compactTree(root)})
	}
	if vision {
		if shot, err := dev.Screenshot(ctx); err == nil && len(shot.PNG) > 0 {
			blocks = append(blocks,
				llm.Block{Type: llm.BlockText, Transient: true, Text: fmt.Sprintf("Current screenshot (%dx%d):", shot.Width, shot.Height)},
				llm.Block{Type: llm.BlockImage, Transient: true, Image: shot.PNG})
		}
	}
	return blocks
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// execTool dispatches one tool call to the device.
func execTool(ctx context.Context, dev device.Device, name string, args map[string]any, vision bool) (string, error) {
	switch name {
	case "screenshot":
		shot, err := dev.Screenshot(ctx)
		if err != nil {
			return "", err
		}
		if vision {
			return fmt.Sprintf("screenshot captured (%dx%d); it is attached to the next observation", shot.Width, shot.Height), nil
		}
		return fmt.Sprintf("screenshot captured (%dx%d); this model cannot view images, rely on ui_tree", shot.Width, shot.Height), nil
	case "ui_tree":
		root, err := dev.UITree(ctx)
		if err != nil {
			return "", err
		}
		return compactTree(root), nil
	case "tap":
		x, y, err := twoInts(args, "x", "y")
		if err != nil {
			return "", err
		}
		if err := dev.Tap(ctx, x, y); err != nil {
			return "", err
		}
		return fmt.Sprintf("tapped (%d,%d)", x, y), nil
	case "tap_text":
		text := strArg(args, "text")
		if text == "" {
			return "", errors.New("text is required")
		}
		root, err := dev.UITree(ctx)
		if err != nil {
			return "", err
		}
		el, ok := findByText(root, text)
		if !ok {
			return "", fmt.Errorf("no element with text or label containing %q", text)
		}
		x, y := el.Bounds.Center()
		if err := dev.Tap(ctx, x, y); err != nil {
			return "", err
		}
		return fmt.Sprintf("tapped %q at (%d,%d)", firstNonEmpty(el.Text, el.Label), x, y), nil
	case "swipe":
		x1, y1, err := twoInts(args, "x1", "y1")
		if err != nil {
			return "", err
		}
		x2, y2, err := twoInts(args, "x2", "y2")
		if err != nil {
			return "", err
		}
		ms := 300
		if v, err := intArg(args, "durationMs"); err == nil && v > 0 {
			ms = v
		}
		if err := dev.Swipe(ctx, x1, y1, x2, y2, ms); err != nil {
			return "", err
		}
		return fmt.Sprintf("swiped (%d,%d)->(%d,%d) in %dms", x1, y1, x2, y2, ms), nil
	case "double_tap":
		x, y, err := twoInts(args, "x", "y")
		if err != nil {
			return "", err
		}
		g, ok := dev.(device.GestureExtras)
		if !ok {
			return "", fmt.Errorf("double_tap: %w", device.ErrUnsupported)
		}
		if err := g.DoubleTap(ctx, x, y); err != nil {
			return "", err
		}
		return fmt.Sprintf("double-tapped (%d,%d)", x, y), nil
	case "long_press":
		x, y, err := twoInts(args, "x", "y")
		if err != nil {
			return "", err
		}
		ms := 600
		if v, err := intArg(args, "ms"); err == nil && v > 0 {
			ms = v
		}
		if err := dev.LongPress(ctx, x, y, ms); err != nil {
			return "", err
		}
		return fmt.Sprintf("long-pressed (%d,%d) for %dms", x, y, ms), nil
	case "drag":
		x1, y1, err := twoInts(args, "x1", "y1")
		if err != nil {
			return "", err
		}
		x2, y2, err := twoInts(args, "x2", "y2")
		if err != nil {
			return "", err
		}
		g, ok := dev.(device.GestureExtras)
		if !ok {
			return "", fmt.Errorf("drag: %w", device.ErrUnsupported)
		}
		ms := 500
		if v, err := intArg(args, "durationMs"); err == nil && v > 0 {
			ms = v
		}
		if err := g.Drag(ctx, x1, y1, x2, y2, ms); err != nil {
			return "", err
		}
		return fmt.Sprintf("dragged (%d,%d)->(%d,%d) in %dms", x1, y1, x2, y2, ms), nil
	case "open_url":
		u := strArg(args, "url")
		if u == "" {
			return "", errors.New("url is required")
		}
		o, ok := dev.(device.URLOpener)
		if !ok {
			return "", fmt.Errorf("open_url: %w", device.ErrUnsupported)
		}
		if err := o.OpenURL(ctx, u); err != nil {
			return "", err
		}
		return "opened " + u, nil
	case "type_text":
		text := strArg(args, "text")
		if err := dev.TypeText(ctx, text); err != nil {
			return "", err
		}
		return fmt.Sprintf("typed %d characters", len([]rune(text))), nil
	case "press_key":
		k := device.Key(strings.ToLower(strArg(args, "key")))
		switch k {
		case device.KeyHome, device.KeyBack, device.KeyEnter, device.KeyDelete:
		default:
			if !device.IsAppSwitch(k) {
				return "", fmt.Errorf("unknown key %q (use home, back, enter, delete, app_switch)", k)
			}
		}
		if err := dev.PressKey(ctx, k); err != nil {
			return "", err
		}
		return "pressed " + string(k), nil
	case "launch_app":
		id := strArg(args, "bundleId")
		if id == "" {
			return "", errors.New("bundleId is required")
		}
		if err := dev.LaunchApp(ctx, id); err != nil {
			return "", err
		}
		return "launched " + id, nil
	case "list_apps":
		apps, err := dev.ListApps(ctx)
		if err != nil {
			return "", err
		}
		if len(apps) > 200 {
			apps = apps[:200]
		}
		b, _ := json.Marshal(apps)
		return string(b), nil
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

// findByText searches the tree depth-first for an element whose Text or
// Label contains text (case-insensitive) and has a non-empty bounding box.
// An exact (case-insensitive) match wins over a substring match; ties go to
// the first element in document order.
func findByText(root device.Element, text string) (device.Element, bool) {
	needle := strings.ToLower(strings.TrimSpace(text))
	if needle == "" {
		return device.Element{}, false
	}
	var contains *device.Element
	var exact *device.Element
	var walk func(e *device.Element)
	walk = func(e *device.Element) {
		if exact != nil {
			return
		}
		if e.Bounds.W > 0 && e.Bounds.H > 0 {
			for _, s := range []string{e.Text, e.Label} {
				ls := strings.ToLower(strings.TrimSpace(s))
				if ls == "" {
					continue
				}
				if ls == needle {
					exact = e
					return
				}
				if contains == nil && strings.Contains(ls, needle) {
					contains = e
				}
			}
		}
		for i := range e.Children {
			walk(&e.Children[i])
		}
	}
	walk(&root)
	switch {
	case exact != nil:
		return *exact, true
	case contains != nil:
		return *contains, true
	}
	return device.Element{}, false
}

// compactTree renders the UI tree with the shared compact formatter.
func compactTree(root device.Element) string {
	return uitree.Format(root, uitree.Options{MaxNodes: maxTreeNodes, MaxChars: maxTreeChars})
}

func clip(s string) string {
	r := []rune(s)
	if len(r) > maxTextLen {
		return string(r[:maxTextLen]) + "..."
	}
	return s
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func strArg(args map[string]any, k string) string {
	switch v := args[k].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func intArg(args map[string]any, k string) (int, error) {
	switch v := args[k].(type) {
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case json.Number:
		f, err := v.Float64()
		return int(f), err
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, fmt.Errorf("argument %s: not a number: %q", k, v)
		}
		return int(f), nil
	case nil:
		return 0, fmt.Errorf("argument %s is required", k)
	default:
		return 0, fmt.Errorf("argument %s: unsupported type %T", k, v)
	}
}

func twoInts(args map[string]any, a, b string) (int, int, error) {
	x, err := intArg(args, a)
	if err != nil {
		return 0, 0, err
	}
	y, err := intArg(args, b)
	if err != nil {
		return 0, 0, err
	}
	return x, y, nil
}
