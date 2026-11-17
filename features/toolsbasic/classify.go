package toolsbasic

import (
	"path/filepath"
	"strings"

	"github.com/hedeqiang/crewbase/core/tools"
)

// severity orders categories: a compound command takes its most severe part.
var severity = map[string]int{
	tools.CategoryExec:    1,
	tools.CategoryInstall: 2,
	tools.CategoryDelete:  3,
	tools.CategoryPublish: 4,
	tools.CategoryPush:    5,
}

// ClassifyCommand derives the action category of a shell command line.
// It is a best-effort heuristic for the rules engine (Ask on push / delete
// / publish); the sandbox, not this function, is what confines a command.
func ClassifyCommand(cmd string) string { return classifyCommand(cmd, false) }

// ClassifyCommandSandboxed is ClassifyCommand for a command that runs
// confined to the workspace (sandbox mode): deleting local files there is
// no different from overwriting them, so it does not ask. Deletes that
// reach outside (gh repo delete, ...) and push / publish still do.
func ClassifyCommandSandboxed(cmd string) string { return classifyCommand(cmd, true) }

// localDelete marks a segment that only deletes files or refs on this
// machine (rm, find -delete, git clean, ...).
const localDelete = "local-delete"

func classifyCommand(cmd string, sandboxed bool) string {
	best := tools.CategoryExec
	// Two views: quotes as separators (sees `sh -c 'git push'`) and quotes
	// removed (keeps `find . -name '*.o' -delete` in one piece).
	unquoted := strings.NewReplacer("'", "", "\"", "").Replace(cmd)
	for _, seg := range append(segments(cmd, true), segments(unquoted, false)...) {
		c := classifySegment(seg)
		if c == localDelete {
			c = tools.CategoryDelete
			if sandboxed {
				c = tools.CategoryExec
			}
		}
		if severity[c] > severity[best] {
			best = c
		}
	}
	return best
}

// segments splits on command separators and pipes, and also descends into
// $(...) / backtick payloads (and, with quotes, `sh -c '...'` payloads) by
// treating them as separators (over-approximation is fine here).
func segments(cmd string, quotes bool) [][]string {
	seps := []string{"&&", "\n", "||", "\n", ";", "\n", "|", "\n", "&", "\n",
		"$(", "\n", "`", "\n", "(", "\n", ")", "\n"}
	if quotes {
		// with quotes as separators a quoted "%{http_code}" is cut off
		// anyway; braces split { cmd; } groups. Without quotes, braces
		// stay: they are mostly ${VAR} and format strings, and splitting
		// there cut `curl -w "%{http_code}" -X POST …` in two.
		seps = append(seps, "'", "\n", "\"", "\n", "{", "\n", "}", "\n")
	}
	repl := strings.NewReplacer(seps...)
	var out [][]string
	for _, line := range strings.Split(repl.Replace(cmd), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			out = append(out, f)
		}
	}
	return out
}

// strip drops env assignments and wrappers (sudo, env, command, nohup,
// time, xargs, exec) in front of the real program.
func strip(f []string) []string {
	for len(f) > 0 {
		w := f[0]
		switch {
		case strings.Contains(w, "=") && !strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "="):
			f = f[1:]
		case w == "sudo" || w == "env" || w == "command" || w == "nohup" || w == "time" || w == "exec" || w == "xargs" || w == "doas" || w == "builtin":
			f = f[1:]
			for len(f) > 0 && strings.HasPrefix(f[0], "-") {
				f = f[1:]
			}
		default:
			return f
		}
	}
	return f
}

func has(f []string, words ...string) bool {
	for _, x := range f {
		for _, w := range words {
			if x == w {
				return true
			}
		}
	}
	return false
}

// sub returns the first non-flag argument after the program, skipping flag
// values for the given flags (e.g. git -C dir push).
func sub(f []string, valued ...string) (string, []string) {
	for i := 1; i < len(f); i++ {
		a := f[i]
		if strings.HasPrefix(a, "-") {
			for _, v := range valued {
				if a == v {
					i++
				}
			}
			continue
		}
		return a, f[i+1:]
	}
	return "", nil
}

func classifySegment(f []string) string {
	f = strip(f)
	if len(f) == 0 {
		return tools.CategoryExec
	}
	prog := filepath.Base(f[0])
	switch prog {
	case "git":
		s, rest := sub(f, "-C", "-c", "--git-dir", "--work-tree", "--namespace")
		switch s {
		case "push":
			return tools.CategoryPush
		case "clean", "rm":
			return localDelete
		case "send-email":
			return tools.CategoryPublish
		case "branch":
			if has(rest, "-D", "-d", "--delete") {
				return localDelete
			}
		}
	case "gh":
		s, rest := sub(f, "-R", "--repo")
		r, _ := sub(append([]string{s}, rest...))
		switch {
		case s == "pr" && r == "merge":
			return tools.CategoryPush
		case s == "repo" && r == "delete", s == "release" && r == "delete":
			return tools.CategoryDelete
		case s == "pr" && (r == "create" || r == "comment" || r == "review"),
			s == "issue" && (r == "create" || r == "comment"),
			s == "release" && r == "create",
			s == "gist" && r == "create":
			return tools.CategoryPublish
		}
	case "rm", "rmdir", "unlink", "shred", "srm", "trash":
		return localDelete
	case "find":
		if has(f, "-delete") {
			return localDelete
		}
	case "npm", "pnpm", "yarn", "bun":
		s, _ := sub(f)
		switch s {
		case "install", "i", "ci", "add":
			return tools.CategoryInstall
		case "publish":
			return tools.CategoryPublish
		case "":
			if prog == "yarn" {
				return tools.CategoryInstall // bare `yarn` installs
			}
		}
	case "pip", "pip3", "pipx", "uv", "poetry", "conda", "mamba":
		s, rest := sub(f)
		if s == "install" || s == "add" || (s == "pip" && has(rest, "install")) {
			return tools.CategoryInstall
		}
		if s == "publish" {
			return tools.CategoryPublish
		}
	case "python", "python3":
		if has(f, "pip") && has(f, "install") {
			return tools.CategoryInstall
		}
	case "go":
		s, _ := sub(f)
		if s == "get" || s == "install" {
			return tools.CategoryInstall
		}
	case "brew", "apt", "apt-get", "yum", "dnf", "apk", "port", "gem", "cargo", "composer", "pacman":
		s, _ := sub(f)
		switch s {
		case "install", "add", "reinstall", "upgrade", "require":
			return tools.CategoryInstall
		case "publish", "push":
			return tools.CategoryPublish
		}
		if prog == "pacman" && has(f, "-S", "-Syu") {
			return tools.CategoryInstall
		}
	case "twine":
		if s, _ := sub(f); s == "upload" {
			return tools.CategoryPublish
		}
	case "docker", "podman":
		if s, _ := sub(f); s == "push" {
			return tools.CategoryPush
		}
	case "curl":
		if curlSends(f) {
			return tools.CategoryPublish
		}
	case "wget":
		for _, a := range f {
			if strings.HasPrefix(a, "--post-data") || strings.HasPrefix(a, "--post-file") || strings.HasPrefix(a, "--method") {
				return tools.CategoryPublish
			}
		}
	case "http", "https", "xh": // httpie
		if s, _ := sub(f); s == "POST" || s == "PUT" || s == "PATCH" || s == "DELETE" {
			return tools.CategoryPublish
		}
	case "sendmail", "mail", "mailx", "mutt", "msmtp", "swaks":
		return tools.CategoryPublish
	}
	return tools.CategoryExec
}

func curlSends(f []string) bool {
	for i, a := range f {
		switch {
		case a == "-X" || a == "--request":
			if i+1 < len(f) && strings.ToUpper(f[i+1]) != "GET" && strings.ToUpper(f[i+1]) != "HEAD" {
				return true
			}
		case strings.HasPrefix(a, "-X") && len(a) > 2:
			m := strings.ToUpper(a[2:])
			if m != "GET" && m != "HEAD" {
				return true
			}
		case strings.HasPrefix(a, "--request="):
			m := strings.ToUpper(strings.TrimPrefix(a, "--request="))
			if m != "GET" && m != "HEAD" {
				return true
			}
		case a == "-d" || a == "-F" || a == "-T" || strings.HasPrefix(a, "--data") || strings.HasPrefix(a, "--form") ||
			a == "--upload-file" || a == "--json" || strings.HasPrefix(a, "--mail-rcpt") ||
			(strings.HasPrefix(a, "-d") && len(a) > 2) || (strings.HasPrefix(a, "-F") && len(a) > 2):
			return true
		case strings.HasPrefix(a, "smtp://") || strings.HasPrefix(a, "smtps://"):
			return true
		}
	}
	return false
}
