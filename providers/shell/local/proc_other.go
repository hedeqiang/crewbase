//go:build !unix

package local

import (
	"os"
	"os/exec"

	"github.com/hedeqiang/crewbase/internal/winjob"
)

func setGroup(cmd *exec.Cmd) {}

func afterStart(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = winjob.Assign(cmd.Process.Pid)
	}
}

// killGroup kills the process tree (a job object on Windows).
func killGroup(pid int) {
	if winjob.Kill(pid) {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
