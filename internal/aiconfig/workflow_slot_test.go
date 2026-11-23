package aiconfig

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkflowSlotSurvivesSaveWithoutWorkflowKey(t *testing.T) {
	m := newTestMgr(t, &fakeExec{})
	srv := httptest.NewServer(m.Routes())
	defer srv.Close()
	put := func(body string) Settings {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/settings", strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var s Settings
		if resp.StatusCode != 200 {
			t.Fatalf("status %d", resp.StatusCode)
		}
		_ = json.NewDecoder(resp.Body).Decode(&s)
		return s
	}
	// An explicit workflow slot is stored.
	s := put(`{"automation":{"agent":"builtin"},"testing":{"agent":"builtin"},"workflow":{"agent":"claude","model":"opus"},"maxSteps":50}`)
	if s.Workflow.Agent != "claude" || s.Workflow.Model != "opus" {
		t.Fatalf("explicit: %+v", s.Workflow)
	}
	// A client that omits "workflow" (older UI) must not reset it to Testing.
	s = put(`{"automation":{"agent":"builtin"},"testing":{"agent":"codex","model":"gpt-x"},"maxSteps":50}`)
	if s.Workflow.Agent != "claude" || s.Workflow.Model != "opus" || s.Testing.Agent != "codex" {
		t.Fatalf("omitted workflow overwritten: %+v", s)
	}
	if got := m.Settings(); got.Workflow.Agent != "claude" {
		t.Fatalf("stored: %+v", got.Workflow)
	}
	// An explicit model-only slot keeps its model and takes the Testing agent.
	var in Settings
	in.Testing = TypeSettings{Agent: "codex"}
	in.Workflow = TypeSettings{Model: "m1"}
	if err := in.normalize(); err != nil || in.Workflow.Agent != "codex" || in.Workflow.Model != "m1" {
		t.Fatalf("model-only: %+v %v", in.Workflow, err)
	}
}
