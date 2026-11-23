package aiconfig

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// errExec is a fakeExec whose process fails after emitting its lines.
type errExec struct {
	fakeExec
	err error
}

func (e *errExec) Stream(ctx context.Context, name string, args []string, env []string, onLine func(string, string)) error {
	if err := e.fakeExec.Stream(ctx, name, args, env, onLine); err != nil {
		return err
	}
	return e.err
}

type doneCall struct {
	id  string
	err error
}

func TestWorkflowSettingsFallBackToTestingSlot(t *testing.T) {
	m := newTestMgr(t, &fakeExec{})
	// A version-3 file without a workflow slot.
	old := `{"automation":{"agent":"builtin","model":""},"testing":{"agent":"codex","model":"gpt-x"},"maxSteps":9,"version":3}`
	if err := os.WriteFile(m.settingsPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(m.settingsPath, "").Settings()
	if s.Workflow.Agent != "codex" || s.Workflow.Model != "gpt-x" || s.Testing.Agent != "codex" || s.MaxSteps != 9 || s.Version != settingsVersion {
		t.Fatalf("migration: %+v", s)
	}
	// The migrated file is persisted and keeps an own slot afterwards.
	m2 := New(m.settingsPath, "")
	s2 := m2.Settings()
	s2.Workflow = TypeSettings{Agent: "claude", Model: "sonnet"}
	if err := m2.SetSettings(s2); err != nil {
		t.Fatal(err)
	}
	got := New(m.settingsPath, "").Settings()
	if got.Workflow.Agent != "claude" || got.Workflow.Model != "sonnet" || got.Testing.Agent != "codex" {
		t.Fatalf("own slot: %+v", got)
	}
	// Defaults and an unknown workflow agent.
	if d := newTestMgr(t, &fakeExec{}).Settings(); d.Workflow.Agent != AgentBuiltin {
		t.Fatalf("default: %+v", d.Workflow)
	}
	bad := Settings{Workflow: TypeSettings{Agent: "bogus"}}
	if m.SetSettings(bad) == nil {
		t.Fatal("unknown workflow agent must be rejected")
	}
}

func TestStartRunWorkflowUsesOwnSlotAndPrompt(t *testing.T) {
	fe := &fakeExec{lines: []string{"line one", "line two"}}
	m := newTestMgr(t, fe, "claude", "codex")
	if err := m.SetSettings(Settings{
		Testing:  TypeSettings{Agent: "codex"},
		Workflow: TypeSettings{Agent: "claude", Model: "opus"},
	}); err != nil {
		t.Fatal(err)
	}
	var lines []string
	done := make(chan doneCall, 2)
	id, err := m.StartRun(RunRequest{
		Type: TypeWorkflow, DeviceID: "dev9", Prompt: "record it",
		SystemPrompt: "CUSTOM WORKFLOW RULES",
		OnLine:       func(_, l string) { lines = append(lines, l) },
		OnDone:       func(id string, err error) { done <- doneCall{id, err} },
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-done:
		if d.id != id || d.err != nil {
			t.Fatalf("OnDone: %+v", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnDone not called")
	}
	waitDone(t, m, id)
	args := strings.Join(fe.calls[0], "\x00")
	for _, w := range []string{"-p\x00record it", "--model\x00opus", "CUSTOM WORKFLOW RULES", "dev9"} {
		if !strings.Contains(args, w) {
			t.Fatalf("missing %q in %q", w, args)
		}
	}
	if strings.Contains(args, "Automation agent") {
		t.Fatalf("override must replace the built-in prompt: %q", args)
	}
	if len(lines) != 2 || lines[0] != "line one" {
		t.Fatalf("OnLine: %v", lines)
	}
	select {
	case d := <-done:
		t.Fatalf("OnDone called twice: %+v", d)
	case <-time.After(50 * time.Millisecond):
	}

	// Workflow with the built-in agent is routed away like the other types.
	if err := m.SetSettings(Settings{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartRun(RunRequest{Type: TypeWorkflow, Prompt: "x"}); !errors.Is(err, ErrBuiltin) {
		t.Fatalf("want ErrBuiltin, got %v", err)
	}
	if _, err := m.StartRun(RunRequest{Type: "bogus", Prompt: "x"}); err == nil {
		t.Fatal("bogus type must fail")
	}
}

func TestWorkflowDefaultPrompt(t *testing.T) {
	p := SystemPrompt(TypeWorkflow, "dev1", Settings{})
	if !strings.Contains(p, "Workflow agent") || !strings.Contains(p, "dev1") {
		t.Fatal(p)
	}
	if !strings.Contains(SystemPrompt(TypeTesting, "", Settings{}), "Testing agent") || !strings.Contains(SystemPrompt("", "", Settings{ExtraInstructions: "xx"}), "Automation agent") {
		t.Fatal("existing prompts changed")
	}
}

func TestOnDoneOnceOnErrorAndCancel(t *testing.T) {
	var n atomic.Int32
	done := make(chan doneCall, 4)
	hook := func(id string, err error) { n.Add(1); done <- doneCall{id, err} }

	ee := &errExec{err: errors.New("exit status 2")}
	m := newTestMgr(t, &fakeExec{}, "codex")
	m.exec = ee
	if _, err := m.StartRun(RunRequest{Agent: "codex", Type: TypeWorkflow, Prompt: "x", OnDone: hook}); err != nil {
		t.Fatal(err)
	}
	if d := <-done; d.err == nil || !strings.Contains(d.err.Error(), "exit status 2") {
		t.Fatalf("error case: %+v", d)
	}

	fe := &fakeExec{block: make(chan struct{})}
	m2 := newTestMgr(t, fe, "codex")
	id, err := m2.StartRun(RunRequest{Agent: "codex", Type: TypeWorkflow, Prompt: "x", OnDone: hook})
	if err != nil {
		t.Fatal(err)
	}
	if !m2.CancelRun(id) {
		t.Fatal("cancel")
	}
	if d := <-done; d.id != id || !errors.Is(d.err, context.Canceled) {
		t.Fatalf("cancel case: %+v", d)
	}
	time.Sleep(50 * time.Millisecond)
	if n.Load() != 2 {
		t.Fatalf("OnDone calls: %d", n.Load())
	}
}
