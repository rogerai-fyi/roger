//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// edgeDetach starts the command in its OWN session (setsid), so a resumed agent outlives the
// shell that launched it (features/edge/agents.feature - resume is a plain, detached roger).
func edgeDetach(c *exec.Cmd) error {
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return c.Start()
}
