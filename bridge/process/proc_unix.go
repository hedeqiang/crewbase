//go:build unix

package process

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the plugin in its own process group so the whole
// tree can be signalled at once.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalTree(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}

// terminateTree asks the process tree to exit (SIGTERM).
func terminateTree(cmd *exec.Cmd) { signalTree(cmd, syscall.SIGTERM) }

// killTree kills the process tree (SIGKILL).
func killTree(cmd *exec.Cmd) { signalTree(cmd, syscall.SIGKILL) }

func afterStart(*exec.Cmd) {}
