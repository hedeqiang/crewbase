// Package loginpath gives a GUI-launched process the PATH of the person's
// login shell. Apps opened from Finder, the Dock or a desktop menu start
// with the system's minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin on macOS),
// so command-line tools installed with Homebrew, npm, pipx, uv, bun, mise
// and the like — Claude Code, Codex, Gemini CLI, npx, uvx — are not found.
//
// Fix asks the login shell for its PATH ($SHELL -ilc, with a timeout and
// markers around the value, since interactive shells may print banners),
// then appends the usual install folders that exist, and sets PATH for
// this process, so everything it starts inherits it. Nothing on Windows.
package loginpath

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	begin = "__CREWBASE_PATH_BEGIN__"
	end   = "__CREWBASE_PATH_END__"
)

// Timeout bounds how long the login shell may take.
var Timeout = 4 * time.Second

// Fix merges the login shell's PATH and common install folders into PATH
// and returns the result. It never fails: without a usable shell, PATH
// only gains the folders that exist.
func Fix() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("PATH")
	}
	home, _ := os.UserHomeDir()
	p := Merge(fromShell(shellPath()), os.Getenv("PATH"), existing(fallback(home)))
	_ = os.Setenv("PATH", p)
	return p
}

func shellPath() string {
	if s := os.Getenv("SHELL"); s != "" && filepath.IsAbs(s) {
		return s
	}
	if runtime.GOOS == "darwin" {
		return "/bin/zsh"
	}
	return "/bin/sh"
}

// fromShell runs the login shell once and returns its PATH ("" on any
// failure or timeout).
func fromShell(sh string) string {
	if _, err := os.Stat(sh); err != nil {
		return ""
	}
	script := `printf '` + begin + `%s` + end + `' "$PATH"`
	if filepath.Base(sh) == "fish" {
		script = `printf '` + begin + `%s` + end + `' (string join : $PATH)`
	}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sh, "-ilc", script)
	// stdin stays /dev/null: nothing can wait for input. Keep shell
	// frameworks from prompting or updating themselves.
	cmd.Env = append(os.Environ(), "DISABLE_AUTO_UPDATE=true", "ZSH_DISABLE_COMPFIX=true", "TERM=dumb")
	cmd.WaitDelay = time.Second
	out, _ := cmd.Output() // a noisy rc file may exit non-zero after printing PATH
	return Parse(out)
}

// Parse extracts the PATH printed between the markers.
func Parse(out []byte) string {
	i := bytes.LastIndex(out, []byte(begin))
	if i < 0 {
		return ""
	}
	v, _, ok := bytes.Cut(out[i+len(begin):], []byte(end))
	if !ok {
		return ""
	}
	return strings.TrimSpace(string(v))
}

// fallback lists folders tools are commonly installed into.
func fallback(home string) []string {
	dirs := []string{"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin"}
	if home != "" {
		for _, d := range []string{
			".local/bin", "bin", ".npm-global/bin", ".bun/bin", ".volta/bin", ".deno/bin",
			".cargo/bin", "go/bin", ".asdf/shims", ".local/share/mise/shims", ".local/share/pnpm",
			"Library/pnpm", ".claude/local", ".opencode/bin",
		} {
			dirs = append(dirs, filepath.Join(home, d))
		}
	}
	return dirs
}

func existing(dirs []string) string {
	var out []string
	for _, d := range dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			out = append(out, d)
		}
	}
	return strings.Join(out, string(os.PathListSeparator))
}

// Merge joins PATH lists in order, dropping empty and repeated entries.
func Merge(lists ...string) string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, d := range filepath.SplitList(l) {
			d = strings.TrimSpace(d)
			if d == "" || seen[d] {
				continue
			}
			seen[d] = true
			out = append(out, d)
		}
	}
	return strings.Join(out, string(os.PathListSeparator))
}
