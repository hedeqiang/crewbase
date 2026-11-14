//go:build unix

package mcp

import (
	"os/exec"
	"syscall"
)

// setGroup puts the server in its own process group so the whole tree can
// be killed at once.
func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalGroup(pid int, sig syscall.Signal) {
	if pid > 0 {
		_ = syscall.Kill(-pid, sig)
	}
}

// killGroup SIGKILLs the process group led by pid.
func killGroup(pid int) { signalGroup(pid, syscall.SIGKILL) }
