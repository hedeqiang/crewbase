//go:build !unix

package chrome

import (
	"os"
	"os/exec"
	"strconv"
)

func setGroup(cmd *exec.Cmd) {}

// killTree kills pid and its children.
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run(); err != nil {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
}
