//go:build windows

package main

import "syscall"

// processAlive reports whether a process with the given PID is running on Windows,
// where signal 0 isn't available. Open a query handle and check the exit code:
// STILL_ACTIVE means it's still running. A handle that won't open is treated as gone.
func processAlive(pid int) bool {
	const (
		processQueryLimitedInformation = 0x1000
		stillActive                    = 259
	)
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return true // opened but couldn't query - assume it's alive
	}
	return code == stillActive
}
