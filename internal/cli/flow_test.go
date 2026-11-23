package cli

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const flowOK = "version: 1\nname: t\nsteps:\n  - {id: a, type: act, do: tap}\n"

func TestFlowValidateFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.mobflow")
	bad := filepath.Join(dir, "bad.mobflow")
	if err := os.WriteFile(good, []byte(flowOK), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(strings.Replace(flowOK, "do: tap", "do: 'tap ${x}'", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run(t, nil, "", "flow", "validate", good); code != 0 || !strings.Contains(out, "ok") {
		t.Errorf("good: %d %q", code, out)
	}
	code, _, errOut := run(t, nil, "", "flow", "validate", bad)
	if code != 1 || !strings.Contains(errOut, "undeclared parameter ${x}") {
		t.Errorf("bad: %d %q", code, errOut)
	}
	if code, _, _ := run(t, nil, "", "flow", "validate", filepath.Join(dir, "missing.mobflow")); code != 2 {
		t.Errorf("missing file code=%d", code)
	}
	if code, _, _ := run(t, nil, "", "flow", "validate"); code != 2 {
		t.Errorf("no args code=%d", code)
	}
}

func TestFlowListAndRemoteValidate(t *testing.T) {
	f := newFake(t)
	f.json("GET /flows/cases", `[{"id":"p/c","title":"Checkout","steps":2,"valid":true,"cache":{"present":true,"fresh":1,"stale":1,"missing":0}}]`)
	code, out, errOut := run(t, f, "", "flow", "list", "--project", "p")
	if code != 0 || !strings.Contains(out, "p/c") || !strings.Contains(out, "fresh 1, stale 1") {
		t.Fatalf("list: %d %q %q", code, out, errOut)
	}
	if r, _ := f.last("GET", "/flows/cases"); r.Query != "project=p" {
		t.Errorf("query=%q", r.Query)
	}
	if code, out, _ := run(t, f, "", "flow", "list", "--json"); code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("json: %d %q", code, out)
	}
	f.h["GET /flows/cases/p/bad"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"errors":[{"step":"a","field":"do","message":"is required"}]}`)
	}
	f.json("GET /flows/cases/p/good", `{"id":"p/good"}`)
	if code, _, errOut := run(t, f, "", "flow", "validate", "p/bad"); code != 1 || !strings.Contains(errOut, "step a: do: is required") {
		t.Errorf("remote bad: %d %q", code, errOut)
	}
	if code, _, _ := run(t, f, "", "flow", "validate", "p/good"); code != 0 {
		t.Errorf("remote good: %d", code)
	}
	if code, _, _ := run(t, nil, "", "flow", "list"); code == 0 {
		t.Error("unreachable server should fail")
	}
}

const runStart = `{"run":{"id":"r1","caseId":"p/c","mode":"replay","status":"running","steps":[]}}`

func runBody(status, errText string, steps string) string {
	return fmt.Sprintf(`{"id":"r1","caseId":"p/c","deviceId":"android-AAA","mode":"replay","status":%q,"error":%q,"startedAt":"2026-01-02T03:04:05Z","healed":[],"steps":%s}`, status, errText, steps)
}

func fastPoll(t *testing.T) {
	t.Helper()
	old := flowPollInterval
	flowPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { flowPollInterval = old })
}

func TestFlowReplayPassed(t *testing.T) {
	fastPoll(t)
	f := newFake(t)
	f.json("POST /flows/runs", runStart)
	polls := 0
	f.h["GET /flows/runs/r1"] = func(w http.ResponseWriter, _ *http.Request) {
		polls++
		w.Header().Set("Content-Type", "application/json")
		if polls == 1 {
			fmt.Fprint(w, runBody("running", "", `[{"id":"s1","title":"Sign in","state":"passed","durationMs":1200},{"id":"s2","title":"Add","state":"running"}]`))
			return
		}
		fmt.Fprint(w, runBody("passed", "", `[{"id":"s1","title":"Sign in","state":"passed","durationMs":1200},{"id":"s2","title":"Add","state":"healed","durationMs":300}]`))
	}
	code, out, errOut := run(t, f, "", "flow", "replay", "p/c", "-d", "android-AAA", "--heal", "--param", "email=a=b", "--param", "item=Mug")
	if code != 0 {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	for _, want := range []string{"PASS   s1 Sign in (1.2s)", "HEALED s2 Add", "Result: passed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	r, _ := f.last("POST", "/flows/runs")
	var body map[string]any
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
		t.Fatal(err)
	}
	params, _ := body["params"].(map[string]any)
	if body["mode"] != "replay" || body["heal"] != true || body["project"] != "p" || body["case"] != "c" ||
		body["deviceId"] != "android-AAA" || params["email"] != "a=b" || params["item"] != "Mug" {
		t.Errorf("request body: %s", r.Body)
	}
}

func TestFlowRunFailedExitCodeAndJUnit(t *testing.T) {
	fastPoll(t)
	f := newFake(t)
	f.json("POST /flows/runs", runStart)
	f.json("GET /flows/runs/r1", runBody("failed", "step s2 failed: DSL step 1 (tap) failed: <no match> & more",
		`[{"id":"s1","title":"Sign in","state":"passed","durationMs":1000},
		  {"id":"s2","title":"Add","state":"failed","durationMs":250,"error":"DSL step 1 (tap) failed: <no match> & more","screenshot":"step-s2.png"},
		  {"id":"s3","title":"Cart","state":"skipped"}]`))
	f.h["GET /flows/runs/r1/artifacts/step-s2.png"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG-shot"))
	}
	report := filepath.Join(t.TempDir(), "out", "flow.xml")
	code, out, errOut := run(t, f, "", "flow", "run", "p/c", "-d", "android-AAA", "--mode", "replay", "--junit", report)
	if code != 1 {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, "FAIL   s2 Add") || !strings.Contains(out, "SKIP   s3") || !strings.Contains(out, "Result: failed") {
		t.Errorf("output:\n%s", out)
	}
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var doc junitSuites
	if err := xml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, b)
	}
	if len(doc.Suites) != 1 {
		t.Fatalf("suites: %s", b)
	}
	su := doc.Suites[0]
	if su.Name != "p/c" || su.Tests != 3 || su.Failures != 1 || su.Skipped != 1 {
		t.Fatalf("suite: %+v", su)
	}
	fc := su.Cases[1]
	shot := strings.TrimSuffix(report, ".xml") + ".screenshots/step-s2.png"
	if fc.Failure == nil || !strings.Contains(fc.Failure.Message, "<no match> & more") || !strings.Contains(fc.Failure.Text, shot) {
		t.Fatalf("failure: %+v", fc.Failure)
	}
	if data, err := os.ReadFile(shot); err != nil || string(data) != "\x89PNG-shot" {
		t.Fatalf("screenshot file: %v %q", err, data)
	}
	if su.Cases[0].Failure != nil || su.Cases[2].Skipped == nil {
		t.Fatalf("cases: %+v", su.Cases)
	}
}

func TestFlowJUnitRunLevelFailure(t *testing.T) {
	fastPoll(t)
	f := newFake(t)
	f.json("POST /flows/runs", runStart)
	f.json("GET /flows/runs/r1", runBody("failed", "stale segments: s2; record or heal needed",
		`[{"id":"s1","title":"Sign in","state":"skipped"},{"id":"s2","title":"Add","state":"skipped"}]`))
	report := filepath.Join(t.TempDir(), "flow.xml")
	if code, _, _ := run(t, f, "", "flow", "replay", "p/c", "-d", "android-AAA", "--junit", report); code != 1 {
		t.Fatalf("code=%d", code)
	}
	b, _ := os.ReadFile(report)
	var doc junitSuites
	if err := xml.Unmarshal(b, &doc); err != nil || doc.Suites[0].Failures != 1 || !strings.Contains(string(b), "stale segments: s2") {
		t.Fatalf("junit: %v\n%s", err, b)
	}
}

func TestFlowRunJSONAndErrors(t *testing.T) {
	fastPoll(t)
	f := newFake(t)
	f.json("POST /flows/runs", runStart)
	f.json("GET /flows/runs/r1", runBody("passed", "", `[]`))
	code, out, _ := run(t, f, "", "flow", "replay", "p/c", "-d", "android-AAA", "--json")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var got struct{ Status string }
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Status != "passed" {
		t.Fatalf("json out: %v %q", err, out)
	}

	// server errors (busy device, missing case) are exit 2
	f.h["POST /flows/runs"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		fmt.Fprint(w, `{"error":"device busy","code":"device_busy"}`)
	}
	if code, _, errOut := run(t, f, "", "flow", "run", "p/c", "-d", "android-AAA"); code != 2 || !strings.Contains(errOut, "device busy") {
		t.Errorf("busy: %d %q", code, errOut)
	}
	// usage problems are exit 2 too
	for _, args := range [][]string{
		{"flow", "run"},
		{"flow", "run", "nocase", "-d", "android-AAA"},
		{"flow", "run", "p/c", "-d", "android-AAA", "--mode", "bogus"},
		{"flow", "run", "p/c", "-d", "android-AAA", "--mode", "record", "--heal"},
		{"flow", "run", "p/c", "-d", "android-AAA", "--param", "novalue"},
	} {
		if code, _, _ := run(t, f, "", args...); code != 2 {
			t.Errorf("%v: code=%d", args, code)
		}
	}
	// an unreachable server keeps its own code
	if code, _, _ := run(t, nil, "", "flow", "replay", "p/c", "-d", "x"); code != ExitUnreachable {
		t.Errorf("unreachable: %d", code)
	}
}

func TestFlowRunTimeoutCancelsRun(t *testing.T) {
	fastPoll(t)
	f := newFake(t)
	f.json("POST /flows/runs", runStart)
	f.json("GET /flows/runs/r1", runBody("running", "", `[]`))
	f.json("POST /flows/runs/r1/cancel", runBody("cancelled", "cancelled", `[{"id":"s1","title":"Sign in","state":"skipped"}]`))
	code, out, _ := run(t, f, "", "flow", "replay", "p/c", "-d", "android-AAA", "--timeout", "150ms")
	if code != 1 || !strings.Contains(out, "cancelled") || !strings.Contains(out, "timed out") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if _, ok := f.last("POST", "/flows/runs/r1/cancel"); !ok {
		t.Fatal("run was not cancelled on the server")
	}
}

func TestFlowRecordSendsModeAndAgent(t *testing.T) {
	fastPoll(t)
	f := newFake(t)
	f.json("POST /flows/runs", runStart)
	f.json("GET /flows/runs/r1", runBody("passed", "", `[]`))
	if code, _, errOut := run(t, f, "", "flow", "record", "p/c", "-d", "android-AAA", "--agent", "claude"); code != 0 {
		t.Fatalf("code=%d %s", code, errOut)
	}
	r, _ := f.last("POST", "/flows/runs")
	if !strings.Contains(r.Body, `"mode":"record"`) || !strings.Contains(r.Body, `"agent":"claude"`) {
		t.Errorf("body %s", r.Body)
	}
}

func TestFlowCacheCommands(t *testing.T) {
	f := newFake(t)
	f.json("GET /flows/cases/p/c/cache", `{"cache":{"version":3,"updatedAt":"2026-01-02T03:04:05Z","segments":[{"id":"s1","steps":[{},{}],"healedAt":"2026-01-02T03:04:05Z"}]},"staleness":{"s1":"fresh","s2":"missing"}}`)
	code, out, errOut := run(t, f, "", "flow", "cache", "p/c")
	if code != 0 || !strings.Contains(out, "cache v3") || !strings.Contains(out, "missing") || !strings.Contains(out, "fresh") {
		t.Fatalf("show: %d %q %q", code, out, errOut)
	}
	f.json("GET /flows/cases/p/c/cache/history", `[{"id":"20260102T030405.000-v2","version":2,"archivedAt":"2026-01-02T03:04:05Z"}]`)
	if code, out, _ := run(t, f, "", "flow", "cache", "p/c", "history"); code != 0 || !strings.Contains(out, "20260102T030405.000-v2") {
		t.Fatalf("history: %d %q", code, out)
	}
	f.json("POST /flows/cases/p/c/cache/history/20260102T030405.000-v2/restore", `{"cache":{"version":4,"updatedAt":"2026-01-02T03:04:05Z","segments":[]},"staleness":{"s1":"fresh"}}`)
	if code, out, _ := run(t, f, "", "flow", "cache", "p/c", "restore", "20260102T030405.000-v2"); code != 0 || !strings.Contains(out, "cache v4") {
		t.Fatalf("restore: %d %q", code, out)
	}
	f.h["DELETE /flows/cases/p/c/cache"] = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }
	if code, out, _ := run(t, f, "", "flow", "cache", "p/c", "delete", "--platform", "ios"); code != 0 || !strings.Contains(out, "deleted") {
		t.Fatalf("delete: %d %q", code, out)
	}
	if r, _ := f.last("DELETE", "/flows/cases/p/c/cache"); r.Query != "platform=ios" {
		t.Errorf("query %q", r.Query)
	}
	for _, args := range [][]string{{"flow", "cache"}, {"flow", "cache", "p/c", "bogus"}, {"flow", "cache", "p/c", "restore"}} {
		if code, _, _ := run(t, f, "", args...); code != 2 {
			t.Errorf("%v: code=%d", args, code)
		}
	}
}

func TestFlowValidatePrintsLintWarnings(t *testing.T) {
	f := newFake(t)
	f.json("GET /flows/cases/p/good", `{"id":"p/good","errors":[]}`)
	f.json("GET /flows/cases/p/good/lint", `{"ok":false,"warnings":[{"step":"s1","path":"0","message":"tap on raw coordinates (1,2)"}]}`)
	code, out, errOut := run(t, f, "", "flow", "validate", "p/good")
	if code != 0 || !strings.Contains(out, "ok") || !strings.Contains(errOut, "warning: step s1 [0]: tap on raw coordinates") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	code, out, _ = run(t, f, "", "flow", "validate", "p/good", "--json")
	if code != 0 || !strings.Contains(out, `"warnings":[{"step":"s1"`) {
		t.Fatalf("json: %d %q", code, out)
	}
}
