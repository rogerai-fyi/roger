//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// edgeDetach starts the command in its own process group, detached from this console, so a spawned
// agent outlives the shell that launched it instead of dying with the console (audit 2026-09-24).
func edgeDetach(c *exec.Cmd) error {
	const detachedProcess = 0x00000008
	const createNewProcessGroup = 0x00000200
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | createNewProcessGroup}
	return c.Start()
}
