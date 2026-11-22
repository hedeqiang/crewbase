package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// CLIConfigNames are the entries at the top of a working directory from
// which coding CLIs (Claude Code, Codex, …) load project-level
// configuration when someone runs them in that folder. Several of them can
// widen what the CLI may do without asking: permission allow-lists and
// default modes, hooks that run shell commands, MCP servers the CLI
// starts, plugins, sub-agent definitions with their own tools and hooks. An
// agent must not plant them in a project the person later opens with
// their own CLI.
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
// command sandbox treat them as read-only).
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

// GitControlNames are the parts of a repository from which git itself
// runs programs or takes settings that do: .git/config (filter, merge and
// diff drivers, fsmonitor, sshCommand, include.path), hooks, the per-worktree
// configs and gitdir pointers under .git/worktrees, and .gitmodules. In a
// linked worktree .git is a file pointing at the repository; rewriting it
// would point git elsewhere. Crewbase runs git in its own process for the
// git tools, so an agent must not be able to change these.
var GitControlNames = []string{".git/config", ".git/config.worktree", ".git/hooks", ".git/worktrees", ".git/info", ".gitmodules"}

// GitControlPaths returns dir's GitControlNames entries that exist (only
// those: a sandbox that makes placeholders for missing protected paths
// must not create a .git where there is none), and dir/.git itself when it
// is a file (a linked worktree).
func GitControlPaths(dir string) []string {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil
	}
	dir = filepath.Clean(dir)
	st, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil {
		return nil
	}
	if !st.IsDir() {
		out := []string{filepath.Join(dir, ".git")}
		if _, err := os.Lstat(filepath.Join(dir, ".gitmodules")); err == nil {
			out = append(out, filepath.Join(dir, ".gitmodules"))
		}
		return out
	}
	var out []string
	for _, n := range GitControlNames {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// ProtectedPaths are the paths in dir an agent in sandbox mode may read but
// not write: the coding CLIs' config and git's control files.
func ProtectedPaths(dir string) []string {
	return append(CLIConfigPaths(dir), GitControlPaths(dir)...)
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
