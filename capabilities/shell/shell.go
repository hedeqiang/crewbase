// Package shell defines the `shell` capability: running one command to
// completion and returning its captured output. Providers (shell-local,
// later shell-docker / shell-ssh) implement Shell; consumers are model-facing
// tools such as `bash`. The sandbox policy is carried by every request.
package shell

import (
	"context"
	"errors"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/sandbox"
)

// ServiceKey is the service key of the shell capability.
const ServiceKey = "shell"

// ExecRequest is one command execution.
type ExecRequest struct {
	// Argv runs a program directly. Command, if Argv is empty, runs through
	// the provider's shell (`/bin/sh -c`).
	Argv    []string       `json:"argv,omitempty"`
	Command string         `json:"command,omitempty"`
	Cwd     string         `json:"cwd"`
	Env     []string       `json:"env,omitempty"` // added to the provider's base env
	Timeout time.Duration  `json:"timeout,omitempty"`
	Policy  sandbox.Policy `json:"policy"`
	// MaxOutput caps each of stdout/stderr in bytes (0 = provider default).
	MaxOutput int `json:"maxOutput,omitempty"`
	// Output, when set, also receives the output as it is produced
	// (stream "stdout" or "stderr"), e.g. for background jobs. It must not
	// block; RunResult still carries the capped output.
	Output func(stream string, p []byte) `json:"-"`
}

// RunResult is the outcome of a command that ran (possibly unsuccessfully).
type RunResult struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exitCode"` // -1 if killed / no exit status
	TimedOut  bool   `json:"timedOut,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// Blocked means the output matched one of the sandbox's blocked
	// signatures: the sandbox refused something, so the agent should ask for
	// more permission rather than retry.
	Blocked     bool          `json:"blocked,omitempty"`
	Enforcement string        `json:"enforcement"` // full | partial | none
	Limits      []string      `json:"limits,omitempty"`
	Duration    time.Duration `json:"duration"`
}

// Shell is the `shell` service.
//
// Exec returns an error only when the command could not be run at all
// (sandbox unavailable — errors.Is(err, sandbox.ErrUnavailable) — bad
// request, start failure). A non-zero exit, a timeout or a cancellation is a
// RunResult.
type Shell interface {
	Exec(ctx context.Context, req ExecRequest) (RunResult, error)
}

// ErrEmpty is returned for a request with neither Argv nor Command.
var ErrEmpty = errors.New("shell: empty command")
