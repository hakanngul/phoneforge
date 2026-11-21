// Package aiconfig implements PhoneForge's "AI Configuration": detection of
// external coding-agent CLIs, registration of PhoneForge's MCP server in them,
// persisted agent settings, and a runner that executes a CLI agent
// non-interactively and streams its output.
package aiconfig

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"phoneforge/internal/agent/llm"
	"strings"
	"sync"
	"time"
)

// MCPName is the name PhoneForge's MCP server is registered under.
const MCPName = "phoneforge"

// DefaultMCPURL is the default MCP endpoint.
const DefaultMCPURL = "http://127.0.0.1:8700/mcp"

// Agent identifiers.
const (
	AgentBuiltin = "builtin"
	AgentClaude  = "claude"
	AgentCodex   = "codex"
	AgentGemini  = "gemini"
	AgentCursor  = "cursor"
	AgentKiro    = "kiro"     // Kiro IDE (connect only, no headless runs)
	AgentKiroCLI = "kiro-cli" // Kiro terminal agent
)

// runDirEnv is a reserved env entry understood by osExec.Stream: its value is
// the working directory for the child (not passed to the child).
const runDirEnv = "PHONEFORGE_RUN_DIR="

// agentSpec describes one external CLI.
type agentSpec struct {
	Name   string
	Label  string
	Binary string
	// App is a macOS app bundle name (IDE agents), searched in /Applications and ~/Applications.
	App string
	// NoRun marks agents that cannot run headless (IDEs): Connect only.
	NoRun       bool
	InstallHint string
	InstallURL  string
}

var externalAgents = []agentSpec{
	{Name: AgentClaude, Label: "Claude Code", Binary: "claude"},
	{Name: AgentCodex, Label: "OpenAI Codex CLI", Binary: "codex"},
	{Name: AgentGemini, Label: "Gemini CLI", Binary: "gemini"},
	{Name: AgentCursor, Label: "Cursor Agent", Binary: "cursor-agent"},
	{Name: AgentKiro, Label: "Kiro (IDE)", Binary: "kiro", App: "Kiro.app", NoRun: true,
		InstallHint: "Download the Kiro IDE", InstallURL: "https://kiro.dev/downloads/"},
	{Name: AgentKiroCLI, Label: "Kiro CLI", Binary: "kiro-cli",
		InstallHint: "curl -fsSL https://cli.kiro.dev/install | bash", InstallURL: "https://kiro.dev/docs/getting-started/installation/"},
}

func specFor(name string) (agentSpec, bool) {
	for _, a := range externalAgents {
		if a.Name == name {
			return a, true
		}
	}
	return agentSpec{}, false
}

// Exec abstracts process execution so tests never spawn real CLIs.
type Exec interface {
	// Output runs a command and returns combined stdout+stderr.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	// Stream runs a command, calling onLine for each stdout/stderr line
	// (stream is "stdout" or "stderr"). It returns when the process exits.
	Stream(ctx context.Context, name string, args []string, env []string, onLine func(stream, line string)) error
}

type osExec struct{}

func (osExec) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (osExec) Stream(ctx context.Context, name string, args []string, env []string, onLine func(string, string)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	for _, e := range env {
		if dir, ok := strings.CutPrefix(e, runDirEnv); ok {
			cmd.Dir = dir
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	cmd.WaitDelay = 3 * time.Second
	so, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	se, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	pump := func(r interface{ Read([]byte) (int, error) }, stream string) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
		for sc.Scan() {
			onLine(stream, sc.Text())
		}
	}
	wg.Add(2)
	go pump(so, "stdout")
	go pump(se, "stderr")
	wg.Wait()
	return cmd.Wait()
}

// Manager is the AI Configuration service.
type Manager struct {
	settingsPath string
	mcpURL       string
	home         string
	exec         Exec
	lookPath     func(string) (string, error)
	tmpDir       string
	dirs         []string // extra install dirs searched after PATH
	appDirs      []string // macOS app dirs searched for IDE agents (default /Applications, ~/Applications)

	setMu    sync.Mutex
	settings Settings

	runMu sync.Mutex
	runs  map[string]*run

	cacheMu  sync.Mutex
	cache    []AgentStatus
	cacheAt  time.Time
	cacheTTL time.Duration

	// builtinProvider reports the LLM provider the built-in agent would use
	// ("anthropic", "ollama" or ""); ollamaHost/ollamaModels are test seams.
	builtinProvider func() string
	ollamaHost      func() string
	ollamaModels    func(ctx context.Context, host string) ([]string, error)
}

const detectCacheTTL = 30 * time.Second

func (m *Manager) invalidateDetect() {
	m.cacheMu.Lock()
	m.cache = nil
	m.cacheMu.Unlock()
}

// DetectCached is Detect with a short-lived cache; refresh bypasses it.
func (m *Manager) DetectCached(ctx context.Context, refresh bool) []AgentStatus {
	ttl := m.cacheTTL
	if ttl == 0 {
		ttl = detectCacheTTL
	}
	if !refresh {
		m.cacheMu.Lock()
		if m.cache != nil && time.Since(m.cacheAt) < ttl {
			c := append([]AgentStatus(nil), m.cache...)
			m.cacheMu.Unlock()
			return c
		}
		m.cacheMu.Unlock()
	}
	res := m.Detect(ctx)
	m.cacheMu.Lock()
	m.cache = append([]AgentStatus(nil), res...)
	m.cacheAt = time.Now()
	m.cacheMu.Unlock()
	return res
}

// New creates a Manager. settingsPath is the JSON settings file (created on
// first save); mcpURL defaults to DefaultMCPURL when empty. Nothing is
// executed or modified at construction time.
func New(settingsPath, mcpURL string) *Manager {
	if mcpURL == "" {
		mcpURL = DefaultMCPURL
	}
	home, _ := os.UserHomeDir()
	m := &Manager{
		settingsPath: settingsPath,
		mcpURL:       mcpURL,
		home:         home,
		exec:         osExec{},
		lookPath:     exec.LookPath,
		tmpDir:       os.TempDir(),
		runs:         map[string]*run{},

		builtinProvider: func() string { _, p, _ := llm.FromEnvModel(""); return p },
		ollamaHost:      func() string { return llm.OllamaHost(os.Getenv) },
		ollamaModels:    llm.ListOllamaModels,
	}
	m.settings = m.loadSettings()
	return m
}

// AgentStatus is the detection result for one agent.
type AgentStatus struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	Builtin    bool   `json:"builtin,omitempty"`
	Installed  bool   `json:"installed"`
	Path       string `json:"path,omitempty"`
	Version    string `json:"version,omitempty"`
	Connected  bool   `json:"connected"`
	Scope      string `json:"scope,omitempty"` // claude: user | local | project
	ConfigPath string `json:"configPath,omitempty"`
	Error      string `json:"error,omitempty"`
	// IDE agents can only be connected, never run headless.
	IDE         bool   `json:"ide,omitempty"`
	InstallHint string `json:"installHint,omitempty"`
	InstallURL  string `json:"installUrl,omitempty"`
}

func (m *Manager) searchAppDirs() []string {
	if m.appDirs != nil {
		return m.appDirs
	}
	return []string{"/Applications", filepath.Join(m.home, "Applications")}
}

// findApp returns the path of a macOS app bundle, or "".
func (m *Manager) findApp(app string) string {
	if app == "" {
		return ""
	}
	for _, d := range m.searchAppDirs() {
		p := filepath.Join(d, app)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}

func (m *Manager) searchDirs() []string {
	if m.dirs != nil {
		return m.dirs
	}
	return []string{
		filepath.Join(m.home, ".local", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
		filepath.Join(m.home, ".npm-global", "bin"),
	}
}

func (m *Manager) findBinary(bin string) string {
	if p, err := m.lookPath(bin); err == nil && p != "" {
		return p
	}
	for _, d := range m.searchDirs() {
		p := filepath.Join(d, bin)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// Detect reports every agent's install and MCP-registration state (read-only).
func (m *Manager) Detect(ctx context.Context) []AgentStatus {
	out := []AgentStatus{{Name: AgentBuiltin, Label: "Built-in agent", Builtin: true, Installed: true, Connected: true}}
	res := make([]AgentStatus, len(externalAgents))
	var wg sync.WaitGroup
	for i, spec := range externalAgents {
		wg.Add(1)
		go func(i int, spec agentSpec) {
			defer wg.Done()
			res[i] = m.detectOne(ctx, spec)
		}(i, spec)
	}
	wg.Wait()
	return append(out, res...)
}

func (m *Manager) detectOne(ctx context.Context, spec agentSpec) AgentStatus {
	st := AgentStatus{Name: spec.Name, Label: spec.Label, ConfigPath: m.configPath(spec.Name), IDE: spec.NoRun}
	path := m.findBinary(spec.Binary)
	if app := m.findApp(spec.App); app != "" {
		// IDE: the app bundle counts as installed even without a PATH launcher.
		st.Installed, st.Path = true, app
		st.Connected = m.isRegisteredFile(spec.Name)
		if path != "" {
			vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if b, err := m.exec.Output(vctx, path, "--version"); err == nil {
				st.Version = firstLine(string(b))
			}
		}
		return st
	}
	if path == "" {
		st.InstallHint, st.InstallURL = spec.InstallHint, spec.InstallURL
		// Config-file based agents can still report registration.
		if spec.Name != AgentClaude {
			st.Connected = m.isRegisteredFile(spec.Name)
		}
		return st
	}
	st.Installed, st.Path = true, path
	vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if b, err := m.exec.Output(vctx, path, "--version"); err == nil || len(b) > 0 {
			st.Version = firstLine(string(b))
		}
	}()
	if spec.Name == AgentClaude {
		st.Connected, st.Scope = m.claudeRegistered(ctx, path)
	} else {
		st.Connected = m.isRegisteredFile(spec.Name)
	}
	wg.Wait()
	return st
}

// claudeRegistered reports whether the server exists in any scope, and which.
func (m *Manager) claudeRegistered(ctx context.Context, path string) (bool, string) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if b, err := m.exec.Output(cctx, path, "mcp", "get", MCPName); err == nil {
		return true, parseScope(string(b))
	}
	b, err := m.exec.Output(cctx, path, "mcp", "list")
	if err != nil {
		return false, ""
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), MCPName+":") {
			return true, ""
		}
	}
	return false, ""
}

// parseScope extracts the scope from `claude mcp get` output ("Scope: User config ...").
func parseScope(out string) string {
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if v, ok := strings.CutPrefix(ln, "Scope:"); ok {
			v = strings.ToLower(strings.TrimSpace(v))
			switch {
			case strings.HasPrefix(v, "user"):
				return "user"
			case strings.HasPrefix(v, "local"):
				return "local"
			case strings.HasPrefix(v, "project"):
				return "project"
			}
			return v
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

var errUnknownAgent = errors.New("unknown agent")
