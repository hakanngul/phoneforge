package server

import (
	"errors"

	"pocketlab/internal/aiconfig"
	"pocketlab/internal/mobflow"
)

// flowLauncher adapts the aiconfig agent manager to mobflow.AgentLauncher: a
// workflow run launches a CLI agent in the Workflow settings slot.
type flowLauncher struct{ m *aiconfig.Manager }

func (l flowLauncher) Start(req mobflow.AgentRequest) (string, error) {
	id, err := l.m.StartRun(aiconfig.RunRequest{
		Agent: req.Agent, Type: aiconfig.TypeWorkflow, DeviceID: req.DeviceID,
		Prompt: req.Prompt, SystemPrompt: req.SystemPrompt, OnLine: req.OnLine,
		OnDone: func(_ string, err error) {
			if req.OnDone != nil {
				req.OnDone(err)
			}
		},
	})
	if errors.Is(err, aiconfig.ErrBuiltin) {
		return "", errors.New("workflow runs need a CLI agent (Claude Code, Codex, ...): pick one for Workflow in the AI settings, or start the run with agent \"external\"")
	}
	return id, err
}

func (l flowLauncher) Cancel(id string) { l.m.CancelRun(id) }
