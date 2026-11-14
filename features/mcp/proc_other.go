//go:build !unix

package mcp

import (
	"os"
	"os/exec"
	"syscall"
)

func setGroup(*exec.Cmd) {}

func signalGroup(pid int, _ syscall.Signal) { killGroup(pid) }

// killGroup kills the process (no process groups on this platform).
func killGroup(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
