//go:build windows

// Package winjob keeps a child process and everything it spawns in a
// Windows Job Object, so the whole tree can be killed (process groups do
// not exist on Windows). The job is created with KILL_ON_JOB_CLOSE: even
// if Crewbase itself dies, Windows ends the tree when the handle closes.
//
// The process is assigned right after it starts; anything it spawns in
// that first instant escapes the job (Go's os/exec has no suspended start).
package winjob

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	mu   sync.Mutex
	jobs = map[int]windows.Handle{}
)

// Assign puts process pid (just started) into a new kill-on-close job.
func Assign(pid int) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return err
	}
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(job)
		return err
	}
	defer windows.CloseHandle(p)
	if err := windows.AssignProcessToJobObject(job, p); err != nil {
		windows.CloseHandle(job)
		return err
	}
	mu.Lock()
	jobs[pid] = job
	mu.Unlock()
	return nil
}

// Kill ends pid's whole tree. It reports false when pid has no job (the
// caller then kills the process itself).
func Kill(pid int) bool {
	mu.Lock()
	job, ok := jobs[pid]
	delete(jobs, pid)
	mu.Unlock()
	if !ok {
		return false
	}
	_ = windows.TerminateJobObject(job, 1)
	_ = windows.CloseHandle(job)
	return true
}
