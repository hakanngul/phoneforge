package server

import (
	"path/filepath"
	"strings"
	"testing"

	"pocketlab/internal/aiconfig"
	"pocketlab/internal/mobflow"
)

func TestFlowLauncherBuiltinAgentGivesClearError(t *testing.T) {
	m := aiconfig.New(filepath.Join(t.TempDir(), "ai.json"), "")
	called := false
	_, err := flowLauncher{m}.Start(mobflow.AgentRequest{Prompt: "x", OnDone: func(error) { called = true }})
	if err == nil || !strings.Contains(err.Error(), "CLI agent") {
		t.Fatalf("want a hint to pick a CLI agent, got %v", err)
	}
	if called {
		t.Fatal("OnDone must not run when the launch fails")
	}
	flowLauncher{m}.Cancel("unknown") // must not panic
}
