package macos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/capabilities/computer"
)

// Scripts: AppleScript / JXA through osascript(1) and Shortcuts through
// shortcuts(1). The source goes on stdin and names go as argv, never
// through a shell. Every run has a timeout and its output a size cap.
// The policy (asking, blocked apps, no `do shell script`) is the tools'.

const (
	osascriptPath = "/usr/bin/osascript"
	shortcutsPath = "/usr/bin/shortcuts"

	// DefaultScriptTimeout bounds one script or shortcut.
	DefaultScriptTimeout = 30 * time.Second
	// DefaultMaxScriptOutput caps what a script returns (bytes).
	DefaultMaxScriptOutput = 64 << 10
	maxStderr              = 8 << 10
	maxScriptSource        = 256 << 10
)

var _ computer.Scripter = (*Provider)(nil)

// runner runs a program with argv and stdin, keeping at most limit bytes
// of its stdout (cut reports more was written).
type runner func(ctx context.Context, path string, args []string, stdin string, limit int) (out, errOut string, cut bool, err error)

// execRun is the real runner: exec, no shell.
func execRun(ctx context.Context, path string, args []string, stdin string, limit int) (string, string, bool, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = strings.NewReader(stdin)
	stdout, stderr := &capped{max: limit}, &capped{max: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	return stdout.b.String(), stderr.b.String(), stdout.cut, err
}

// capped keeps the first max bytes written to it.
type capped struct {
	b   bytes.Buffer
	max int
	cut bool
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.b.Len(); room < len(p) {
		c.cut = true
		if room > 0 {
			c.b.Write(p[:room])
		}
		return len(p), nil
	}
	c.b.Write(p)
	return len(p), nil
}

// scriptArgs is osascript's argv for lang (the source goes on stdin).
func scriptArgs(lang string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case computer.AppleScript, "":
		return nil, nil
	case computer.JavaScript, "jxa", "js":
		return []string{"-l", "JavaScript"}, nil
	}
	return nil, fmt.Errorf("unknown script language %q (want %s or %s)", lang, computer.AppleScript, computer.JavaScript)
}

// shortcutArgs is shortcuts(1)'s argv to run name, reading input from the
// file in ("" = no input) and writing its output to the file out.
func shortcutArgs(name, in, out string) ([]string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return nil, errors.New("no shortcut name given")
	case strings.HasPrefix(name, "-"):
		return nil, fmt.Errorf("a shortcut name cannot start with %q", "-")
	case strings.ContainsAny(name, "\r\n\x00"):
		return nil, errors.New("a shortcut name is one line")
	}
	args := []string{"run", name}
	if in != "" {
		args = append(args, "--input-path", in)
	}
	return append(args, "--output-path", out), nil
}

// runCapped runs one script program under the provider's timeout and
// turns its failure into an error with its stderr as the text.
func (p *Provider) runCapped(ctx context.Context, what, path string, args []string, stdin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.scriptTimeout)
	defer cancel()
	out, errOut, cut, err := p.run(ctx, path, args, stdin, p.maxOutput)
	if cerr := ctx.Err(); cerr != nil {
		if errors.Is(cerr, context.DeadlineExceeded) {
			return "", fmt.Errorf("%s did not finish within %s and was stopped", what, p.scriptTimeout)
		}
		return "", cerr
	}
	if err != nil {
		return "", scriptError(what, errOut, err)
	}
	return finishOutput(out, cut, p.maxOutput), nil
}

// finishOutput trims the output and notes a cut.
func finishOutput(out string, cut bool, limit int) string {
	out = strings.TrimRight(out, "\r\n")
	if cut {
		for !utf8.ValidString(out) && out != "" {
			out = out[:len(out)-1] // a rune split at the cap
		}
		out += fmt.Sprintf("\n… (output cut at %d bytes)", limit)
	}
	return out
}

// scriptError is a failed run's error: its stderr, with a hint for the
// Automation permission.
func scriptError(what, errOut string, err error) error {
	msg := strings.TrimSpace(errOut)
	if msg == "" {
		msg = err.Error()
	}
	if strings.Contains(msg, "-1743") || strings.Contains(msg, "Not authorized to send Apple events") {
		msg += " (Crewbase is not allowed to control that app: the user can allow it in System Settings › Privacy & Security › Automation)"
	}
	return fmt.Errorf("%s failed: %s", what, msg)
}

// RunScript runs AppleScript or JXA with osascript, the source on stdin.
func (p *Provider) RunScript(ctx context.Context, lang, source string) (string, error) {
	args, err := scriptArgs(lang)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(source) == "" {
		return "", errors.New("the script is empty")
	}
	if len(source) > maxScriptSource {
		return "", fmt.Errorf("the script is longer than %d bytes", maxScriptSource)
	}
	return p.runCapped(ctx, "the script", osascriptPath, args, source)
}

// RunShortcut runs a shortcut by name; input goes through a temporary file.
func (p *Provider) RunShortcut(ctx context.Context, name, input string) (string, error) {
	dir, err := os.MkdirTemp("", "crewbase-shortcut-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	in, out := "", dir+"/output"
	if input != "" {
		in = dir + "/input.txt"
		if err := os.WriteFile(in, []byte(input), 0o600); err != nil {
			return "", err
		}
	}
	args, err := shortcutArgs(name, in, out)
	if err != nil {
		return "", err
	}
	stdout, err := p.runCapped(ctx, "the shortcut", shortcutsPath, args, "")
	if err != nil {
		return "", err
	}
	data, cut, err := readCapped(out, p.maxOutput)
	if err != nil {
		return stdout, nil // the shortcut produced no output file
	}
	if !utf8.Valid(data) {
		return fmt.Sprintf("(the shortcut returned %d bytes that are not text)", len(data)), nil
	}
	res := finishOutput(string(data), cut, p.maxOutput)
	if stdout != "" {
		res = strings.TrimSpace(stdout + "\n" + res)
	}
	return res, nil
}

// readCapped reads at most limit bytes of a file.
func readCapped(path string, limit int) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

// Shortcuts lists the person's shortcuts.
func (p *Provider) Shortcuts(ctx context.Context) ([]string, error) {
	out, err := p.runCapped(ctx, "listing shortcuts", shortcutsPath, []string{"list"}, "")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "… (output cut") {
			names = append(names, l)
		}
	}
	return names, nil
}
