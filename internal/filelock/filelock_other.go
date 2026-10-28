//go:build !unix && !windows

package filelock

import "os"

// No release ships for these platforms. Without an OS lock the callers' in-process mutexes are
// the only serialisation, so every call succeeds.

// Lock always succeeds here.
func Lock(f *os.File) error { return nil }

// TryLock always succeeds here.
func TryLock(f *os.File) (bool, error) { return true, nil }

// Unlock always succeeds here.
func Unlock(f *os.File) error { return nil }
