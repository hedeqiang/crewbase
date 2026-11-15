//go:build unix

package acp

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the agent in its own process group so that npx/node
// wrappers and the CLI they spawn are stopped together.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup signals the agent's whole process group (SIGTERM, or SIGKILL
// when hard).
func killGroup(cmd *exec.Cmd, hard bool) {
	if cmd.Process == nil {
		return
	}
	sig := syscall.SIGTERM
	if hard {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}

func afterStart(*exec.Cmd) {}
