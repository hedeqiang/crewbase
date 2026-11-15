//go:build !unix

package mcp

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/hedeqiang/crewbase/internal/winjob"
)

func setGroup(*exec.Cmd) {}

func afterStart(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = winjob.Assign(cmd.Process.Pid)
	}
}

func signalGroup(pid int, _ syscall.Signal) { killGroup(pid) }

// killGroup kills the process tree (a job object on Windows).
func killGroup(pid int) {
	if winjob.Kill(pid) {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
