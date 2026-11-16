package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// CLIConfigNames are the entries at the top of a working directory from
// which subscription coding CLIs (driven over ACP, see providers/llm/acp)
// load project-level configuration. Several of them can widen what the CLI
// may do without asking: permission allow-lists and default modes, hooks
// that run shell commands, MCP servers the CLI starts, plugins, sub-agent
// definitions with their own tools and hooks.
//
//	.claude, .mcp.json         Claude Code (settings[.local].json, hooks, agents, skills; project MCP servers)
//	.codex                     Codex (config.toml, hooks, exec policy)
//	.gemini                    Gemini CLI (settings.json: mcpServers, tools.allowed, hooks)
//	.cursor                    Cursor CLI (cli.json permissions, mcp.json, hooks.json)
//	.opencode, opencode.json,  OpenCode (permissions, mcp, in-process plugins);
//	opencode.jsonc, .agents    .agents is also read by OpenCode
//	.grok                      Grok CLI (config.toml permissions, mcp_servers, hooks)
//
// Plain instruction files (CLAUDE.md, AGENTS.md, GEMINI.md) are not listed:
// they shape the prompt but grant nothing, and agents legitimately write
// them.
//
// An agent in sandbox mode may not write these paths (fs tools and the
// command sandbox treat them as read-only), and in an agent's own workspace
// the ACP bridge moves any that exist aside before a CLI turn.
var CLIConfigNames = []string{
	".claude", ".mcp.json",
	".codex",
	".gemini",
	".cursor",
	".opencode", "opencode.json", "opencode.jsonc", ".agents",
	".grok",
}

// CLIConfigPaths returns dir/<name> for every CLIConfigNames entry; nil for
// an empty or relative dir.
func CLIConfigPaths(dir string) []string {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil
	}
	dir = filepath.Clean(dir)
	out := make([]string, 0, len(CLIConfigNames))
	for _, n := range CLIConfigNames {
		out = append(out, filepath.Join(dir, n))
	}
	return out
}

// UnderFold reports whether path is root or lies inside it, comparing
// case-insensitively: on the default macOS file systems ".CLAUDE" and
// ".claude" are the same directory, and being stricter elsewhere is
// harmless. Both paths must be absolute and clean.
func UnderFold(path, root string) bool {
	if root == "" {
		return false
	}
	if strings.EqualFold(path, root) {
		return true
	}
	sep := string(filepath.Separator)
	prefix := strings.TrimSuffix(root, sep) + sep
	return len(path) > len(prefix) && strings.EqualFold(path[:len(prefix)], prefix)
}

// IsCLIConfigPath reports whether path (absolute) is one of dir's
// CLIConfigNames entries or inside one. It compares the cleaned path and,
// where they exist, the paths with symlinks resolved, so neither a link to
// the config directory nor a config directory that is a link gets through.
func IsCLIConfigPath(dir, path string) bool {
	if dir == "" || path == "" || !filepath.IsAbs(path) {
		return false
	}
	path = filepath.Clean(path)
	realP := resolveExisting(path)
	for _, root := range CLIConfigPaths(dir) {
		realR := resolveExisting(root)
		if UnderFold(path, root) || UnderFold(realP, realR) || UnderFold(realP, root) || UnderFold(path, realR) {
			return true
		}
	}
	return false
}

// resolveExisting resolves symlinks in p; for a path that does not exist it
// resolves the deepest existing ancestor and appends the rest.
func resolveExisting(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolveExisting(parent), filepath.Base(p))
}

// OwnsWorkspace reports whether the agent works in a directory Crewbase
// made for an agent (<root>/workspaces/<id>, its own or, for a sub-agent,
// its parent's) rather than a project folder the person chose. A project
// folder can never lie inside the data directory (Registry.ProjectDir), so
// the location decides.
func (a *Agent) OwnsWorkspace() bool {
	ws := a.Workspace()
	if ws == "" || !filepath.IsAbs(ws) {
		return false
	}
	base := filepath.Join(a.reg.root, "workspaces")
	if within(resolveExisting(filepath.Clean(ws)), resolveExisting(base)) {
		return true
	}
	return within(filepath.Clean(ws), filepath.Clean(base))
}

func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// QuarantineCLIConfig moves every CLIConfigNames entry found at the top of
// dir into dir/.crewbase-quarantine/<stamp>/, so a CLI started there does
// not load configuration an agent wrote. It returns the names it moved.
// Lstat follows the file system's own case rules, so ".CLAUDE" is found on
// macOS exactly when the CLI would read it as ".claude".
func QuarantineCLIConfig(dir, stamp string) ([]string, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil, nil
	}
	var moved []string
	var firstErr error
	for _, p := range CLIConfigPaths(dir) {
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		q := filepath.Join(dir, ".crewbase-quarantine", stamp)
		if err := os.MkdirAll(q, 0o700); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := os.Rename(p, filepath.Join(q, filepath.Base(p))); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		moved = append(moved, filepath.Base(p))
	}
	return moved, firstErr
}
