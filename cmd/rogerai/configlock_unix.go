//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// lockConfig takes an exclusive OS lock (flock) on path, waiting up to 5 s. The kernel drops
// the lock when its holder exits, crashed or not, so there is no stale-lock takeover and no
// window in which two writers both hold it. The file itself is left in place: removing a
// flock'd file would let a newcomer lock a fresh inode while another waiter holds the old one.
func lockConfig(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
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
