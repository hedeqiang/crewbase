//go:build !windows

package agentdocs

import "syscall"

// sysProcAttr puts the server in its own process group (Ctrl-C in a
// terminal running crewd does not reach it; stopLocal stops it).
func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
