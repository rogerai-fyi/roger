//go:build unix

package edge

import (
	"os"
	"syscall"
)

// flockExclusive takes an advisory exclusive lock on an open file, blocking until held. It
// serialises the alert log's read-judge-write ACROSS processes: the host judging each pass and a
// `roger edge health` reading beside it (review 2026-09-26).
func flockExclusive(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }

func flockUnlock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
