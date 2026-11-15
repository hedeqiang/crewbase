//go:build !unix

package process

import (
	"os/exec"

	"github.com/hedeqiang/crewbase/internal/winjob"
)

// No process groups here: on Windows the tree lives in a job object
// (assigned in afterStart) and is killed as a whole.
func setProcessGroup(*exec.Cmd) {}

func afterStart(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = winjob.Assign(cmd.Process.Pid)
	}
}

func terminateTree(cmd *exec.Cmd) { killTree(cmd) }

func killTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		if !winjob.Kill(cmd.Process.Pid) {
			_ = cmd.Process.Kill()
		}
	}
}
