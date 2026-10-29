//go:build unix || windows

package main

import (
	"fmt"
	"os"
	"time"

	"rogerai.fm/roger/v7/internal/filelock"
)

// lockConfig takes an exclusive OS lock (internal/filelock: flock on Unix, LockFileEx on
// Windows) on path, waiting up to 5 s. The OS drops the lock when its holder exits, crashed or
// not, so there is no stale-lock takeover and no window in which two writers both hold it. The
// file itself is left in place: removing a locked file would let a newcomer lock a fresh inode
// while another waiter holds the old one.
func lockConfig(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		held, err := filelock.TryLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if held {
			return func() {
				_ = filelock.Unlock(f)
				f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("config.json is locked by another roger command (%s)", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
