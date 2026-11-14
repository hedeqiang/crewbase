//go:build unix

package chrome

import (
	"os/exec"
	"syscall"
)

// setGroup starts Chrome in its own process group so its helper processes
// (renderers, GPU, utilities) can be killed with it.
func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree SIGKILLs the process group led by pid.
func killTree(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}
