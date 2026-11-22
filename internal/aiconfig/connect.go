package aiconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func (m *Manager) configPath(agent string) string {
	switch agent {
	case AgentCodex:
		return filepath.Join(m.home, ".codex", "config.toml")
	case AgentGemini:
		return filepath.Join(m.home, ".gemini", "settings.json")
	case AgentCursor:
		return filepath.Join(m.home, ".cursor", "mcp.json")
	case AgentKiro, AgentKiroCLI:
		// Kiro IDE and kiro-cli share the user-level MCP config.
		return filepath.Join(m.home, ".kiro", "settings", "mcp.json")
	}
	return ""
}

// isRegisteredFile is the read-only detection for file-configured agents.
func (m *Manager) isRegisteredFile(agent string) bool {
	b, err := os.ReadFile(m.configPath(agent))
	if err != nil {
		return false
	}
	if agent == AgentCodex {
		return tomlHasServer(string(b))
	}
	servers, _, err := jsonServers(b)
	if err != nil {
		return false
	}
	_, ok := servers[MCPName]
	return ok
}

// Connect registers the MCP server in the agent. Only call from a user action.
func (m *Manager) Connect(ctx context.Context, agent string) error {
	defer m.invalidateDetect()
	spec, ok := specFor(agent)
	if !ok {
		return errUnknownAgent
	}
	switch agent {
	case AgentClaude:
		path := m.findBinary(spec.Binary)
		if path == "" {
			return errors.New("claude CLI not installed")
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		// Drop an entry registered under the pre-rename server name (best effort).
		for _, scope := range []string{"user", "local"} {
			_, _ = m.exec.Output(cctx, path, "mcp", "remove", LegacyMCPName, "-s", scope)
		}
		if b, err := m.exec.Output(cctx, path, "mcp", "add", "--scope", "user", "--transport", "http", MCPName, m.mcpURL); err != nil {
			return fmt.Errorf("claude mcp add: %v: %s", err, strings.TrimSpace(string(b)))
		}
		m.invalidateDetect()
		return nil
	case AgentCodex:
		return m.editFile(agent, func(old []byte) ([]byte, error) {
			s := tomlRemoveServer(string(old))
			s = strings.TrimRight(s, "\n")
			if s != "" {
				s += "\n\n"
			}
			s += fmt.Sprintf("[mcp_servers.%s]\nurl = %q\n", MCPName, m.mcpURL)
			return []byte(s), nil
		})
	case AgentGemini:
		return m.editFile(agent, func(old []byte) ([]byte, error) {
			return jsonSetServer(old, map[string]any{"httpUrl": m.mcpURL})
		})
	case AgentCursor, AgentKiro, AgentKiroCLI:
		return m.editFile(agent, func(old []byte) ([]byte, error) {
			return jsonSetServer(old, map[string]any{"url": m.mcpURL})
		})
	}
	return errUnknownAgent
}

// Disconnect unregisters the MCP server. Only call from a user action.
func (m *Manager) Disconnect(ctx context.Context, agent string) error {
	spec, ok := specFor(agent)
	if !ok {
		return errUnknownAgent
	}
	switch agent {
	case AgentClaude:
		path := m.findBinary(spec.Binary)
		if path == "" {
			return errors.New("claude CLI not installed")
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		defer m.invalidateDetect()
		var firstErr error
		removed := false
		for _, name := range []string{MCPName, LegacyMCPName} {
			for _, scope := range []string{"user", "local"} {
				b, err := m.exec.Output(cctx, path, "mcp", "remove", name, "-s", scope)
				if err == nil {
					removed = true
					continue
				}
				if strings.Contains(strings.ToLower(string(b)), "not found") || strings.Contains(strings.ToLower(string(b)), "no mcp server") {
					continue
				}
				if firstErr == nil && name == MCPName {
					firstErr = fmt.Errorf("claude mcp remove -s %s: %v: %s", scope, err, strings.TrimSpace(string(b)))
				}
			}
		}
		if firstErr != nil && !removed {
			return firstErr
		}
		return nil
	case AgentCodex:
		return m.editFile(agent, func(old []byte) ([]byte, error) {
			return []byte(tomlRemoveServer(string(old))), nil
		})
	default:
		return m.editFile(agent, func(old []byte) ([]byte, error) { return jsonRemoveServer(old) })
	}
}

// editFile backs the file up to <file>.pocketlab-bak (when it exists) and
// atomically rewrites it with fn's result.
func (m *Manager) editFile(agent string, fn func([]byte) ([]byte, error)) error {
	p := m.configPath(agent)
	old, err := os.ReadFile(p)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	out, err := fn(old)
	if err != nil {
		return err
	}
	if !exists {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
	} else if _, serr := os.Stat(p + ".pocketlab-bak"); os.IsNotExist(serr) {
		// Keep the backup of the original: never overwrite an earlier one.
		if err := os.WriteFile(p+".pocketlab-bak", old, 0o600); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(p); err == nil {
		mode = st.Mode().Perm()
	}
	tmp := p + ".pocketlab-tmp"
	if err := os.WriteFile(tmp, out, mode); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ---- JSON (gemini / cursor) ----

func jsonServers(b []byte) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	top := map[string]json.RawMessage{}
	servers := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(b)) == 0 {
		return servers, top, nil
	}
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, nil, fmt.Errorf("parse config: %w", err)
	}
	if raw, ok := top["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, nil, fmt.Errorf("parse mcpServers: %w", err)
		}
	}
	return servers, top, nil
}

func jsonEncode(top, servers map[string]json.RawMessage) ([]byte, error) {
	raw, err := json.Marshal(servers)
	if err != nil {
		return nil, err
	}
	top["mcpServers"] = raw
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func jsonSetServer(old []byte, entry map[string]any) ([]byte, error) {
	servers, top, err := jsonServers(old)
	if err != nil {
		return nil, err
	}
	e, _ := json.Marshal(entry)
	delete(servers, LegacyMCPName) // replace a pre-rename entry
	servers[MCPName] = e
	return jsonEncode(top, servers)
}

func jsonRemoveServer(old []byte) ([]byte, error) {
	servers, top, err := jsonServers(old)
	if err != nil {
		return nil, err
	}
	_, hasNew := servers[MCPName]
	_, hasOld := servers[LegacyMCPName]
	if !hasNew && !hasOld {
		if len(bytes.TrimSpace(old)) == 0 {
			return old, nil
		}
		return old, nil // nothing to remove; leave bytes untouched
	}
	delete(servers, MCPName)
	delete(servers, LegacyMCPName)
	return jsonEncode(top, servers)
}

// ---- TOML (codex): line-based so unrelated content is preserved verbatim ----

var tomlHeader = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)

// tomlIsOurs reports whether a header key is our table or one of its subtables.
func tomlIsName(key, name string) bool {
	key = strings.ReplaceAll(key, `"`, "")
	key = strings.ReplaceAll(key, " ", "")
	return key == "mcp_servers."+name || strings.HasPrefix(key, "mcp_servers."+name+".")
}

// tomlIsOurs also matches the pre-rename table so it gets replaced/removed.
func tomlIsOurs(key string) bool {
	return tomlIsName(key, MCPName) || tomlIsName(key, LegacyMCPName)
}

func tomlHasServer(s string) bool {
	for _, ln := range strings.Split(s, "\n") {
		if mm := tomlHeader.FindStringSubmatch(strings.TrimRight(ln, "\r")); mm != nil && tomlIsName(mm[1], MCPName) {
			return true
		}
	}
	return false
}

func tomlRemoveServer(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	skipping := false
	for _, ln := range lines {
		if mm := tomlHeader.FindStringSubmatch(strings.TrimRight(ln, "\r")); mm != nil {
			skipping = tomlIsOurs(mm[1])
		}
		if !skipping {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}
