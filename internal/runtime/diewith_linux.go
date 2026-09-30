//go:build linux

package runtime

import (
	"os/exec"
	"syscall"
)

// dieWithParent asks the KERNEL to end this child when roger dies, however roger dies.
//
// Stop() handles the orderly case. This handles the one that actually leaves gigabytes resident:
// roger killed outright, or crashing, with no chance to run anything. Without it the runtime
// keeps the port and the memory and there is nobody left who knows its pid.
//
// It is the opposite of what the Edge agents do, and deliberately: an agent should outlive the
// shell that launched it, and an inference runtime must not outlive its owner.
func dieWithParent(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
