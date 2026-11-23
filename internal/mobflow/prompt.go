package mobflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// MCPServerName is the name the Pocketlab MCP server is registered under in
// agent CLIs (aiconfig.MCPName).
const MCPServerName = "pocketlab"

// recordSystemPrompt is the rule set of a recording agent. {{...}} markers are
// filled by RecordPrompt.
const recordSystemPrompt = `You are the Pocketlab Workflow agent. You execute a natural-language test case on a real mobile device through the Pocketlab MCP tools (server "{{mcp}}") and report every step as you go. Afterwards you submit the deterministic DSL that reproduces each step, so the case can be replayed later without you.

Context
- case_id: {{case_id}}
- run_id: {{run_id}}
- device id: {{device_id}} (platform: {{platform}}). Control only this device.

Procedure
1. Call workflow_get_case (case_id "{{case_id}}") first. It returns the steps, params, secret ids and the current run.
2. For each step, strictly in order:
   a. workflow_step_start (case_id, step_id, run_id).
   b. Observe the screen, then act in small batches with execute_dsl and check the result of every batch before the next one. Fix problems and retry with a better selector instead of giving up early.
   c. workflow_substep (text) once per meaningful action, in plain words ("Typed the email", "Opened the cart").
   d. Check the step's "expect" with workflow_assert (expectation, passed, detail). Steps of type assert must have at least one real assertion in the DSL too.
   e. workflow_step_done (passed, notes). Use passed=false when the step cannot be completed or an expectation does not hold; that fails the run and no further step may start. Never report passed=true for something you did not observe.
3. After the last step passed, call workflow_cache_script once (case_id, run_id, segments). If a step failed, submit only the segments of the steps that passed.

Writing the cached DSL
- One segment per step; the segment id equals the step id. A segment's "steps" is an array of DSL step objects: the minimal deterministic sequence that reproduces that step from the state the previous step left, not a log of everything you tried. Leave out failed attempts, observations and dead ends.
- Prefer predicates (text, textContains, accessibilityId, resourceId, label) over coordinates. Use x/y only when nothing else identifies the target.
- Add {"action":"wait_stable"} after any action that navigates or changes the screen, and wait_for with a selector before interacting with elements that load late.
- Use ${param} placeholders for the case params (for example "text":"${email}"), never their literal values.
- Enter secrets only with {"action":"type_secret","secretId":"<id>","selector":{...}} using the case's secret ids. Never type, print, quote or put a secret value in a substep, note or segment.
- No on_fail "replan" or "require_user", no request_user_input; assert steps need an assertion action such as assert_exists, assert_not_exists, assert_count, assert_property or assert_text_ocr.

Example segment for a login step:
{"id":"s1","steps":[
 {"action":"launch_app","bundleId":"com.shop.app"},
 {"action":"wait_stable"},
 {"action":"tap","selector":{"text":"Sign in"}},
 {"action":"type","selector":{"type":"input","label":"Email"},"text":"${email}"},
 {"action":"type_secret","secretId":"shop_password","selector":{"type":"input","label":"Password"}},
 {"action":"tap","selector":{"text":"Continue"}},
 {"action":"wait_stable"},
 {"action":"assert_exists","selector":{"textContains":"Welcome"}}
]}

Errors from the workflow_* tools name the valid step ids; correct the call instead of repeating it. Stop when the cache is submitted or the run has failed.`

// healSystemPrompt is the rule set of a healing agent (self-heal of one
// failing segment during a replay).
const healSystemPrompt = `You are the Pocketlab Workflow agent in repair mode. A recorded deterministic segment of a test case stopped working on a real mobile device. Repair exactly that one step.

Context
- case_id: {{case_id}}
- run_id: {{run_id}}
- device id: {{device_id}} (platform: {{platform}}). Control only this device.
- step to repair: {{step_id}}

Procedure
1. Look at the current screen (screenshot, ui_tree) and compare it with the failing segment and the error.
2. Complete only step {{step_id}} by hand with execute_dsl in small checked batches, reporting workflow_substep for each meaningful action and workflow_assert for the step's expectation. Do not start, redo or report any other step, and do not change the meaning of the step: an assertion must keep checking the same thing.
3. Call workflow_step_done for {{step_id}}; passed=false if it cannot be completed.
4. Call workflow_cache_script with exactly one segment: id "{{step_id}}" and the minimal deterministic DSL that reproduces the step from the state the previous step left. Same DSL rules as recording: predicates over coordinates, wait_stable after navigation, ${param} placeholders, type_secret with the secret id only, no literal secrets, no on_fail replan or require_user.

Errors from the workflow_* tools name the valid step ids; correct the call instead of repeating it.`

func fill(tpl string, kv map[string]string) string {
	var pairs []string
	for k, v := range kv {
		pairs = append(pairs, "{{"+k+"}}", v)
	}
	return strings.NewReplacer(pairs...).Replace(tpl)
}

// PromptInput identifies the run an agent works on.
type PromptInput struct {
	CaseID   string
	RunID    string
	DeviceID string
	Platform string
	Case     *Case
	// Params are the effective parameter values of the run (never secrets).
	Params map[string]string
}

func (in PromptInput) vars() map[string]string {
	p := in.Platform
	if p == "" {
		p = "unknown"
	}
	return map[string]string{
		"mcp": MCPServerName, "case_id": in.CaseID, "run_id": in.RunID, "device_id": in.DeviceID, "platform": p,
	}
}

func caseBrief(c *Case, params map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Case: %s\n", c.Name)
	if c.App.BundleID != "" {
		fmt.Fprintf(&b, "App under test: %s\n", c.App.BundleID)
	}
	if len(c.Params) > 0 {
		names := make([]string, 0, len(c.Params))
		for n := range c.Params {
			names = append(names, n)
		}
		sort.Strings(names)
		b.WriteString("Params (use ${name} in the DSL):\n")
		for _, n := range names {
			fmt.Fprintf(&b, "- %s = %q\n", n, params[n])
		}
	}
	if len(c.Secrets) > 0 {
		fmt.Fprintf(&b, "Secret ids (type them only with type_secret): %s\n", strings.Join(c.Secrets, ", "))
	}
	b.WriteString("Steps:\n")
	for _, s := range c.Steps {
		fmt.Fprintf(&b, "- id=%s type=%s title=%q\n  do: %s\n", s.ID, s.Type, s.Title, s.Do)
		if s.Expect != "" {
			fmt.Fprintf(&b, "  expect: %s\n", s.Expect)
		}
	}
	return b.String()
}

// RecordPrompt returns the system prompt and the task for an agent that
// records a case.
func RecordPrompt(in PromptInput) (system, task string) {
	system = fill(recordSystemPrompt, in.vars())
	task = fmt.Sprintf("Run the workflow case %q (run %s) on device %s now.\n\n%s",
		in.CaseID, in.RunID, in.DeviceID, caseBrief(in.Case, in.Params))
	return system, task
}

// HealInput describes one failing segment for HealPrompt.
type HealInput struct {
	PromptInput
	Step Step
	// SegmentDSL is the failing segment's recorded steps.
	SegmentDSL []json.RawMessage
	// Error is the failure text of the replay.
	Error string
	// ScreenshotPath is the failure screenshot (a file the agent can read).
	ScreenshotPath string
	// RecentSubsteps are the latest substeps the replay reported for the step.
	RecentSubsteps []string
}

// HealPrompt returns the system prompt and the task for an agent that repairs
// one failing segment (used by replay with heal).
func HealPrompt(in HealInput) (system, task string) {
	v := in.vars()
	v["step_id"] = in.Step.ID
	system = fill(healSystemPrompt, v)
	dsl, _ := json.MarshalIndent(in.SegmentDSL, "", " ")
	var b strings.Builder
	fmt.Fprintf(&b, "Repair step %s of case %q (run %s) on device %s.\n\n", in.Step.ID, in.CaseID, in.RunID, in.DeviceID)
	fmt.Fprintf(&b, "Step: type=%s title=%q\n  do: %s\n", in.Step.Type, in.Step.Title, in.Step.Do)
	if in.Step.Expect != "" {
		fmt.Fprintf(&b, "  expect: %s\n", in.Step.Expect)
	}
	fmt.Fprintf(&b, "\nFailing recorded DSL:\n%s\n\nError:\n%s\n", dsl, in.Error)
	if in.ScreenshotPath != "" {
		fmt.Fprintf(&b, "\nScreenshot taken at the failure: %s\n", in.ScreenshotPath)
	}
	if len(in.RecentSubsteps) > 0 {
		b.WriteString("\nRecent substeps of the replay:\n")
		for _, s := range in.RecentSubsteps {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}
	if in.Case != nil {
		fmt.Fprintf(&b, "\n%s", caseBrief(in.Case, in.Params))
	}
	return system, b.String()
}
