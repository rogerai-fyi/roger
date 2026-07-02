//go:build !windows

package onair

import (
	"os"
	"syscall"
)

// ProcessAlive reports whether a process with the given PID is currently running.
// Signal 0 performs the kernel's permission/existence check without delivering a
// signal: nil means the process exists (ESRCH => gone, EPERM => exists but ours to
// not touch, still "alive").
func ProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return err == syscall.EPERM
}
