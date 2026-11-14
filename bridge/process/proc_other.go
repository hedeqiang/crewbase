//go:build !unix

package process

import "os/exec"

// Without process groups only the direct child can be killed; a job object
// would be needed on Windows to kill grandchildren.
func setProcessGroup(*exec.Cmd) {}

func terminateTree(cmd *exec.Cmd) { killTree(cmd) }

func killTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
