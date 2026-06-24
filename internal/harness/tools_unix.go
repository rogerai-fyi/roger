//go:build !windows

package harness

import (
	"context"
	"os/exec"
)

// shellArgv returns the executable + args used to run a run_shell command on
// non-Windows platforms: /bin/sh -c <cmd>. Split out (and unit-testable) from
// shellCommand so the platform selection can be asserted without execing.
func shellArgv(cmd string) (name string, args []string) {
	return "/bin/sh", []string{"-c", cmd}
}

// shellCommand builds the bounded run_shell exec for this platform. The shell
// wrapper is internal; the confirm gate previews the literal user command.
func shellCommand(ctx context.Context, cmd string) *exec.Cmd {
	name, args := shellArgv(cmd)
	return exec.CommandContext(ctx, name, args...)
}
