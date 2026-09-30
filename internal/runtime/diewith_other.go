//go:build !linux

package runtime

import "os/exec"

// dieWithParent has no kernel-level equivalent outside Linux. Stop() still ends the runtime in
// the orderly case; a roger KILLED outright on these platforms can leave the runtime behind, and
// saying that plainly is better than pretending otherwise.
func dieWithParent(cmd *exec.Cmd) {}
