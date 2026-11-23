package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mark3labs/mcp-go/mcp"

	"pocketlab/internal/mobflow"
)

const flowCase = `version: 1
name: Two steps
steps:
  - {id: s1, type: act, title: Open, do: "Open the app"}
  - {id: s2, type: assert, title: Check, do: "Look at the home", expect: "Home is shown"}
`

// flowHarness serves the real flows routes and returns tool callers wired to it.
func flowHarness(t *testing.T) (call func(name string, args map[string]any) (string, bool), svc *mobflow.Service, caseID string) {
	t.Helper()
	svc, err := mobflow.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc.ScreenshotFunc = func(context.Context, string) ([]byte, error) { return []byte("png"), nil }
	p, err := svc.Store.Projects().CreateProject("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Store.Save(p.ID, "two", []byte(flowCase)); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Mount("/api/v1/flows", svc.Routes())
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	tools := New(ts.URL+"/api/v1", "test").ListTools()
	call = func(name string, args map[string]any) (string, bool) {
		t.Helper()
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		res, err := tool.Handler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: name, Arguments: args}})
		if err != nil {
			t.Fatal(err)
		}
		return resultText(res), res.IsError
	}
	return call, svc, p.ID + "/two"
}

func TestWorkflowFakeAgentEndToEnd(t *testing.T) {
	call, svc, id := flowHarness(t)
	project, name, _ := strings.Cut(id, "/")

	if out, isErr := call("workflow_list", map[string]any{"project": project}); isErr || !strings.Contains(out, `"two"`) {
		t.Fatalf("list: %v %s", isErr, out)
	}
	// no run yet: the active resolver errors verbatim
	if out, isErr := call("workflow_step_start", map[string]any{"case_id": id, "step_id": "s1"}); !isErr || !strings.Contains(out, "no running run") {
		t.Fatalf("no run: %v %s", isErr, out)
	}
	run, err := svc.StartRun(mobflow.StartRunRequest{Project: project, Case: name, DeviceID: "d1", Mode: "record", Agent: "external"})
	if err != nil {
		t.Fatal(err)
	}
	out, isErr := call("workflow_get_case", map[string]any{"case_id": id})
	var gc struct {
		Case       struct{ Steps []struct{ ID string } }
		Staleness  map[string]string
		CurrentRun struct{ ID string } `json:"current_run"`
	}
	if isErr || json.Unmarshal([]byte(out), &gc) != nil || len(gc.Case.Steps) != 2 || gc.Staleness["s1"] != "missing" || gc.CurrentRun.ID != run.ID {
		t.Fatalf("get_case: %v %s", isErr, out)
	}

	// errors surface verbatim and name valid ids
	if out, isErr := call("workflow_step_start", map[string]any{"case_id": id, "step_id": "s2"}); !isErr || !strings.Contains(out, `next pending step is \"s1\"`) {
		t.Fatalf("out of order: %v %s", isErr, out)
	}
	if out, isErr := call("workflow_step_start", map[string]any{"case_id": id, "step_id": "zz"}); !isErr || !strings.Contains(out, "s1, s2") {
		t.Fatalf("unknown: %v %s", isErr, out)
	}

	ok := func(name string, args map[string]any) {
		t.Helper()
		if out, isErr := call(name, args); isErr {
			t.Fatalf("%s: %s", name, out)
		}
	}
	// s1 addressed by run_id, s2 via the active resolver
	ok("workflow_step_start", map[string]any{"case_id": id, "step_id": "s1", "run_id": run.ID})
	ok("workflow_substep", map[string]any{"case_id": id, "step_id": "s1", "text": "launched app"})
	ok("workflow_step_done", map[string]any{"case_id": id, "step_id": "s1", "passed": true, "notes": "ok"})
	if out, isErr := call("workflow_step_done", map[string]any{"case_id": id, "step_id": "s1", "passed": true}); !isErr || !strings.Contains(out, "already done") {
		t.Fatalf("double done: %v %s", isErr, out)
	}
	ok("workflow_step_start", map[string]any{"case_id": id, "step_id": "s2"})
	ok("workflow_assert", map[string]any{"case_id": id, "step_id": "s2", "expectation": "Home is shown", "passed": true, "detail": "saw Home"})
	ok("workflow_step_done", map[string]any{"case_id": id, "step_id": "s2", "passed": true})

	cur, _ := svc.GetRun(run.ID)
	if cur.Status != "running" || len(cur.Steps[1].Asserts) != 1 || cur.Steps[0].Screenshot == "" {
		t.Fatalf("run before cache: %+v", cur)
	}
	segs := []any{
		map[string]any{"id": "s1", "steps": []any{map[string]any{"action": "launch_app", "bundleId": "com.x"}}},
		map[string]any{"id": "s2", "steps": []any{map[string]any{"action": "assert_exists", "selector": map[string]any{"text": "Home"}}}},
	}
	if out, isErr := call("workflow_cache_script", map[string]any{"case_id": id, "segments": segs}); isErr {
		t.Fatalf("cache: %s", out)
	}
	cur, _ = svc.GetRun(run.ID)
	if cur.Status != "passed" || !cur.CacheSubmitted {
		t.Fatalf("run after cache: %+v", cur)
	}
	c, err := svc.Store.GetCache(project, name, "")
	if err != nil || len(c.Segments) != 2 || c.Segments[0].StepHash == "" {
		t.Fatalf("cache: %+v %v", c, err)
	}
}

func TestWorkflowFailedStepFailsRun(t *testing.T) {
	call, svc, id := flowHarness(t)
	project, name, _ := strings.Cut(id, "/")
	run, _ := svc.StartRun(mobflow.StartRunRequest{Project: project, Case: name, DeviceID: "d1", Mode: "record", Agent: "external"})
	call("workflow_step_start", map[string]any{"case_id": id, "step_id": "s1"})
	if out, isErr := call("workflow_step_done", map[string]any{"case_id": id, "step_id": "s1", "passed": false, "notes": "app crashed"}); isErr {
		t.Fatal(out)
	}
	got, _ := svc.GetRun(run.ID)
	if got.Status != "failed" || got.Steps[1].State != "skipped" || !strings.Contains(got.Error, "app crashed") {
		t.Fatalf("%+v", got)
	}
	if out, isErr := call("workflow_step_start", map[string]any{"case_id": id, "step_id": "s2", "run_id": run.ID}); !isErr || !strings.Contains(out, "already failed") {
		t.Fatalf("%v %s", isErr, out)
	}
	if _, isErr := call("workflow_step_start", map[string]any{"case_id": "badid", "step_id": "s1"}); !isErr {
		t.Fatal("bad case_id must error")
	}
}

// mcpLauncher is a fake agent: Start spawns a goroutine that records the case
// purely through the MCP tool handlers, like a CLI agent would.
type mcpLauncher struct {
	call   func(name string, args map[string]any) (string, bool)
	caseID string
	prompt chan string
}

func (l *mcpLauncher) Start(req mobflow.AgentRequest) (string, error) {
	go func() {
		l.prompt <- req.SystemPrompt + req.Prompt
		runID := strings.TrimSpace(strings.SplitN(strings.SplitN(req.SystemPrompt, "run_id: ", 2)[1], "\n", 2)[0])
		args := func(m map[string]any) map[string]any {
			m["case_id"], m["run_id"] = l.caseID, runID
			return m
		}
		l.call("workflow_get_case", map[string]any{"case_id": l.caseID})
		for _, id := range []string{"s1", "s2"} {
			l.call("workflow_step_start", args(map[string]any{"step_id": id}))
			l.call("workflow_substep", args(map[string]any{"step_id": id, "text": "did " + id}))
			if id == "s2" {
				l.call("workflow_assert", args(map[string]any{"step_id": id, "expectation": "Home is shown", "passed": true}))
			}
			l.call("workflow_step_done", args(map[string]any{"step_id": id, "passed": true}))
		}
		l.call("workflow_cache_script", args(map[string]any{"segments": []any{
			map[string]any{"id": "s1", "steps": []any{map[string]any{"action": "launch_app", "bundleId": "com.x"}}},
			map[string]any{"id": "s2", "steps": []any{map[string]any{"action": "assert_exists", "selector": map[string]any{"text": "Home"}}}},
		}}))
		req.OnLine("stdout", "agent finished")
		req.OnDone(nil)
	}()
	return "agent-x", nil
}

func (l *mcpLauncher) Cancel(string) {}

func TestWorkflowLaunchedAgentDrivesRunThroughMCP(t *testing.T) {
	call, svc, id := flowHarness(t)
	project, name, _ := strings.Cut(id, "/")
	l := &mcpLauncher{call: call, caseID: id, prompt: make(chan string, 1)}
	svc.Launcher = l
	run, err := svc.StartRun(mobflow.StartRunRequest{Project: project, Case: name, DeviceID: "d1", Mode: "record"})
	if err != nil {
		t.Fatal(err)
	}
	var got *mobflow.Run
	for i := 0; i < 300; i++ {
		got, _ = svc.GetRun(run.ID)
		if got.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Status != "passed" || !got.CacheSubmitted || got.AgentRunID != "agent-x" {
		t.Fatalf("run: %+v", got)
	}
	if p := <-l.prompt; !strings.Contains(p, id) || !strings.Contains(p, run.ID) {
		t.Fatalf("prompt lacks ids: %s", p)
	}
	c, err := svc.Store.GetCache(project, name, "")
	if err != nil || len(c.Segments) != 2 {
		t.Fatalf("cache: %+v %v", c, err)
	}
}
