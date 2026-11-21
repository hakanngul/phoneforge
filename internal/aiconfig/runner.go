package aiconfig

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Agent types.
const (
	TypeAutomation = "automation"
	TypeTesting    = "testing"
)

// RunRequest starts a run.
type RunRequest struct {
	Agent    string `json:"agent"` // empty = the agent selected for Type in settings
	Type     string `json:"type"`  // automation | testing
	DeviceID string `json:"deviceId"`
	Prompt   string `json:"prompt"`
	// SessionID resumes an earlier agent conversation (Claude Code: a session id;
	// kiro-cli: any non-empty value resumes the last conversation of its work dir).
	SessionID string `json:"sessionId,omitempty"`
}

// Event is one streamed run event. Type: start | session | stdout | stderr | error | done.
// A "session" event carries the agent session id for follow-up --resume runs.
type Event struct {
	Seq  int       `json:"seq"`
	Type string    `json:"type"`
	Data string    `json:"data"`
	Time time.Time `json:"time"`
}

type run struct {
	id     string
	agent  string
	cancel context.CancelFunc

	mu      sync.Mutex
	events  []Event
	done    bool
	changed chan struct{}
}

func (r *run) append(typ, data string) {
	r.mu.Lock()
	r.events = append(r.events, Event{Seq: len(r.events), Type: typ, Data: data, Time: time.Now()})
	if typ == "done" {
		r.done = true
	}
	ch := r.changed
	r.changed = make(chan struct{})
	r.mu.Unlock()
	close(ch)
}

// snapshot returns events from index `from`, whether the run finished, and a
// channel closed on the next change.
func (r *run) snapshot(from int) ([]Event, bool, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var evs []Event
	if from < len(r.events) {
		evs = append(evs, r.events[from:]...)
	}
	return evs, r.done, r.changed
}

// AllowedTools computes the --allowedTools entries for a Claude run: the whole
// phoneforge MCP server, always (agents are under the user's control).
func AllowedTools(_ Settings) []string {
	return []string{"mcp__" + MCPName}
}

// SystemPrompt builds the agent-type system prompt.
func SystemPrompt(typ, deviceID string, s Settings) string {
	var b strings.Builder
	switch typ {
	case TypeTesting:
		b.WriteString("You are the PhoneForge Testing agent. Write, run and fix .mob test scripts for mobile apps using the PhoneForge MCP tools (server \"" + MCPName + "\"). ")
		b.WriteString("Inspect the screen and UI tree before writing steps, keep scripts deterministic, run them on the device, and report pass/fail with the reason for any failure.")
	default:
		b.WriteString("You are the PhoneForge Automation agent. You operate a real mobile device through the PhoneForge MCP tools (server \"" + MCPName + "\"). ")
		b.WriteString("Observe the screen before every action, act in small verified steps, and stop when the task is complete or cannot be completed.")
	}
	if deviceID != "" {
		b.WriteString("\nControl the device with id " + deviceID + ". Do not touch other devices.")
	}
	if x := strings.TrimSpace(s.ExtraInstructions); x != "" {
		b.WriteString("\n\n" + x)
	}
	return b.String()
}

// buildCommand returns the CLI invocation (name args) for an agent.
// mcpConfig is the temp MCP config path (claude only).
func buildCommand(agent, bin, system, prompt, model, mcpConfig, sessionID string, s Settings) []string {
	switch agent {
	case AgentClaude:
		a := []string{bin, "-p", prompt, "--output-format", "stream-json", "--verbose",
			"--mcp-config", mcpConfig, "--strict-mcp-config", "--append-system-prompt", system}
		if model != "" {
			a = append(a, "--model", model)
		}
		if sessionID != "" {
			a = append(a, "--resume", sessionID)
		}
		if s.FullAccess {
			a = append(a, "--dangerously-skip-permissions")
		}
		a = append(a, "--allowedTools", strings.Join(AllowedTools(s), ","))
		return a
	case AgentCodex:
		// No known flag to ignore the user's other MCP servers (codex has no
		// strict-mcp equivalent we are sure of), so none is added.
		a := []string{bin, "exec"}
		if model != "" {
			a = append(a, "--model", model)
		}
		// --full-auto was removed in codex 0.16x. In non-interactive exec an MCP tool call
		// needs approval (policy is "never"), so approve the phoneforge server's tools
		// explicitly; the shell sandbox stays workspace-write unless full access is on.
		a = append(a, "--skip-git-repo-check",
			"-c", "mcp_servers."+MCPName+`.default_tools_approval_mode="approve"`)
		if s.FullAccess {
			a = append(a, "--dangerously-bypass-approvals-and-sandbox")
		} else {
			a = append(a, "--sandbox", "workspace-write")
		}
		return append(a, system+"\n\nTask: "+prompt)
	case AgentGemini:
		// No confident equivalent of --strict-mcp-config; left unrestricted.
		a := []string{bin}
		if model != "" {
			a = append(a, "--model", model)
		}
		a = append(a, "--yolo")
		return append(a, "-p", system+"\n\nTask: "+prompt)
	case AgentKiroCLI:
		// Plain-text output (the stream-json record schema is undocumented). Trust only the
		// phoneforge MCP server's tools (@server reference, kiro-cli >= 3.3.0), or everything
		// when full access is on. --require-mcp-startup makes the run wait for the MCP server.
		a := []string{bin, "chat", "--no-interactive", "--require-mcp-startup"}
		if s.FullAccess {
			a = append(a, "--trust-all-tools")
		} else {
			a = append(a, "--trust-tools=@"+MCPName)
		}
		if model != "" {
			a = append(a, "--model", model)
		}
		if sessionID != "" {
			a = append(a, "--resume")
		}
		return append(a, system+"\n\nTask: "+prompt)
	case AgentCursor:
		// No confident equivalent of --strict-mcp-config; left unrestricted.
		a := []string{bin, "-p", system + "\n\nTask: " + prompt, "--output-format", "stream-json"}
		if model != "" {
			a = append(a, "--model", model)
		}
		a = append(a, "--force")
		return a
	}
	return nil
}

// ErrBuiltin is returned by StartRun when the selected agent is the built-in
// one: the caller should route the request to internal/agent instead.
var ErrBuiltin = errors.New("builtin agent: route to internal/agent")

// StartRun launches a CLI agent run and returns its id. For the built-in agent
// it returns ErrBuiltin and an empty id.
func (m *Manager) StartRun(req RunRequest) (string, error) {
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		return "", errors.New("prompt is required")
	}
	if req.Type == "" {
		req.Type = TypeAutomation
	}
	if req.Type != TypeAutomation && req.Type != TypeTesting {
		return "", errors.New("type must be automation or testing")
	}
	s := m.Settings()
	ts := s.Automation
	if req.Type == TypeTesting {
		ts = s.Testing
	}
	agent := req.Agent
	if agent == "" {
		agent = ts.Agent
	}
	if agent == AgentBuiltin {
		return "", ErrBuiltin
	}
	spec, ok := specFor(agent)
	if !ok {
		return "", errUnknownAgent
	}
	if spec.NoRun {
		return "", fmt.Errorf("%s has no headless mode; use it through MCP only", spec.Label)
	}
	bin := m.findBinary(spec.Binary)
	if bin == "" {
		return "", fmt.Errorf("%s is not installed", spec.Label)
	}
	system := SystemPrompt(req.Type, req.DeviceID, s)

	var cfgPath string
	if agent == AgentClaude {
		cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
			MCPName: map[string]any{"type": "http", "url": m.mcpURL}}})
		f, err := os.CreateTemp(m.tmpDir, "phoneforge-mcp-*.json")
		if err != nil {
			return "", err
		}
		cfgPath = f.Name()
		_, werr := f.Write(cfg)
		f.Close()
		if werr != nil {
			os.Remove(cfgPath)
			return "", werr
		}
	}
	sid := ""
	var runEnv []string
	if agent == AgentClaude || agent == AgentKiroCLI {
		sid = strings.TrimSpace(req.SessionID)
	}
	if agent == AgentKiroCLI {
		// A dedicated work dir keeps --resume ("previous conversation from this directory")
		// scoped to PhoneForge, and its workspace MCP config makes runs independent of Connect.
		dir, err := m.prepareKiroDir()
		if err != nil {
			return "", err
		}
		runEnv = []string{runDirEnv + dir}
	}
	argv := buildCommand(agent, bin, system, req.Prompt, ResolveModel(agent, req.Type, ts.Model), cfgPath, sid, s)

	ctx, cancel := context.WithCancel(context.Background())
	r := &run{id: newID(), agent: agent, cancel: cancel, changed: make(chan struct{})}
	m.runMu.Lock()
	m.runs[r.id] = r
	m.runMu.Unlock()

	go func() {
		defer cancel()
		if cfgPath != "" {
			defer os.Remove(filepath.Clean(cfgPath))
		}
		r.append("start", agent)
		sessionSent := false
		err := m.exec.Stream(ctx, argv[0], argv[1:], runEnv, func(stream, line string) {
			if stream == "stdout" && agent == AgentClaude && !sessionSent {
				if id := sessionIDFromLine(line); id != "" {
					sessionSent = true
					r.append("session", id)
				}
			}
			if agent == AgentKiroCLI {
				line = CleanTerminalLine(line)
				if line == "" {
					return
				}
			}
			r.append(stream, line)
		})
		switch {
		case ctx.Err() != nil:
			r.append("done", "cancelled")
		case err != nil:
			r.append("error", err.Error())
			r.append("done", "failed")
		default:
			if agent == AgentKiroCLI {
				// No session id in plain output; a marker lets the UI request --resume next time.
				r.append("session", "last")
			}
			r.append("done", "ok")
		}
	}()
	return r.id, nil
}

// CancelRun cancels a run; it reports false for unknown ids.
func (m *Manager) CancelRun(id string) bool {
	r := m.getRun(id)
	if r == nil {
		return false
	}
	r.cancel()
	return true
}

func (m *Manager) getRun(id string) *run {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	return m.runs[id]
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sessionIDFromLine extracts session_id from a Claude stream-json line.
func sessionIDFromLine(line string) string {
	if !strings.Contains(line, "session_id") {
		return ""
	}
	var j struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(line), &j) != nil {
		return ""
	}
	return j.SessionID
}

// prepareKiroDir creates the dedicated kiro-cli work directory next to the settings file
// and writes a workspace-level .kiro/settings/mcp.json registering the phoneforge server.
func (m *Manager) prepareKiroDir() (string, error) {
	base := m.tmpDir
	if m.settingsPath != "" {
		base = filepath.Dir(m.settingsPath)
	}
	dir := filepath.Join(base, "kiro-cli-workdir")
	cfgDir := filepath.Join(dir, ".kiro", "settings")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return "", err
	}
	b, err := jsonSetServer(nil, map[string]any{"url": m.mcpURL})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "mcp.json"), b, 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

// ansiRe matches CSI and OSC terminal escape sequences.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][0-9A-Za-z]`)

// CleanTerminalLine strips ANSI escapes, keeps only the text after the last carriage
// return (spinner/progress redraws) and trims trailing space. Empty result = skip line.
func CleanTerminalLine(line string) string {
	if i := strings.LastIndex(line, "\r"); i >= 0 {
		line = line[i+1:]
	}
	line = ansiRe.ReplaceAllString(line, "")
	return strings.TrimRight(line, " \t\r")
}
