//go:build !windows

package agentdocs

import "syscall"

// sysProcAttr puts the server in its own process group (Ctrl-C in a
// terminal running crewd does not reach it; stopLocal stops it).
func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// alive reports whether process pid still runs (a zombie counts as gone).
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	var ws syscall.WaitStatus
	got, _ := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	return got == 0
}
