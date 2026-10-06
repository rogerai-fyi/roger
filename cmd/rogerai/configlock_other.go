//go:build !unix

package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"
)

// The O_EXCL lock file, for platforms without flock(2) (configlock_unix.go is the OS lock).
//
// lockConfig takes an exclusive lock file (O_EXCL, portable), waiting up to 5 s; a lock
// older than lockStale is a crashed writer's and is taken over.
func lockConfig(path string) (func(), error) {
	token := lockToken()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := f.WriteString(token)
			f.Close()
			if werr != nil {
				_ = os.Remove(path)
				return nil, werr
			}
			return func() {
				// Only our own: a lock held past lockStale may have been taken over.
				if held, err := os.ReadFile(path); err == nil && string(held) == token {
					_ = os.Remove(path)
				}
			}, nil
		}
		if lockIsStale(path) {
			takeOverStaleLock(path) // then the deadline and the pause, like any other wait
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("config.json is locked by another roger command (%s)", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// lockStale is how old a config lock must be before it counts as a crashed writer's.
const lockStale = 30 * time.Second

func lockIsStale(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && time.Since(fi.ModTime()) > lockStale
}

// takeOverStaleLock removes a crashed writer's lock. The decision is serialized by a second
// lock file, and re-checked under it: while a stale lock exists nobody can create a new one,
// and only a takeover removes a stale one, so the lock seen stale here is the crashed one,
// never a fresh holder's. A takeover lock left by a crash is itself cleared after a while.
func takeOverStaleLock(path string) {
	tl := path + ".takeover"
	f, err := os.OpenFile(tl, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if fi, serr := os.Stat(tl); serr == nil && time.Since(fi.ModTime()) > lockStale {
			_ = os.Remove(tl)
		}
		return
	}
	f.Close()
	defer os.Remove(tl)
	if lockIsStale(path) {
		_ = os.Remove(path)
	}
}

// lockToken is a random value naming one holder of the config lock.
func lockToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
