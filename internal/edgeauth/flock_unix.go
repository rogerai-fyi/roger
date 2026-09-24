//go:build unix

package edgeauth

import (
	"os"
	"syscall"
)

// flockExclusive takes an advisory exclusive lock on an open file, blocking until it is held. It
// serialises the authority's read-modify-write of claims.json / issued.json / revoked.json ACROSS
// processes - the daemon that answers claims and a `roger edge forget` running beside it - which an
// in-process mutex alone cannot (audit 2026-09-23).
func flockExclusive(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }

func flockUnlock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
