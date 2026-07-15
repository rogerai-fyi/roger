//go:build !windows

package main

import (
	"os"
	"syscall"
)

// execRestart replaces this process with the freshly upgraded binary, preserving
// argv + env - the "restart now" half of the in-TUI upgrade. Unix exec is atomic:
// on success it never returns.
func execRestart() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(self, os.Args, os.Environ())
}
