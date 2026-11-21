package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"phoneforge/internal/agent/llm"
	"phoneforge/internal/device"
)

type fakeDevice struct {
	mu    sync.Mutex
	tree  device.Element
	taps  [][2]int
	keys  []device.Key
	typed []string
}

func (f *fakeDevice) Info() device.Info { return device.Info{ID: "fake"} }
func (f *fakeDevice) Screenshot(context.Context) (device.Screenshot, error) {
	return device.Screenshot{PNG: []byte{1, 2, 3}, Width: 10, Height: 20}, nil
}
func (f *fakeDevice) UITree(context.Context) (device.Element, error) { return f.tree, nil }
func (f *fakeDevice) Tap(_ context.Context, x, y int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.taps = append(f.taps, [2]int{x, y})
	return nil
}
func (f *fakeDevice) LongPress(context.Context, int, int, int) error       { return nil }
func (f *fakeDevice) Swipe(context.Context, int, int, int, int, int) error { return nil }
func (f *fakeDevice) TypeText(_ context.Context, t string) error {
	f.typed = append(f.typed, t)
	return nil
}
func (f *fakeDevice) PressKey(_ context.Context, k device.Key) error {
	f.keys = append(f.keys, k)
	return nil
}
func (f *fakeDevice) ListApps(context.Context) ([]device.AppInfo, error) { return nil, nil }
func (f *fakeDevice) LaunchApp(context.Context, string) error            { return nil }
func (f *fakeDevice) StopApp(context.Context, string) error              { return nil }

func cannedTree() device.Element {
	return device.Element{
		Role:   "Window",
		Bounds: device.Rect{W: 1080, H: 1920},
		Children: []device.Element{
			{Role: "Text", Text: "Network & internet", Bounds: device.Rect{X: 0, Y: 100, W: 1080, H: 100}},
			{Role: "Group", Label: "Search settings", Bounds: device.Rect{X: 0, Y: 0, W: 0, H: 0}},
			{Role: "Group", Children: []device.Element{
				{Role: "Text", Text: "Settings", Bounds: device.Rect{X: 100, Y: 300, W: 200, H: 100}},
				{Role: "Button", Label: "More settings options", Bounds: device.Rect{X: 900, Y: 20, W: 100, H: 100}},
			}},
		},
	}
}

func TestFindByText(t *testing.T) {
	root := cannedTree()
	el, ok := findByText(root, "settings")
	if !ok || el.Text != "Settings" {
		t.Fatalf("exact match should win, got %+v ok=%v", el, ok)
	}
	if x, y := el.Bounds.Center(); x != 200 || y != 350 {
		t.Fatalf("center = %d,%d", x, y)
	}
	el, ok = findByText(root, "internet")
	if !ok || el.Text != "Network & internet" {
		t.Fatalf("substring match failed: %+v", el)
	}
	el, ok = findByText(root, "options")
	if !ok || el.Role != "Button" {
		t.Fatalf("label match failed: %+v", el)
	}
	if _, ok := findByText(root, "search"); ok {
		t.Fatal("zero-size element must be skipped")
	}
	if _, ok := findByText(root, "nope"); ok {
		t.Fatal("unexpected match")
	}
}

func TestRunNoProvider(t *testing.T) {
	old := selectClient
	defer func() { selectClient = old }()
	selectClient = func() (llm.Client, string, bool) {
		return llm.Select(func(string) string { return "" }, func(string) bool { return false })
	}
	if p, ok := Available(); ok || p != "" {
		t.Fatalf("Available = %q,%v", p, ok)
	}
	var emitted int
	res, err := Run(context.Background(), &fakeDevice{}, "do x", 0, func(Step) { emitted++ })
	if err != nil {
		t.Fatal(err)
	}
	if res.Done || res.Error != "no LLM provider configured (set ANTHROPIC_API_KEY or run Ollama)" {
		t.Fatalf("unexpected result %+v", res)
	}
	if res.Task != "do x" || emitted != 1 {
		t.Fatalf("task=%q emitted=%d", res.Task, emitted)
	}
}

func TestSelectProviders(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if _, p, ok := llm.Select(env(map[string]string{"ANTHROPIC_API_KEY": "k", "OLLAMA_HOST": "h"}), nil); !ok || p != "anthropic" {
		t.Fatalf("anthropic first: %q %v", p, ok)
	}
	if _, p, ok := llm.Select(env(map[string]string{"OLLAMA_HOST": "127.0.0.1:1"}), nil); !ok || p != "ollama" {
		t.Fatalf("ollama host: %q %v", p, ok)
	}
	if _, p, ok := llm.Select(env(nil), func(string) bool { return true }); !ok || p != "ollama" {
		t.Fatalf("ollama probe: %q %v", p, ok)
	}
}

type fakeClient struct {
	replies []llm.Reply
	calls   int
	seen    [][]llm.Message
}

func (c *fakeClient) Chat(_ context.Context, _ string, msgs []llm.Message, _ []llm.Tool) (llm.Reply, error) {
	cp := make([]llm.Message, len(msgs))
	copy(cp, msgs)
	c.seen = append(c.seen, cp)
	if c.calls >= len(c.replies) {
		return llm.Reply{}, errors.New("script exhausted")
	}
	r := c.replies[c.calls]
	c.calls++
	return r, nil
}

func TestRunWithTwoSteps(t *testing.T) {
	settleDelay = 0
	dev := &fakeDevice{tree: cannedTree()}
	cl := &fakeClient{replies: []llm.Reply{
		{Text: "Open Settings.", ToolCalls: []llm.ToolCall{{ID: "t1", Name: "tap_text", Args: map[string]any{"text": "Settings"}}}},
		{Text: "All done.", ToolCalls: []llm.ToolCall{{ID: "t2", Name: "done", Args: map[string]any{"summary": "opened settings"}}}},
	}}
	var emitted []Step
	res, err := runWith(context.Background(), dev, cl, "open settings", 5, func(s Step) { emitted = append(emitted, s) })
	if err != nil {
		t.Fatal(err)
	}
	if !res.Done || res.Error != "" {
		t.Fatalf("result %+v", res)
	}
	want := []string{"thought", "action", "observation", "thought", "done"}
	if len(res.Steps) != len(want) || len(emitted) != len(want) {
		t.Fatalf("steps=%d emitted=%d: %+v", len(res.Steps), len(emitted), res.Steps)
	}
	for i, w := range want {
		if res.Steps[i].Type != w {
			t.Fatalf("step %d type %q want %q", i, res.Steps[i].Type, w)
		}
	}
	if len(dev.taps) != 1 || dev.taps[0] != [2]int{200, 350} {
		t.Fatalf("taps = %v", dev.taps)
	}
	if res.Steps[4].Text != "opened settings" {
		t.Fatalf("done text %q", res.Steps[4].Text)
	}
	// Second call must carry a tool_result answering t1.
	last := cl.seen[1][len(cl.seen[1])-1]
	if last.Role != "user" || last.Content[0].Type != llm.BlockToolResult || last.Content[0].ID != "t1" {
		t.Fatalf("bad tool_result message: %+v", last)
	}
}

func TestRunWithMaxStepsAndCancel(t *testing.T) {
	settleDelay = 0
	dev := &fakeDevice{tree: cannedTree()}
	call := llm.Reply{ToolCalls: []llm.ToolCall{{Name: "press_key", Args: map[string]any{"key": "back"}}}}
	cl := &fakeClient{}
	for i := 0; i < 40; i++ {
		cl.replies = append(cl.replies, call)
	}
	res, err := runWith(context.Background(), dev, cl, "loop", 20, nil)
	if err != nil || res.Done || !strings.Contains(res.Error, "max steps") {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	// Transcript stays bounded.
	for _, m := range cl.seen {
		if len(m) > 1+2*keepTurns {
			t.Fatalf("transcript too long: %d", len(m))
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err = runWith(ctx, dev, &fakeClient{}, "x", 3, nil)
	if !errors.Is(err, context.Canceled) || res.Done || res.Error == "" {
		t.Fatalf("cancel: res=%+v err=%v", res, err)
	}
}
