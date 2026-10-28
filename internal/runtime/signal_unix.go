//go:build unix

package runtime

import (
	"os"
	"syscall"
)

// terminate asks politely: SIGTERM lets the runtime flush and release its port. Stop() insists
// with a kill if this is ignored, because a machine the owner asked to free must be freed.
func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
