//go:build !unix

package runtime

import "os"

// terminate has no polite form on this platform, so it is the insistent one. Stop() reports that
// it had to insist, which stays true here.
func terminate(p *os.Process) error { return p.Kill() }
