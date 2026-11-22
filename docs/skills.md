# Claude Code skills and plugin

Pocketlab ships six original Claude Code skills (in `skills/`) that teach an agent how to use the server's MCP
tools, the `pocketlab` CLI and the DSL. They are written from Pocketlab's own capabilities and are packaged as a
Claude Code plugin that also registers the MCP server.

| Skill | Use it to |
|---|---|
| `pocketlab-device-control` | Core observe -> act -> verify loop: pick a device, read the compact UI tree, tap/swipe/type, launch apps, recover from alerts and crashes. Includes `references/mcp-tools.md`. |
| `pocketlab-smoke-test` | Launch an app, walk its main flows, screenshot each step, scan crash reports, write a pass/fail report. |
| `pocketlab-accessibility-audit` | Inspect UI trees on every screen for missing labels, small tap targets (44pt iOS / 48dp Android), unnamed inputs; report by severity. |
| `pocketlab-store-screenshots` | Capture key screens at App Store / Play sizes and build a folder plus HTML gallery. |
| `pocketlab-test-authoring` | Write, validate and run `.mob` scripts and JSON DSL (v0.2). Includes `references/mob-syntax.md` and `references/dsl-actions.md`. |
| `pocketlab-cli` | Use the `pocketlab` command from shells, scripts and CI. |

## Install as a plugin (recommended)
```
/plugin marketplace add hakanngul/phoneforge
/plugin install pocketlab
```
The plugin manifest (`.claude-plugin/plugin.json`) loads `skills/` and declares an MCP server named `pocketlab`
at `http://127.0.0.1:8700/mcp`. Start the server first (`go run ./cmd/pocketlabd -addr 127.0.0.1:8700`);
the MCP connection fails harmlessly until it is running. The marketplace file is `.claude-plugin/marketplace.json`.
Validate the manifests locally with `claude plugin validate .`.

## Install the skills only
```bash
scripts/install-skills.sh              # symlink skills/pocketlab-* into ~/.claude/skills
scripts/install-skills.sh --copy       # copy instead
scripts/install-skills.sh --uninstall  # remove what it installed
scripts/install-skills.sh --dest DIR --dry-run
```
Only `pocketlab-*` folders are touched. An existing entry with the same name is replaced only if it is a symlink or
a copy of a Pocketlab skill; anything else is skipped and reported. Skills-only installs do not add the MCP server;
add it yourself (`claude mcp add --transport http pocketlab http://127.0.0.1:8700/mcp`) or use the CLI skill.

## Writing or changing a skill
- One folder per skill under `skills/`, containing `SKILL.md` with YAML frontmatter: `name` (must equal the folder
  name) and `description` (what it does plus trigger phrases, English and Turkish; max 1024 chars; quote it if it
  contains `: `). Supporting files go in `references/` and are mentioned in the body as `references/<file>`.
- Keep content concrete and tied to real tool names, arguments and DSL keys. Check them against
  `internal/mcpserver`, `internal/dsl` and `internal/mobtest`.
- Run `go test ./internal/skillcheck/`. It parses every frontmatter, checks name/folder and description, verifies
  referenced files exist, checks the plugin and marketplace manifests, and runs the script examples in
  `pocketlab-test-authoring` through the real DSL and `.mob` parsers.
