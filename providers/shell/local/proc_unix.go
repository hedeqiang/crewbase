//go:build unix

package local

import (
	"os/exec"
	"syscall"
)

// setGroup puts the child in its own process group so the whole tree can be
// killed at once.
func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup SIGKILLs the process group led by pid.
func killGroup(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}
