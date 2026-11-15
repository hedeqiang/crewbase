//go:build !windows

// Package winjob is a no-op outside Windows (process groups are used there).
package winjob

// Assign does nothing on this platform.
func Assign(int) error { return nil }

// Kill reports false: the caller kills the process group itself.
func Kill(int) bool { return false }
