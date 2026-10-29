//go:build unix

package filelock

import (
	"errors"
	"os"
	"syscall"
)

// Lock takes an exclusive lock on f, blocking until it is held.
func Lock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// TryLock takes an exclusive lock on f without waiting. It reports false, with no error, when
// another holder has the lock.
func TryLock(f *os.File) (bool, error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, syscall.EWOULDBLOCK):
			return false, nil
		case errors.Is(err, syscall.EINTR):
			continue
		default:
			return false, err
		}
	}
}

// Unlock releases a lock taken by Lock or TryLock.
func Unlock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
