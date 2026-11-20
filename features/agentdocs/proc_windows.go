//go:build windows

package agentdocs

import "syscall"

func sysProcAttr() *syscall.SysProcAttr { return nil }
