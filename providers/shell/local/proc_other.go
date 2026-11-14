//go:build !unix

package local

import (
	"os"
	"os/exec"
)

func setGroup(cmd *exec.Cmd) {}

func killGroup(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
