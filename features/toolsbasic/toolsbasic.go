// Package toolsbasic is tools-basic: the model-facing consumers of the
// `shell` and `fs` capabilities — bash, read_file, write_file, edit_file,
// list_dir and delete_file. The tools know nothing about approval or rules;
// they map the call's run mode (Call.Mode) to a sandbox policy / fs access
// and resolve relative paths against Call.Workspace.
package toolsbasic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/fs"
	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/capabilities/shell"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/prompt"
	"github.com/hedeqiang/crewbase/core/tools"
	"github.com/hedeqiang/crewbase/kernel"
)

// Config is the plugin config.
type Config struct {
	// AllowHosts turns on the network allowlist in confined modes (empty =
	// no network in read-only / sandbox modes).
	AllowHosts []string `json:"allowHosts,omitempty"`
	// DefaultTimeout / MaxTimeout bound bash (Go durations; 2m / 10m).
	DefaultTimeout string `json:"defaultTimeout,omitempty"`
	MaxTimeout     string `json:"maxTimeout,omitempty"`
	// MaxReadLines caps read_file output lines (default 2000).
	MaxReadLines int `json:"maxReadLines,omitempty"`
	// MaxResultBytes caps any tool result shown to the model (default 100KiB).
	MaxResultBytes int `json:"maxResultBytes,omitempty"`
}

type basic struct {
	sh      shell.Shell
	fsys    fs.FS
	hosts   []string
	timeout time.Duration
	maxTime time.Duration
	lines   int
	maxRes  int
	jobs    *jobs
}

// EffectiveMode normalizes a call's run mode: "" is the default (sandbox);
// anything unknown fails closed to read-only.
func EffectiveMode(m string) string { return sandbox.EffectiveRunMode(m) }

// PolicyFor maps a call to its sandbox policy.
func PolicyFor(call tools.Call, allowHosts []string) sandbox.Policy {
	return sandbox.PolicyFor(EffectiveMode(call.Mode), call.Workspace, allowHosts)
}

// AccessFor maps a call to its fs access: host mode is unconfined, the
// other modes are confined to the workspace (read-only in read-only mode).
func AccessFor(call tools.Call) (fs.Access, error) {
	a := fs.Access{Agent: call.Agent}
	switch EffectiveMode(call.Mode) {
	case agent.ModeHost:
		return a, nil
	case agent.ModeReadOnly:
		a.ReadOnly = true
	}
	if call.Workspace == "" || !filepath.IsAbs(call.Workspace) {
		return a, errors.New("no workspace is set for this agent; file access is not allowed in this mode")
	}
	a.Roots = []string{call.Workspace}
	return a, nil
}

// ResolvePath resolves p against the call's workspace. Absolute paths are
// kept; `~` is not expanded (it would escape the workspace silently).
func ResolvePath(call tools.Call, p string) (string, error) {
	if p == "" {
		p = "."
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	if call.Workspace == "" {
		return "", fmt.Errorf("relative path %q needs a workspace", p)
	}
	return filepath.Join(call.Workspace, p), nil
}

func (b *basic) clip(s string) (string, bool) {
	if len(s) <= b.maxRes {
		return s, false
	}
	return s[:b.maxRes] + fmt.Sprintf("\n…[output truncated, %d bytes total]", len(s)), true
}

// ---------------------------------------------------------------- bash

func (b *basic) bashTool() *tools.Tool {
	return &tools.Tool{
		Name: "bash",
		Description: "Run a shell command (/bin/sh -c) in the agent workspace and return its exit code and output. " +
			"In read-only and sandbox modes the command runs inside a sandbox: writes outside the workspace and network access are blocked. " +
			"If the result says the sandbox blocked the command, do not retry; ask the user for a wider run mode. " +
			"For anything long-running (builds, test suites, dev servers, downloads) pass background: true: it returns at once, " +
			"you keep working, and you get a message when it ends (job_output / job_stop / job_list).",
		Params: tools.Schema(map[string]any{
			"command":         tools.Prop("string", "The command line to run."),
			"cwd":             tools.Prop("string", "Working directory (relative to the workspace). Default: the workspace."),
			"timeout_seconds": tools.Prop("integer", "Timeout in seconds (default 120; background jobs 1800, at most 7200)."),
			"background":      tools.Prop("boolean", "Run in the background and return at once (default false)."),
		}, "command"),
		Category: tools.CategoryExec,
		PathArgs: []string{"cwd"},
		Classify: func(call tools.Call) (string, string) {
			cmd := call.String("command")
			return ClassifyCommand(cmd), "bash: " + oneLine(cmd, 200)
		},
		Execute: b.bash,
	}
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

func (b *basic) bash(ctx context.Context, call tools.Call) (tools.Result, error) {
	var args struct {
		Command    string  `json:"command"`
		Cwd        string  `json:"cwd"`
		Timeout    float64 `json:"timeout_seconds"`
		Background bool    `json:"background"`
	}
	if err := call.Decode(&args); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	if strings.TrimSpace(args.Command) == "" {
		return tools.Errorf("command is empty"), nil
	}
	mode := EffectiveMode(call.Mode)
	cwd := args.Cwd
	if cwd == "" {
		cwd = call.Workspace
	}
	if cwd == "" {
		return tools.Errorf("no workspace is set; pass an absolute cwd"), nil
	}
	cwd, err := ResolvePath(call, cwd)
	if err != nil {
		return tools.Errorf("%v", err), nil
	}
	if mode != agent.ModeHost && call.Workspace == "" {
		return tools.Errorf("no workspace is set for this agent; commands are not allowed in %s mode", mode), nil
	}
	if args.Background {
		timeout := DefaultJobTimeout
		if args.Timeout > 0 {
			timeout = min(time.Duration(args.Timeout*float64(time.Second)), MaxJobTimeout)
		}
		return b.jobs.start(call, args.Command, shell.ExecRequest{
			Command: args.Command, Cwd: cwd, Timeout: timeout, Policy: PolicyFor(call, b.hosts),
		})
	}
	timeout := b.timeout
	if args.Timeout > 0 {
		timeout = time.Duration(args.Timeout * float64(time.Second))
	}
	if timeout > b.maxTime {
		timeout = b.maxTime
	}
	res, err := b.sh.Exec(ctx, shell.ExecRequest{
		Command: args.Command, Cwd: cwd, Timeout: timeout,
		Policy: PolicyFor(call, b.hosts),
	})
	if err != nil {
		if errors.Is(err, sandbox.ErrUnavailable) {
			return tools.Result{IsError: true, Content: fmt.Sprintf("%v\nThe command was NOT run. The sandbox backend is unavailable; tell the user (e.g. start Docker) instead of retrying.", err),
				Data: map[string]any{"code": sandbox.CodeUnavailable}}, nil
		}
		return tools.Errorf("command could not be run: %v", err), nil
	}
	return b.formatRun(res), nil
}

func (b *basic) formatRun(r shell.RunResult) tools.Result {
	var s strings.Builder
	switch {
	case r.TimedOut:
		fmt.Fprintf(&s, "Command timed out after %s and was killed.\n", r.Duration.Round(time.Millisecond))
	case r.Cancelled:
		s.WriteString("Command was cancelled.\n")
	default:
		fmt.Fprintf(&s, "Exit code: %d\n", r.ExitCode)
	}
	if r.Stdout != "" {
		s.WriteString("<stdout>\n" + r.Stdout)
		if !strings.HasSuffix(r.Stdout, "\n") {
			s.WriteString("\n")
		}
		s.WriteString("</stdout>\n")
	}
	if r.Stderr != "" {
		s.WriteString("<stderr>\n" + r.Stderr)
		if !strings.HasSuffix(r.Stderr, "\n") {
			s.WriteString("\n")
		}
		s.WriteString("</stderr>\n")
	}
	if r.Truncated {
		s.WriteString("(output was truncated)\n")
	}
	if r.Blocked {
		s.WriteString("The sandbox blocked an operation of this command (it is not a bug in the command). Do not retry it as is; " +
			"stay inside the workspace without network, or ask the user to switch to a wider run mode.\n")
	}
	if r.Enforcement == sandbox.EnforcementPartial {
		s.WriteString("Note: sandbox enforcement is partial")
		if len(r.Limits) > 0 {
			s.WriteString(" (" + strings.Join(r.Limits, "; ") + ")")
		}
		s.WriteString(".\n")
	}
	out, _ := b.clip(s.String())
	return tools.Result{Content: out, IsError: r.ExitCode != 0 || r.TimedOut || r.Cancelled, Data: r}
}

// ---------------------------------------------------------------- files

func (b *basic) fileCtx(call tools.Call, p string) (fs.Access, string, error) {
	a, err := AccessFor(call)
	if err != nil {
		return a, "", err
	}
	abs, err := ResolvePath(call, p)
	return a, abs, err
}

func (b *basic) readTool() *tools.Tool {
	return &tools.Tool{
		Name:        "read_file",
		Description: "Read a text file. Returns numbered lines (`N<TAB>text`). Use offset/limit for large files. A file must be read before it can be edited or overwritten.",
		Params: tools.Schema(map[string]any{
			"path":   tools.Prop("string", "File path, relative to the workspace or absolute."),
			"offset": tools.Prop("integer", "First line to return (1-based, default 1)."),
			"limit":  tools.Prop("integer", "Maximum number of lines (default 2000)."),
		}, "path"),
		Category: tools.CategoryRead,
		PathArgs: []string{"path"},
		Classify: func(call tools.Call) (string, string) { return tools.CategoryRead, "read " + call.String("path") },
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var args struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := call.Decode(&args); err != nil {
				return tools.Errorf("invalid arguments: %v", err), nil
			}
			a, p, err := b.fileCtx(call, args.Path)
			if err != nil {
				return tools.Errorf("%v", err), nil
			}
			f, err := b.fsys.Read(ctx, a, p)
			if err != nil {
				return tools.Errorf("read_file: %v", err), nil
			}
			head := f.Content
			if len(head) > 8192 {
				head = head[:8192]
			}
			if bytes.IndexByte(head, 0) >= 0 {
				return tools.Text(fmt.Sprintf("%s is a binary file (%d bytes); not shown.", args.Path, f.Size)), nil
			}
			return tools.Text(b.numbered(string(f.Content), args.Offset, args.Limit)), nil
		},
	}
}

func (b *basic) numbered(content string, offset, limit int) string {
	if content == "" {
		return "(empty file)"
	}
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 || limit > b.lines {
		limit = b.lines
	}
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if offset > len(lines) {
		return fmt.Sprintf("(offset %d is past the end: the file has %d lines)", offset, len(lines))
	}
	end := min(offset-1+limit, len(lines))
	var s strings.Builder
	for i := offset - 1; i < end; i++ {
		fmt.Fprintf(&s, "%6d\t%s", i+1, strings.TrimRight(lines[i], "\r\n")+"\n")
		if s.Len() > b.maxRes {
			fmt.Fprintf(&s, "…[stopped at line %d: output limit; use offset to continue]\n", i+1)
			return s.String()
		}
	}
	if end < len(lines) {
		fmt.Fprintf(&s, "…[%d more lines; use offset=%d to continue]\n", len(lines)-end, end+1)
	}
	return s.String()
}

func (b *basic) writeTool() *tools.Tool {
	return &tools.Tool{
		Name:        "write_file",
		Description: "Create or overwrite a file with the given content (parent directories are created). Overwriting an existing file requires reading it first.",
		Params: tools.Schema(map[string]any{
			"path":    tools.Prop("string", "File path, relative to the workspace or absolute."),
			"content": tools.Prop("string", "Full file content."),
		}, "path", "content"),
		Category: tools.CategoryWrite,
		PathArgs: []string{"path"},
		Classify: func(call tools.Call) (string, string) { return tools.CategoryWrite, "write " + call.String("path") },
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var args struct{ Path, Content string }
			if err := call.Decode(&args); err != nil {
				return tools.Errorf("invalid arguments: %v", err), nil
			}
			a, p, err := b.fileCtx(call, args.Path)
			if err != nil {
				return tools.Errorf("%v", err), nil
			}
			r, err := b.fsys.Write(ctx, a, p, []byte(args.Content))
			if err != nil {
				return tools.Errorf("write_file: %v", err), nil
			}
			verb := "Updated"
			if r.Created {
				verb = "Created"
			}
			return tools.Result{Content: fmt.Sprintf("%s %s (%d bytes).", verb, args.Path, r.Bytes), Data: r}, nil
		},
	}
}

func (b *basic) editTool() *tools.Tool {
	return &tools.Tool{
		Name: "edit_file",
		Description: "Replace one exact occurrence of old_string with new_string in a file. old_string must match exactly (including whitespace) and occur exactly once; " +
			"the file must have been read since it last changed.",
		Params: tools.Schema(map[string]any{
			"path":       tools.Prop("string", "File path, relative to the workspace or absolute."),
			"old_string": tools.Prop("string", "Exact text to replace."),
			"new_string": tools.Prop("string", "Replacement text."),
		}, "path", "old_string", "new_string"),
		Category: tools.CategoryWrite,
		PathArgs: []string{"path"},
		Classify: func(call tools.Call) (string, string) { return tools.CategoryWrite, "edit " + call.String("path") },
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var args struct {
				Path string `json:"path"`
				Old  string `json:"old_string"`
				New  string `json:"new_string"`
			}
			if err := call.Decode(&args); err != nil {
				return tools.Errorf("invalid arguments: %v", err), nil
			}
			a, p, err := b.fileCtx(call, args.Path)
			if err != nil {
				return tools.Errorf("%v", err), nil
			}
			r, err := b.fsys.Edit(ctx, a, p, args.Old, args.New)
			if err != nil {
				return tools.Errorf("edit_file: %v", err), nil
			}
			return tools.Result{Content: fmt.Sprintf("Edited %s.", args.Path), Data: r}, nil
		},
	}
}

const maxListEntries = 1000

func (b *basic) listTool() *tools.Tool {
	return &tools.Tool{
		Name:        "list_dir",
		Description: "List a directory (not recursive). Directories end with '/'.",
		Params: tools.Schema(map[string]any{
			"path": tools.Prop("string", "Directory, relative to the workspace or absolute. Default: the workspace."),
		}),
		Category: tools.CategoryRead,
		PathArgs: []string{"path"},
		Classify: func(call tools.Call) (string, string) { return tools.CategoryRead, "list " + call.String("path") },
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var args struct{ Path string }
			if err := call.Decode(&args); err != nil {
				return tools.Errorf("invalid arguments: %v", err), nil
			}
			a, p, err := b.fileCtx(call, args.Path)
			if err != nil {
				return tools.Errorf("%v", err), nil
			}
			ents, err := b.fsys.List(ctx, a, p)
			if err != nil {
				return tools.Errorf("list_dir: %v", err), nil
			}
			if len(ents) == 0 {
				return tools.Text("(empty directory)"), nil
			}
			var s strings.Builder
			for i, e := range ents {
				if i == maxListEntries {
					fmt.Fprintf(&s, "…[%d more entries]\n", len(ents)-i)
					break
				}
				switch {
				case e.IsDir:
					s.WriteString(e.Name + "/\n")
				case e.Symlink:
					s.WriteString(e.Name + "@\n")
				default:
					fmt.Fprintf(&s, "%s\t%d\n", e.Name, e.Size)
				}
			}
			out, _ := b.clip(s.String())
			return tools.Text(out), nil
		},
	}
}

func (b *basic) deleteTool() *tools.Tool {
	return &tools.Tool{
		Name:        "delete_file",
		Description: "Delete a file, or a directory when recursive is true.",
		Params: tools.Schema(map[string]any{
			"path":      tools.Prop("string", "Path, relative to the workspace or absolute."),
			"recursive": tools.Prop("boolean", "Delete a directory and its contents."),
		}, "path"),
		Category: tools.CategoryDelete,
		PathArgs: []string{"path"},
		Classify: func(call tools.Call) (string, string) { return tools.CategoryDelete, "delete " + call.String("path") },
		Execute: func(ctx context.Context, call tools.Call) (tools.Result, error) {
			var args struct {
				Path      string `json:"path"`
				Recursive bool   `json:"recursive"`
			}
			if err := call.Decode(&args); err != nil {
				return tools.Errorf("invalid arguments: %v", err), nil
			}
			a, p, err := b.fileCtx(call, args.Path)
			if err != nil {
				return tools.Errorf("%v", err), nil
			}
			if err := b.fsys.Delete(ctx, a, p, args.Recursive); err != nil {
				return tools.Errorf("delete_file: %v", err), nil
			}
			return tools.Text("Deleted " + args.Path + "."), nil
		},
	}
}

// Plugin registers the basic tools on the `tools` registry.
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/tools-basic",
	Description: "bash, read_file, write_file, edit_file, list_dir, delete_file (consumers of shell + fs).",
	Inject:      []string{tools.ServiceKey, shell.ServiceKey, fs.ServiceKey},
	Apply: func(c *kernel.Context) error {
		cfg, err := kernel.Decode[Config](c)
		if err != nil {
			return err
		}
		b := &basic{
			sh:      kernel.Use[shell.Shell](c, shell.ServiceKey),
			fsys:    kernel.Use[fs.FS](c, fs.ServiceKey),
			hosts:   cfg.AllowHosts,
			timeout: 2 * time.Minute, maxTime: 10 * time.Minute,
			lines: cfg.MaxReadLines, maxRes: cfg.MaxResultBytes,
		}
		if cfg.DefaultTimeout != "" {
			if b.timeout, err = time.ParseDuration(cfg.DefaultTimeout); err != nil {
				return fmt.Errorf("tools-basic: defaultTimeout: %w", err)
			}
		}
		if cfg.MaxTimeout != "" {
			if b.maxTime, err = time.ParseDuration(cfg.MaxTimeout); err != nil {
				return fmt.Errorf("tools-basic: maxTimeout: %w", err)
			}
		}
		if b.lines <= 0 {
			b.lines = 2000
		}
		if b.maxRes <= 0 {
			b.maxRes = 100 << 10
		}
		b.jobs = &jobs{b: b, c: c, all: map[string]*job{}}
		reg := kernel.Use[*tools.Registry](c, tools.ServiceKey)
		for _, t := range append([]*tools.Tool{b.bashTool(), b.readTool(), b.writeTool(), b.editTool(), b.listTool(), b.deleteTool()}, b.jobs.tools()...) {
			reg.Register(c, t)
		}
		agent.Deleted.Listen(c, b.jobs.stopAll)
		if pa, ok := kernel.Lookup[*prompt.Assembler](c, prompt.ServiceKey); ok {
			env := Environment(c)
			pa.Register(c, &prompt.Fragment{Name: "environment", Order: 12, Render: func(string) string { return env }})
		}
		return nil
	},
})

// Environment tells the model where its commands run, so it uses the
// right tools the first time (BSD `stat -f` on macOS, not GNU `stat -c`).
func Environment(c *kernel.Context) string {
	host := map[string]string{"darwin": "macOS", "linux": "Linux", "windows": "Windows"}[runtime.GOOS]
	if host == "" {
		host = runtime.GOOS
	}
	userland := ""
	switch runtime.GOOS {
	case "darwin":
		userland = " with BSD userland (e.g. `stat -f`, `md5`, `sed -i ''`; no GNU coreutils unless installed)"
	case "linux":
		userland = " with GNU userland"
	}
	s := fmt.Sprintf("Commands (bash) run with /bin/sh on %s %s%s.", host, runtime.GOARCH, userland)
	if sb, ok := kernel.Lookup[sandbox.Sandbox](c, sandbox.ServiceKey); ok && sb.Name() == "docker" {
		s = fmt.Sprintf("In read-only and sandbox modes commands (bash) run with /bin/sh inside a Linux container with the workspace mounted; in host mode they run on %s %s%s.", host, runtime.GOARCH, userland)
	}
	return s
}
