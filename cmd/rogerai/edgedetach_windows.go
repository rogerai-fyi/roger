//go:build windows

package main

import "os/exec"

// edgeDetach starts the command; Windows process groups differ, so this is a plain start.
func edgeDetach(c *exec.Cmd) error { return c.Start() }
