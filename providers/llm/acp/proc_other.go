//go:build !unix

package acp

import "os/exec"

func setProcessGroup(*exec.Cmd) {}

// killGroup kills only the direct child on platforms without process groups.
func killGroup(cmd *exec.Cmd, _ bool) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
