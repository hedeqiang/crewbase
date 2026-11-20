//go:build windows

package agentdocs

import (
	"os"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr { return nil }

func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p
	return false // no managed server on Windows (no upstream build)
}
