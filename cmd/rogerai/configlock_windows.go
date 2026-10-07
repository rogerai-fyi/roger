//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// lockConfig takes an exclusive OS lock (LockFileEx) on path, waiting up to 5 s. As with
// flock on Unix, Windows releases the lock when its holder exits, crashed or not, so there is
// no stale-lock takeover and no window in which two writers both hold it. The file is left in
// place, for the same reason as on Unix.
func lockConfig(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(f.Fd())
	deadline := time.Now().Add(5 * time.Second)
	for {
		ol := new(windows.Overlapped)
		err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
		if err == nil {
			return func() {
				_ = windows.UnlockFileEx(h, 0, 1, 0, new(windows.Overlapped))
				f.Close()
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("config.json is locked by another roger command (%s)", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
