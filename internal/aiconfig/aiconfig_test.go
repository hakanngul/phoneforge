package aiconfig

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeExec struct {
	mu       sync.Mutex
	calls    [][]string
	outputs  map[string]string // key: joined args after binary
	failures map[string]bool
	lines    []string
	block    chan struct{}
	envs     [][]string
}

func (f *fakeExec) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{name}, args...))
	k := strings.Join(args, " ")
	if f.failures[k] {
		return []byte("not found"), errors.New("exit 1")
	}
	return []byte(f.outputs[k]), nil
}

func (f *fakeExec) Stream(ctx context.Context, name string, args []string, env []string, onLine func(string, string)) error {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	f.mu.Unlock()
	for _, l := range f.lines {
		onLine("stdout", l)
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func newTestMgr(t *testing.T, fe *fakeExec, bins ...string) *Manager {
	t.Helper()
	home := t.TempDir()
	m := New(filepath.Join(home, "ai.json"), "")
	m.home = home
	m.exec = fe
	m.tmpDir = t.TempDir()
	m.dirs = []string{filepath.Join(home, ".local", "bin")}
	m.appDirs = []string{filepath.Join(home, "Applications")}
	set := map[string]bool{}
	for _, b := range bins {
		set[b] = true
	}
	m.lookPath = func(b string) (string, error) {
		if set[b] {
			return "/fake/bin/" + b, nil
		}
		return "", errors.New("nf")
	}
	return m
}

func TestDetect(t *testing.T) {
	fe := &fakeExec{
		outputs:  map[string]string{"--version": "1.2.3 (Claude Code)\nextra"},
		failures: map[string]bool{"mcp get phoneforge": true},
	}
	m := newTestMgr(t, fe, "claude", "codex")
	// codex registered via TOML
	cp := m.configPath(AgentCodex)
	os.MkdirAll(filepath.Dir(cp), 0o755)
	os.WriteFile(cp, []byte("[mcp_servers.phoneforge]\nurl = \"x\"\n"), 0o600)
	fe.outputs["mcp list"] = "other: http://x\nphoneforge: http://127.0.0.1:8700/mcp (HTTP)\n"

	got := map[string]AgentStatus{}
	for _, a := range m.Detect(context.Background()) {
		got[a.Name] = a
	}
	if !got["claude"].Installed || got["claude"].Version != "1.2.3 (Claude Code)" || !got["claude"].Connected {
		t.Fatalf("claude: %+v", got["claude"])
	}
	if !got["codex"].Installed || !got["codex"].Connected {
		t.Fatalf("codex: %+v", got["codex"])
	}
	if got["gemini"].Installed || got["gemini"].Connected {
		t.Fatalf("gemini: %+v", got["gemini"])
	}
	if !got["builtin"].Installed {
		t.Fatal("builtin missing")
	}
}

func TestFindBinaryInCommonDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses an extensionless fake binary")
	}
	m := newTestMgr(t, &fakeExec{})
	d := filepath.Join(m.home, ".local", "bin")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "gemini"), []byte("#!/bin/sh"), 0o755)
	if p := m.findBinary("gemini"); p != filepath.Join(d, "gemini") {
		t.Fatalf("got %q", p)
	}
	if m.findBinary("codex") != "" {
		t.Fatal("unexpected")
	}
}

func TestCodexConnectDisconnectPreserves(t *testing.T) {
	m := newTestMgr(t, &fakeExec{}, "codex")
	cp := m.configPath(AgentCodex)
	os.MkdirAll(filepath.Dir(cp), 0o755)
	orig := "model = \"o3\"\n\n[mcp_servers.other]\ncommand = \"x\"\n\n[mcp_servers.phoneforge]\nurl = \"old\"\n\n[mcp_servers.phoneforge.env]\nA = \"1\"\n\n[profiles.p]\nk = 1\n"
	os.WriteFile(cp, []byte(orig), 0o600)
	if err := m.Connect(context.Background(), AgentCodex); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(cp)
	s := string(b)
	if strings.Count(s, "[mcp_servers.phoneforge]") != 1 || strings.Contains(s, "old") || strings.Contains(s, "A = \"1\"") {
		t.Fatalf("bad:\n%s", s)
	}
	for _, keep := range []string{"model = \"o3\"", "[mcp_servers.other]", "[profiles.p]", "http://127.0.0.1:8700/mcp"} {
		if !strings.Contains(s, keep) {
			t.Fatalf("lost %q:\n%s", keep, s)
		}
	}
	bak, err := os.ReadFile(cp + ".phoneforge-bak")
	if err != nil || string(bak) != orig {
		t.Fatal("backup wrong")
	}
	if err := m.Disconnect(context.Background(), AgentCodex); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(cp)
	if strings.Contains(string(b), "phoneforge") || !strings.Contains(string(b), "[profiles.p]") || !strings.Contains(string(b), "[mcp_servers.other]") {
		t.Fatalf("after disconnect:\n%s", b)
	}
}

func TestCodexConnectCreatesFile(t *testing.T) {
	m := newTestMgr(t, &fakeExec{}, "codex")
	if err := m.Connect(context.Background(), AgentCodex); err != nil {
		t.Fatal(err)
	}
	if !m.isRegisteredFile(AgentCodex) {
		t.Fatal("not registered")
	}
}

func TestGeminiJSON(t *testing.T) {
	m := newTestMgr(t, &fakeExec{}, "gemini")
	cp := m.configPath(AgentGemini)
	os.MkdirAll(filepath.Dir(cp), 0o755)
	os.WriteFile(cp, []byte(`{"theme":"dark","mcpServers":{"x":{"command":"y"}}}`), 0o600)
	if err := m.Connect(context.Background(), AgentGemini); err != nil {
		t.Fatal(err)
	}
	var v map[string]map[string]any
	var raw map[string]any
	b, _ := os.ReadFile(cp)
	json.Unmarshal(b, &raw)
	if raw["theme"] != "dark" {
		t.Fatal("lost theme")
	}
	json.Unmarshal(b, &v)
	if v["mcpServers"]["x"] == nil || v["mcpServers"]["phoneforge"] == nil {
		t.Fatalf("servers: %s", b)
	}
	if !m.isRegisteredFile(AgentGemini) {
		t.Fatal("detect")
	}
	if err := m.Disconnect(context.Background(), AgentGemini); err != nil {
		t.Fatal(err)
	}
	if m.isRegisteredFile(AgentGemini) {
		t.Fatal("still registered")
	}
	b, _ = os.ReadFile(cp)
	if !strings.Contains(string(b), `"x"`) {
		t.Fatal("lost x")
	}
}

func TestGeminiInvalidJSONUntouched(t *testing.T) {
	m := newTestMgr(t, &fakeExec{}, "gemini")
	cp := m.configPath(AgentGemini)
	os.MkdirAll(filepath.Dir(cp), 0o755)
	os.WriteFile(cp, []byte(`{ not json`), 0o600)
	if err := m.Connect(context.Background(), AgentGemini); err == nil {
		t.Fatal("want error")
	}
	b, _ := os.ReadFile(cp)
	if string(b) != `{ not json` {
		t.Fatal("modified")
	}
}

func TestClaudeConnectCommands(t *testing.T) {
	fe := &fakeExec{}
	m := newTestMgr(t, fe, "claude")
	if err := m.Connect(context.Background(), AgentClaude); err != nil {
		t.Fatal(err)
	}
	if err := m.Disconnect(context.Background(), AgentClaude); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fe.calls[0], " "); got != "/fake/bin/claude mcp add --scope user --transport http phoneforge http://127.0.0.1:8700/mcp" {
		t.Fatal(got)
	}
	if got := strings.Join(fe.calls[1], " "); got != "/fake/bin/claude mcp remove phoneforge -s user" {
		t.Fatal(got)
	}
	if got := strings.Join(fe.calls[2], " "); got != "/fake/bin/claude mcp remove phoneforge -s local" {
		t.Fatal(got)
	}
}

func TestClaudeDisconnectIgnoresNotFound(t *testing.T) {
	fe := &fakeExec{failures: map[string]bool{"mcp remove phoneforge -s local": true}}
	m := newTestMgr(t, fe, "claude")
	if err := m.Disconnect(context.Background(), AgentClaude); err != nil {
		t.Fatal(err)
	}
}

func TestDetectCacheAndRefresh(t *testing.T) {
	fe := &fakeExec{outputs: map[string]string{
		"--version":          "1.0",
		"mcp get phoneforge": "phoneforge:\n  Scope: User config (available in all your projects)\n",
	}}
	m := newTestMgr(t, fe, "claude")
	count := func() int {
		fe.mu.Lock()
		defer fe.mu.Unlock()
		return len(fe.calls)
	}
	r := m.DetectCached(context.Background(), false)
	var cl AgentStatus
	for _, a := range r {
		if a.Name == "claude" {
			cl = a
		}
	}
	if !cl.Connected || cl.Scope != "user" {
		t.Fatalf("%+v", cl)
	}
	n := count()
	m.DetectCached(context.Background(), false)
	if count() != n {
		t.Fatal("cache miss")
	}
	m.DetectCached(context.Background(), true)
	if count() == n {
		t.Fatal("refresh did not re-detect")
	}
	n = count()
	m.invalidateDetect()
	m.DetectCached(context.Background(), false)
	if count() == n {
		t.Fatal("invalidate did not re-detect")
	}
	n = count()
	if err := m.Connect(context.Background(), AgentClaude); err != nil {
		t.Fatal(err)
	}
	m.DetectCached(context.Background(), false)
	if count() <= n+1 {
		t.Fatal("connect did not invalidate")
	}
	m.cacheTTL = time.Nanosecond
	time.Sleep(time.Millisecond)
	n = count()
	m.DetectCached(context.Background(), false)
	if count() == n {
		t.Fatal("ttl not honoured")
	}
}

type slowExec struct {
	fakeExec
	delay time.Duration
}

func (s *slowExec) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	time.Sleep(s.delay)
	return s.fakeExec.Output(ctx, name, args...)
}

func TestDetectConcurrent(t *testing.T) {
	se := &slowExec{delay: 150 * time.Millisecond}
	m := newTestMgr(t, &se.fakeExec, "claude", "codex", "gemini", "cursor-agent")
	m.exec = se
	start := time.Now()
	m.Detect(context.Background())
	// sequential would be >= 4 agents * 150ms (+ claude mcp get)
	if d := time.Since(start); d > 450*time.Millisecond {
		t.Fatalf("detection not concurrent: %v", d)
	}
}

func TestSettingsPersist(t *testing.T) {
	m := newTestMgr(t, &fakeExec{})
	if m.Settings().Automation.Agent != AgentBuiltin || m.Settings().MaxSteps != 50 {
		t.Fatal("defaults")
	}
	err := m.SetSettings(Settings{Automation: TypeSettings{Agent: "claude", Model: "opus"}, MaxSteps: 12})
	if err != nil {
		t.Fatal(err)
	}
	m2 := New(m.settingsPath, "")
	s := m2.Settings()
	if s.Automation.Agent != "claude" || s.Automation.Model != "opus" || s.MaxSteps != 12 || s.Testing.Agent != AgentBuiltin {
		t.Fatalf("%+v", s)
	}
	if m.SetSettings(Settings{Automation: TypeSettings{Agent: "bogus"}}) == nil {
		t.Fatal("want error")
	}
}

func waitDone(t *testing.T, m *Manager, id string) []Event {
	t.Helper()
	r := m.getRun(id)
	for i := 0; i < 200; i++ {
		evs, done, _ := r.snapshot(0)
		if done {
			return evs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not finish")
	return nil
}

func TestRunClaude(t *testing.T) {
	fe := &fakeExec{lines: []string{`{"type":"system"}`, `{"type":"result"}`}}
	m := newTestMgr(t, fe, "claude")
	m.SetSettings(Settings{Automation: TypeSettings{Agent: "claude", Model: "sonnet"}, MaxSteps: 7, ExtraInstructions: "be brief"})
	id, err := m.StartRun(RunRequest{Type: "automation", DeviceID: "dev1", Prompt: "open settings"})
	if err != nil {
		t.Fatal(err)
	}
	evs := waitDone(t, m, id)
	if evs[0].Type != "start" || evs[1].Data != `{"type":"system"}` || evs[len(evs)-1].Data != "ok" {
		t.Fatalf("%+v", evs)
	}
	args := strings.Join(fe.calls[0], "\x00")
	for _, w := range []string{"-p\x00open settings", "stream-json", "--mcp-config", "--strict-mcp-config", "--model\x00sonnet", "mcp__phoneforge", "dev1", "be brief"} {
		if !strings.Contains(args, w) {
			t.Fatalf("missing %q in %q", w, args)
		}
	}
	// temp mcp config removed after the run
	time.Sleep(20 * time.Millisecond)
	if ents, _ := os.ReadDir(m.tmpDir); len(ents) != 0 {
		t.Fatalf("temp file left: %v", ents)
	}
}

func TestRunCodexAndBuiltinAndErrors(t *testing.T) {
	fe := &fakeExec{lines: []string{"hello"}}
	m := newTestMgr(t, fe, "codex")
	id, err := m.StartRun(RunRequest{Agent: "codex", Type: "testing", Prompt: "write a test"})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, m, id)
	if fe.calls[0][1] != "exec" || !strings.Contains(fe.calls[0][len(fe.calls[0])-1], "write a test") {
		t.Fatalf("%v", fe.calls[0])
	}
	if _, err := m.StartRun(RunRequest{Prompt: "x"}); !errors.Is(err, ErrBuiltin) {
		t.Fatalf("want builtin, got %v", err)
	}
	if _, err := m.StartRun(RunRequest{Agent: "gemini", Prompt: "x"}); err == nil {
		t.Fatal("want not installed")
	}
	if _, err := m.StartRun(RunRequest{Agent: "codex"}); err == nil {
		t.Fatal("want prompt error")
	}
}

func TestCancel(t *testing.T) {
	fe := &fakeExec{block: make(chan struct{})}
	m := newTestMgr(t, fe, "codex")
	id, _ := m.StartRun(RunRequest{Agent: "codex", Prompt: "x"})
	if !m.CancelRun(id) {
		t.Fatal("cancel")
	}
	evs := waitDone(t, m, id)
	if evs[len(evs)-1].Data != "cancelled" {
		t.Fatalf("%+v", evs)
	}
	if m.CancelRun("nope") {
		t.Fatal("unknown id")
	}
}

func TestHTTP(t *testing.T) {
	fe := &fakeExec{lines: []string{"line1"}, outputs: map[string]string{"--version": "9.9"}, failures: map[string]bool{"mcp get phoneforge": true, "mcp list": true}}
	m := newTestMgr(t, fe, "codex")
	srv := httptest.NewServer(m.Routes())
	defer srv.Close()

	res, _ := http.Get(srv.URL + "/agents")
	var ag struct {
		Agents []AgentStatus `json:"agents"`
	}
	json.NewDecoder(res.Body).Decode(&ag)
	res.Body.Close()
	if len(ag.Agents) != 7 {
		t.Fatalf("%+v", ag)
	}

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/settings", strings.NewReader(`{"automation":{"agent":"codex"},"maxSteps":5}`))
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	res, _ = http.Post(srv.URL+"/agents/nope/connect", "application/json", nil)
	if res.StatusCode != 404 {
		t.Fatal(res.StatusCode)
	}
	res, _ = http.Post(srv.URL+"/agents/codex/connect", "application/json", nil)
	if res.StatusCode != 200 || !m.isRegisteredFile(AgentCodex) {
		t.Fatal("connect", res.StatusCode)
	}
	res, _ = http.Post(srv.URL+"/runs", "application/json", strings.NewReader(`{"type":"automation","deviceId":"d","prompt":"go"}`))
	var rr struct{ ID string }
	json.NewDecoder(res.Body).Decode(&rr)
	res.Body.Close()
	if res.StatusCode != 202 || rr.ID == "" {
		t.Fatal(res.StatusCode)
	}
	res, err := http.Get(srv.URL + "/runs/" + rr.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	var types []string
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			types = append(types, v)
		}
	}
	if strings.Join(types, ",") != "start,stdout,done" {
		t.Fatal(types)
	}
	res2, _ := http.Post(srv.URL+"/runs/zzz/cancel", "application/json", nil)
	if res2.StatusCode != 404 {
		t.Fatal(res2.StatusCode)
	}
}

func TestAllowedTools(t *testing.T) {
	if got := AllowedTools(Settings{}); len(got) != 1 || got[0] != "mcp__phoneforge" {
		t.Fatalf("allowed: %v", got)
	}
	cmd := strings.Join(buildCommand(AgentClaude, "claude", "sys", "p", "", "/tmp/x.json", "", Settings{MaxSteps: 5}), "\x00")
	if !strings.Contains(cmd, "--allowedTools\x00mcp__phoneforge") || strings.Contains(cmd, "--max-turns") || strings.Contains(cmd, "--dangerously-skip-permissions") {
		t.Fatalf("bad default command: %q", cmd)
	}
	cmd = strings.Join(buildCommand(AgentClaude, "claude", "sys", "p", "", "/tmp/x.json", "", Settings{FullAccess: true}), "\x00")
	if !strings.Contains(cmd, "--dangerously-skip-permissions") {
		t.Fatalf("full access flag missing: %q", cmd)
	}
}

func TestBuildCommandModelResume(t *testing.T) {
	cmd := strings.Join(buildCommand(AgentClaude, "claude", "sys", "p", "haiku", "/tmp/x.json", "sess-1", Settings{}), "\x00")
	for _, w := range []string{"--model\x00haiku", "--resume\x00sess-1", "--allowedTools\x00mcp__phoneforge"} {
		if !strings.Contains(cmd, w) {
			t.Fatalf("missing %q in %q", w, cmd)
		}
	}
	cmd = strings.Join(buildCommand(AgentClaude, "claude", "sys", "p", "", "/tmp/x.json", "", Settings{}), "\x00")
	if strings.Contains(cmd, "--resume") || strings.Contains(cmd, "--model") {
		t.Fatalf("unexpected flags: %q", cmd)
	}
}

func TestResolveModel(t *testing.T) {
	cases := []struct{ agent, typ, in, want string }{
		{AgentClaude, TypeAutomation, "", "haiku"},
		{AgentClaude, TypeAutomation, "default", ""},
		{AgentClaude, TypeAutomation, "opus", "opus"},
		{AgentClaude, TypeTesting, "", ""},
		{AgentCodex, TypeAutomation, "", ""},
	}
	for _, c := range cases {
		if got := ResolveModel(c.agent, c.typ, c.in); got != c.want {
			t.Fatalf("%v: got %q", c, got)
		}
	}
}

func TestSessionIDFromLine(t *testing.T) {
	if got := sessionIDFromLine(`{"type":"system","session_id":"abc"}`); got != "abc" {
		t.Fatal(got)
	}
	if sessionIDFromLine(`not json session_id`) != "" || sessionIDFromLine(`{"a":1}`) != "" {
		t.Fatal("expected empty")
	}
}

func TestSettingsLegacyAutoApproveIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aiconfig.json")
	old := `{"automation":{"agent":"claude","model":""},"testing":{"agent":"builtin","model":""},"maxSteps":50,"autoApprove":false}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(path, "")
	s := m.Settings()
	if s.Version != settingsVersion || s.FullAccess || s.Automation.Agent != "claude" {
		t.Fatalf("unexpected: %+v", s)
	}
	s.FullAccess = true
	if err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	if !New(path, "").Settings().FullAccess {
		t.Fatal("fullAccess not persisted")
	}
}

func newKiroMgr(t *testing.T, fe *fakeExec, bins ...string) *Manager {
	m := newTestMgr(t, fe, bins...)
	m.appDirs = []string{filepath.Join(m.home, "Applications")}
	return m
}

func TestDetectKiro(t *testing.T) {
	fe := &fakeExec{outputs: map[string]string{"--version": "kiro-cli 1.9.0"}}
	m := newKiroMgr(t, fe, "kiro-cli")
	got := map[string]AgentStatus{}
	for _, a := range m.Detect(context.Background()) {
		got[a.Name] = a
	}
	k, c := got[AgentKiro], got[AgentKiroCLI]
	if k.Installed || !k.IDE || k.InstallURL == "" || k.Label == "" {
		t.Fatalf("kiro IDE: %+v", k)
	}
	if !c.Installed || c.Version != "kiro-cli 1.9.0" || c.IDE || c.Path != "/fake/bin/kiro-cli" {
		t.Fatalf("kiro-cli: %+v", c)
	}
	if got["claude"].InstallHint != "" && got["claude"].Installed {
		t.Fatal("unexpected hint")
	}
	// IDE app bundle makes the IDE installed; shared config makes both connected.
	os.MkdirAll(filepath.Join(m.home, "Applications", "Kiro.app"), 0o755)
	if err := m.Connect(context.Background(), AgentKiroCLI); err != nil {
		t.Fatal(err)
	}
	got = map[string]AgentStatus{}
	for _, a := range m.Detect(context.Background()) {
		got[a.Name] = a
	}
	if !got[AgentKiro].Installed || !got[AgentKiro].Connected || !got[AgentKiroCLI].Connected {
		t.Fatalf("after connect: %+v / %+v", got[AgentKiro], got[AgentKiroCLI])
	}
	if got[AgentKiro].ConfigPath != filepath.Join(m.home, ".kiro", "settings", "mcp.json") {
		t.Fatalf("path %q", got[AgentKiro].ConfigPath)
	}
	// kiro on PATH (no app) also counts as installed.
	m2 := newKiroMgr(t, &fakeExec{}, "kiro")
	for _, a := range m2.Detect(context.Background()) {
		if a.Name == AgentKiro && !a.Installed {
			t.Fatal("kiro on PATH not detected")
		}
	}
}

func TestKiroConnectDisconnectPreserves(t *testing.T) {
	m := newKiroMgr(t, &fakeExec{})
	cp := m.configPath(AgentKiro)
	os.MkdirAll(filepath.Dir(cp), 0o755)
	orig := `{"mcpServers":{"other":{"command":"npx","args":["x"],"autoApprove":["*"]}},"extra":1}`
	os.WriteFile(cp, []byte(orig), 0o600)
	ctx := context.Background()
	if err := m.Connect(ctx, AgentKiro); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
		Extra      int                       `json:"extra"`
	}
	b, _ := os.ReadFile(cp)
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.MCPServers["phoneforge"]["url"] != DefaultMCPURL || cfg.MCPServers["other"]["command"] != "npx" || cfg.Extra != 1 {
		t.Fatalf("bad merge:\n%s", b)
	}
	if bak, _ := os.ReadFile(cp + ".phoneforge-bak"); string(bak) != orig {
		t.Fatal("backup wrong")
	}
	// second connect (via kiro-cli) must not overwrite the original backup
	if err := m.Connect(ctx, AgentKiroCLI); err != nil {
		t.Fatal(err)
	}
	if bak, _ := os.ReadFile(cp + ".phoneforge-bak"); string(bak) != orig {
		t.Fatal("backup overwritten")
	}
	if err := m.Disconnect(ctx, AgentKiroCLI); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(cp)
	if strings.Contains(string(b), "phoneforge") || !strings.Contains(string(b), `"other"`) || !strings.Contains(string(b), `"extra"`) {
		t.Fatalf("after disconnect:\n%s", b)
	}
	// connect creating the file from scratch
	m3 := newKiroMgr(t, &fakeExec{})
	if err := m3.Connect(ctx, AgentKiro); err != nil {
		t.Fatal(err)
	}
	if !m3.isRegisteredFile(AgentKiroCLI) {
		t.Fatal("not registered")
	}
}

func TestBuildCommandKiroCLI(t *testing.T) {
	cmd := strings.Join(buildCommand(AgentKiroCLI, "kiro-cli", "sys", "do it", "claude-sonnet-4", "", "last", Settings{}), "\x00")
	for _, w := range []string{"kiro-cli\x00chat\x00--no-interactive", "--trust-tools=@phoneforge", "--model\x00claude-sonnet-4", "--resume", "--require-mcp-startup", "sys\n\nTask: do it"} {
		if !strings.Contains(cmd, w) {
			t.Fatalf("missing %q in %q", w, cmd)
		}
	}
	if strings.Contains(cmd, "--trust-all-tools") {
		t.Fatal("unexpected trust-all")
	}
	cmd = strings.Join(buildCommand(AgentKiroCLI, "kiro-cli", "sys", "p", "", "", "", Settings{FullAccess: true}), "\x00")
	if !strings.Contains(cmd, "--trust-all-tools") || strings.Contains(cmd, "--trust-tools") || strings.Contains(cmd, "--resume") || strings.Contains(cmd, "--model") {
		t.Fatalf("bad full-access cmd: %q", cmd)
	}
}

func TestCleanTerminalLine(t *testing.T) {
	cases := map[string]string{
		"\x1b[1;32mHello\x1b[0m world  ": "Hello world",
		"spinner\rSecond frame\r> done":  "> done",
		"\x1b[2K\x1b[1G":                 "",
		"\x1b]0;title\x07text":           "text",
		"plain":                          "plain",
	}
	for in, want := range cases {
		if got := CleanTerminalLine(in); got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestRunKiroCLI(t *testing.T) {
	fe := &fakeExec{lines: []string{"\x1b[32mThinking\x1b[0m", "", "\x1b[2K", "Opened Settings."}}
	m := newKiroMgr(t, fe, "kiro-cli")
	if _, err := m.StartRun(RunRequest{Agent: AgentKiro, Prompt: "x"}); err == nil {
		t.Fatal("IDE must not run")
	}
	if err := m.SetSettings(Settings{Automation: TypeSettings{Agent: AgentKiro}}); err == nil {
		t.Fatal("IDE must not be selectable")
	}
	if err := m.SetSettings(Settings{Automation: TypeSettings{Agent: AgentKiroCLI, Model: "auto"}}); err != nil {
		t.Fatal(err)
	}
	id, err := m.StartRun(RunRequest{Type: "automation", DeviceID: "dev1", Prompt: "open settings"})
	if err != nil {
		t.Fatal(err)
	}
	evs := waitDone(t, m, id)
	var out []string
	sawSession := false
	for _, e := range evs {
		if e.Type == "stdout" {
			out = append(out, e.Data)
		}
		if e.Type == "session" {
			sawSession = true
		}
	}
	if strings.Join(out, "|") != "Thinking|Opened Settings." || !sawSession || evs[len(evs)-1].Data != "ok" {
		t.Fatalf("%+v", evs)
	}
	call := strings.Join(fe.calls[0], "\x00")
	if !strings.Contains(call, "--model\x00auto") || strings.Contains(call, "--resume") {
		t.Fatalf("call %q", call)
	}
	dir := strings.TrimPrefix(fe.envs[0][0], runDirEnv)
	b, err := os.ReadFile(filepath.Join(dir, ".kiro", "settings", "mcp.json"))
	if err != nil || !strings.Contains(string(b), DefaultMCPURL) {
		t.Fatalf("workspace mcp.json: %v %s", err, b)
	}
	// follow-up with a session marker resumes
	id, _ = m.StartRun(RunRequest{Type: "automation", Prompt: "again", SessionID: "last"})
	waitDone(t, m, id)
	if !strings.Contains(strings.Join(fe.calls[1], "\x00"), "--resume") {
		t.Fatalf("no resume: %v", fe.calls[1])
	}
}

func TestBuildCommandCodex(t *testing.T) {
	cmd := strings.Join(buildCommand(AgentCodex, "codex", "sys", "do it", "m1", "", "", Settings{}), "\x00")
	for _, w := range []string{"codex\x00exec", "--model\x00m1", "--skip-git-repo-check",
		`mcp_servers.phoneforge.default_tools_approval_mode="approve"`, "--sandbox\x00workspace-write", "sys\n\nTask: do it"} {
		if !strings.Contains(cmd, w) {
			t.Fatalf("missing %q in %q", w, cmd)
		}
	}
	if strings.Contains(cmd, "--full-auto") || strings.Contains(cmd, "bypass") {
		t.Fatalf("unexpected flag: %q", cmd)
	}
	cmd = strings.Join(buildCommand(AgentCodex, "codex", "sys", "p", "", "", "", Settings{FullAccess: true}), "\x00")
	if !strings.Contains(cmd, "--dangerously-bypass-approvals-and-sandbox") || strings.Contains(cmd, "--sandbox") {
		t.Fatalf("bad full-access cmd: %q", cmd)
	}
}

func TestBuiltinAndOllamaEndpoints(t *testing.T) {
	m := newTestMgr(t, &fakeExec{})
	m.builtinProvider = func() string { return "ollama" }
	m.ollamaHost = func() string { return "http://127.0.0.1:1" }
	m.ollamaModels = func(_ context.Context, host string) ([]string, error) {
		return []string{"llama3.1:8b", "qwen3:4b"}, nil
	}
	srv := httptest.NewServer(m.Routes())
	defer srv.Close()

	var b struct {
		Provider     string `json:"provider"`
		DefaultModel string `json:"defaultModel"`
	}
	res, err := http.Get(srv.URL + "/builtin")
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(res.Body).Decode(&b)
	res.Body.Close()
	if b.Provider != "ollama" || b.DefaultModel == "" {
		t.Fatalf("builtin: %+v", b)
	}

	var o struct {
		Reachable bool     `json:"reachable"`
		Models    []string `json:"models"`
	}
	res, _ = http.Get(srv.URL + "/ollama/models")
	json.NewDecoder(res.Body).Decode(&o)
	res.Body.Close()
	if !o.Reachable || len(o.Models) != 2 || o.Models[1] != "qwen3:4b" {
		t.Fatalf("ollama models: %+v", o)
	}

	// Unreachable Ollama: reachable=false and an empty (non-null) list.
	m.ollamaModels = func(context.Context, string) ([]string, error) { return nil, errors.New("down") }
	res, _ = http.Get(srv.URL + "/ollama/models")
	raw := map[string]any{}
	json.NewDecoder(res.Body).Decode(&raw)
	res.Body.Close()
	if raw["reachable"] != false {
		t.Fatalf("expected unreachable: %v", raw)
	}
	if l, ok := raw["models"].([]any); !ok || len(l) != 0 {
		t.Fatalf("models should be []: %v", raw["models"])
	}
}
