// Package skillcheck validates the Claude Code skills and plugin manifests
// shipped in the repository (skills/*/SKILL.md, .claude-plugin/*.json).
package skillcheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"phoneforge/internal/dsl"
	"phoneforge/internal/mobtest"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if dir == filepath.Dir(dir) {
			t.Fatal("go.mod not found")
		}
	}
}

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func parseFrontmatter(src string) (frontmatter, string, bool) {
	var fm frontmatter
	src = strings.ReplaceAll(src, "\r\n", "\n")
	if !strings.HasPrefix(src, "---\n") {
		return fm, "", false
	}
	rest := src[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return fm, "", false
	}
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		return fm, "", false
	}
	return fm, rest[end+5:], true
}

func skillFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "skills", "*", "SKILL.md"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no skills found: %v", err)
	}
	return files
}

func TestSkillFrontmatter(t *testing.T) {
	for _, f := range skillFiles(t) {
		folder := filepath.Base(filepath.Dir(f))
		t.Run(folder, func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			fm, body, ok := parseFrontmatter(string(b))
			if !ok {
				t.Fatal("missing or invalid YAML frontmatter (must start with --- and parse as YAML)")
			}
			if fm.Name != folder {
				t.Errorf("name %q does not match folder %q", fm.Name, folder)
			}
			if strings.TrimSpace(fm.Description) == "" {
				t.Error("description is empty")
			}
			if len(fm.Description) > 1024 {
				t.Errorf("description is %d chars (max 1024)", len(fm.Description))
			}
			if strings.TrimSpace(body) == "" {
				t.Error("skill body is empty")
			}
			if !strings.HasPrefix(folder, "phoneforge-") {
				t.Errorf("skill folder %q should start with phoneforge-", folder)
			}
		})
	}
}

var linkRe = regexp.MustCompile("`(references/[A-Za-z0-9._/-]+)`")

func TestSkillReferencesExist(t *testing.T) {
	for _, f := range skillFiles(t) {
		b, _ := os.ReadFile(f)
		for _, m := range linkRe.FindAllStringSubmatch(string(b), -1) {
			p := filepath.Join(filepath.Dir(f), m[1])
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s mentions %s which does not exist", f, m[1])
			}
		}
	}
}

func TestPluginManifests(t *testing.T) {
	root := repoRoot(t)
	read := func(rel string, v any) {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
	}
	var plugin struct {
		Name       string                       `json:"name"`
		Skills     string                       `json:"skills"`
		MCPServers map[string]map[string]string `json:"mcpServers"`
	}
	read(".claude-plugin/plugin.json", &plugin)
	if plugin.Name != "phoneforge" {
		t.Errorf("plugin name = %q", plugin.Name)
	}
	if _, err := os.Stat(filepath.Join(root, plugin.Skills)); err != nil {
		t.Errorf("plugin skills path %q: %v", plugin.Skills, err)
	}
	if got := plugin.MCPServers["phoneforge"]["url"]; got != "http://127.0.0.1:8700/mcp" {
		t.Errorf("mcp url = %q", got)
	}
	var mk struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"plugins"`
	}
	read(".claude-plugin/marketplace.json", &mk)
	if mk.Name == "" || len(mk.Plugins) != 1 || mk.Plugins[0].Name != "phoneforge" || mk.Plugins[0].Source == "" {
		t.Errorf("unexpected marketplace manifest: %+v", mk)
	}
}

var fenceRe = regexp.MustCompile("(?s)```([a-z]*)\n(.*?)```")

// TestAuthoringExamples makes sure the script examples documented in the
// test-authoring skill are accepted by the real parsers.
func TestAuthoringExamples(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "skills", "phoneforge-test-authoring")
	files := []string{"SKILL.md", "references/dsl-actions.md", "references/mob-syntax.md"}
	jsonN, mobN := 0, 0
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range fenceRe.FindAllStringSubmatch(string(b), -1) {
			lang, code := m[1], m[2]
			switch {
			case lang == "json" && strings.Contains(code, `"steps"`):
				jsonN++
				if _, err := dsl.Parse([]byte(code)); err != nil {
					t.Errorf("%s: JSON example does not parse: %v\n%s", f, err, code)
				}
			case lang == "" && f != "references/mob-syntax.md" && strings.Contains(code, "app \"com.example.app\""):
				mobN++
				if _, errs := mobtest.ParseWith([]byte(code), mobtest.ParseOptions{IgnoreRun: true}); len(errs) > 0 {
					t.Errorf("%s: .mob example does not parse: %v", f, errs)
				}
			}
		}
	}
	if jsonN < 3 || mobN < 1 {
		t.Errorf("expected several examples, found %d JSON and %d mob", jsonN, mobN)
	}
}
