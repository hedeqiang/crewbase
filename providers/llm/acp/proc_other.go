//go:build !unix

package acp

import (
	"os/exec"

	"github.com/hedeqiang/crewbase/internal/winjob"
)

func setProcessGroup(*exec.Cmd) {}

func afterStart(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = winjob.Assign(cmd.Process.Pid)
	}
}

// killGroup kills the process tree (a job object on Windows).
func killGroup(cmd *exec.Cmd, _ bool) {
	if cmd.Process != nil && !winjob.Kill(cmd.Process.Pid) {
		_ = cmd.Process.Kill()
	}
}
