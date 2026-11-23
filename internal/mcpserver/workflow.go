package mcpserver

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerWorkflowTools adds the workflow_* tools (Flows): thin clients of the
// /flows REST routes. case_id is "<project>/<name>"; run_id is optional and
// defaults to the single running run of the case. Server errors are returned
// verbatim because they name the valid step ids.
func registerWorkflowTools(s *server.MCPServer, c *client) {
	caseID := mcp.WithString("case_id", mcp.Required(), mcp.Description(`Case id "<project>/<name>" from workflow_list`))
	stepID := mcp.WithString("step_id", mcp.Required(), mcp.Description("Step id of the case (see workflow_get_case)"))
	runID := mcp.WithString("run_id", mcp.Description("Run id; optional, defaults to the running run of the case"))

	// target builds the REST path for a step/cache call.
	target := func(req mcp.CallToolRequest, suffix string) (string, *mcp.CallToolResult) {
		project, name, ok := strings.Cut(req.GetString("case_id", ""), "/")
		if !ok || project == "" || name == "" {
			return "", mcp.NewToolResultError(`case_id must be "<project>/<name>"`)
		}
		if run := req.GetString("run_id", ""); run != "" {
			return "/flows/runs/" + url.PathEscape(run) + suffix, nil
		}
		return "/flows/cases/" + url.PathEscape(project) + "/" + url.PathEscape(name) + "/active" + suffix, nil
	}
	step := func(action string, body func(req mcp.CallToolRequest) map[string]any) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path, bad := target(req, "/steps/"+url.PathEscape(req.GetString("step_id", ""))+"/"+action)
			if bad != nil {
				return bad, nil
			}
			var b []byte
			if body != nil {
				b, _ = json.Marshal(body(req))
			}
			return c.text(ctx, "POST", path, string(b))
		}
	}

	s.AddTool(mcp.NewTool("workflow_step_start",
		mcp.WithDescription("Flows: mark a case step as running. Steps must start in order and only one step runs at a time."),
		caseID, stepID, runID), step("start", nil))
	s.AddTool(mcp.NewTool("workflow_substep",
		mcp.WithDescription("Flows: record one meaningful action (substep) of the running step, in plain words."),
		caseID, stepID, runID,
		mcp.WithString("text", mcp.Required(), mcp.Description("What was just done"))),
		step("substep", func(req mcp.CallToolRequest) map[string]any {
			return map[string]any{"text": req.GetString("text", "")}
		}))
	s.AddTool(mcp.NewTool("workflow_assert",
		mcp.WithDescription("Flows: record the result of checking the step's expectation on the running step."),
		caseID, stepID, runID,
		mcp.WithString("expectation", mcp.Required(), mcp.Description("The expectation that was checked")),
		mcp.WithBoolean("passed", mcp.Required(), mcp.Description("Whether the expectation holds")),
		mcp.WithString("detail", mcp.Description("What was observed"))),
		step("assert", func(req mcp.CallToolRequest) map[string]any {
			return map[string]any{
				"expectation": req.GetString("expectation", ""),
				"passed":      req.GetBool("passed", false),
				"detail":      req.GetString("detail", ""),
			}
		}))
	s.AddTool(mcp.NewTool("workflow_step_done",
		mcp.WithDescription("Flows: finish the running step. passed=false fails the step and the whole run (remaining steps are skipped)."),
		caseID, stepID, runID,
		mcp.WithBoolean("passed", mcp.Required(), mcp.Description("Whether the step succeeded")),
		mcp.WithString("notes", mcp.Description("Result notes, or the reason for a failure"))),
		step("done", func(req mcp.CallToolRequest) map[string]any {
			return map[string]any{"passed": req.GetBool("passed", false), "notes": req.GetString("notes", "")}
		}))

	s.AddTool(mcp.NewTool("workflow_cache_script",
		mcp.WithDescription("Flows: submit the deterministic DSL that reproduces each step, after the last step passed. Segment ids equal step ids; each segment's steps are Pocketlab DSL step objects (as for execute_dsl). After a failure submit only the segments of passed steps."),
		caseID, runID,
		mcp.WithArray("segments", mcp.Required(), mcp.Description("Array of {id, steps} objects; steps is an array of DSL step objects"),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":    map[string]any{"type": "string", "description": "Step id"},
					"steps": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				},
				"required": []string{"id", "steps"},
			}))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path, bad := target(req, "/cache")
			if bad != nil {
				return bad, nil
			}
			segs, ok := req.GetArguments()["segments"]
			if !ok {
				return mcp.NewToolResultError("segments is required"), nil
			}
			b, err := json.Marshal(map[string]any{"segments": segs})
			if err != nil {
				return mcp.NewToolResultError("segments is not valid JSON: " + err.Error()), nil
			}
			return c.text(ctx, "POST", path, string(b))
		})

	s.AddTool(mcp.NewTool("workflow_get_case",
		mcp.WithDescription("Flows: read a case with its step ids, params, secret ids, cache staleness per step and the current run (if any). Call this first."),
		caseID),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			project, name, ok := strings.Cut(req.GetString("case_id", ""), "/")
			if !ok || project == "" || name == "" {
				return mcp.NewToolResultError(`case_id must be "<project>/<name>"`), nil
			}
			base := "/flows/cases/" + url.PathEscape(project) + "/" + url.PathEscape(name)
			res, err := c.text(ctx, "GET", base, "")
			if err != nil || res.IsError {
				return res, err
			}
			var entry struct {
				Case   json.RawMessage `json:"case"`
				Errors json.RawMessage `json:"errors"`
			}
			if json.Unmarshal([]byte(resultText(res)), &entry) != nil {
				return res, nil
			}
			out := map[string]any{"case_id": req.GetString("case_id", ""), "case": entry.Case}
			if len(entry.Errors) > 0 && string(entry.Errors) != "null" {
				out["errors"] = entry.Errors
			}
			if cr, _ := c.text(ctx, "GET", base+"/cache", ""); cr != nil && !cr.IsError {
				var cv struct {
					Staleness map[string]string `json:"staleness"`
				}
				if json.Unmarshal([]byte(resultText(cr)), &cv) == nil {
					out["staleness"] = cv.Staleness
				}
			}
			if rr, _ := c.text(ctx, "GET", "/flows/runs?limit=20&case="+url.QueryEscape(req.GetString("case_id", "")), ""); rr != nil && !rr.IsError {
				var runs []json.RawMessage
				if json.Unmarshal([]byte(resultText(rr)), &runs) == nil {
					for _, r := range runs {
						var h struct {
							Status string `json:"status"`
						}
						if json.Unmarshal(r, &h) == nil && h.Status == "running" {
							out["current_run"] = r
							break
						}
					}
				}
			}
			b, _ := json.Marshal(out)
			return mcp.NewToolResultText(string(b)), nil
		})

	s.AddTool(mcp.NewTool("workflow_list",
		mcp.WithDescription("Flows: list workflow cases with their cache state"),
		mcp.WithString("project", mcp.Description("Only cases of this test project"))),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path := "/flows/cases"
			if p := req.GetString("project", ""); p != "" {
				path += "?project=" + url.QueryEscape(p)
			}
			return c.text(ctx, "GET", path, "")
		})
}
