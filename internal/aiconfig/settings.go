package aiconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// TypeSettings selects the agent and model used for one agent type.
type TypeSettings struct {
	Agent string `json:"agent"`
	Model string `json:"model"`
}

// Settings is the persisted AI Configuration.
type Settings struct {
	Automation TypeSettings `json:"automation"`
	Testing    TypeSettings `json:"testing"`
	// Workflow is the slot of the Flows (.mobflow) recording agent. When a
	// settings file has none it starts as a copy of the Testing slot.
	Workflow          TypeSettings `json:"workflow"`
	ExtraInstructions string       `json:"extraInstructions"`
	MaxSteps          int          `json:"maxSteps"`
	// FullAccess runs Claude Code with --dangerously-skip-permissions so it can also use
	// its own non-Pocketlab tools (Bash, files) without prompts. Off by default.
	FullAccess bool `json:"fullAccess"`
	// Version is the settings schema version (see settingsVersion).
	Version int `json:"version"`
}

// settingsVersion 3: the old autoApprove/read-only mode is gone (agents always get all
// Pocketlab tools); a leftover "autoApprove" key in older files is ignored.
// Version 4 adds the Workflow slot (defaults to the Testing slot's agent and model).
const settingsVersion = 4

// DefaultClaudeModel is the model used for Claude automation runs when none is set.
const DefaultClaudeModel = "haiku"

// ResolveModel maps a stored model setting to the --model value ("" = omit the flag).
// For the Claude automation agent an empty setting means Haiku; "default" means the CLI default.
func ResolveModel(agent, typ, model string) string {
	model = strings.TrimSpace(model)
	if model == "default" {
		return ""
	}
	if model == "" && agent == AgentClaude && typ == TypeAutomation {
		return DefaultClaudeModel
	}
	return model
}

func defaultSettings() Settings {
	s := Settings{
		Automation: TypeSettings{Agent: AgentBuiltin},
		Testing:    TypeSettings{Agent: AgentBuiltin},
		MaxSteps:   50,
		Version:    settingsVersion,
	}
	// loadSettings overlays a file onto these defaults, so the Workflow slot is
	// left empty here and normalize() fills it from the (loaded) Testing slot.
	return s
}

func defaultNormalized() Settings {
	s := defaultSettings()
	_ = s.normalize()
	return s
}

func validAgent(n string) bool {
	if n == AgentBuiltin {
		return true
	}
	_, ok := specFor(n)
	return ok
}

func (s *Settings) normalize() error {
	if s.Automation.Agent == "" {
		s.Automation.Agent = AgentBuiltin
	}
	if s.Testing.Agent == "" {
		s.Testing.Agent = AgentBuiltin
	}
	// First migration only: an unset Workflow slot starts as the Testing slot.
	// A slot with an agent (or just a model) is the user's own and is kept.
	if s.Workflow.Agent == "" {
		if s.Workflow.Model == "" {
			s.Workflow = s.Testing
		} else {
			s.Workflow.Agent = s.Testing.Agent
		}
	}
	if !validAgent(s.Automation.Agent) || !validAgent(s.Testing.Agent) || !validAgent(s.Workflow.Agent) {
		return errors.New("unknown agent")
	}
	for _, a := range []string{s.Automation.Agent, s.Testing.Agent, s.Workflow.Agent} {
		if sp, _ := specFor(a); sp.NoRun {
			return errors.New(sp.Label + " has no headless mode; it can only be connected, not selected to run")
		}
	}
	s.Version = settingsVersion
	if s.MaxSteps <= 0 {
		s.MaxSteps = 50
	}
	if s.MaxSteps > 1000 {
		return errors.New("maxSteps too large (max 1000)")
	}
	if len(s.ExtraInstructions) > 16000 {
		return errors.New("extraInstructions too long")
	}
	return nil
}

func (m *Manager) loadSettings() Settings {
	s := defaultSettings()
	if m.settingsPath == "" {
		return defaultNormalized()
	}
	if b, err := os.ReadFile(m.settingsPath); err == nil {
		s.Version = 0
		_ = json.Unmarshal(b, &s)
		migrate := s.Version < settingsVersion
		if s.normalize() != nil {
			return defaultNormalized()
		}
		if migrate {
			m.writeSettingsFile(s)
		}
		return s
	}
	return defaultNormalized()
}

// Settings returns the current settings.
func (m *Manager) Settings() Settings {
	m.setMu.Lock()
	defer m.setMu.Unlock()
	return m.settings
}

// SetSettings validates, persists and applies new settings.
func (m *Manager) SetSettings(s Settings) error {
	if err := s.normalize(); err != nil {
		return err
	}
	m.setMu.Lock()
	defer m.setMu.Unlock()
	if err := m.writeSettingsFile(s); err != nil {
		return err
	}
	m.settings = s
	return nil
}

func (m *Manager) writeSettingsFile(s Settings) error {
	if m.settingsPath == "" {
		return nil
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.settingsPath), 0o755); err != nil {
		return err
	}
	tmp := m.settingsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.settingsPath)
}
