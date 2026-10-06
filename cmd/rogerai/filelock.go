package main

// filelock.go is the lock-file scheme for config.json: the config lock on platforms without
// flock(2) (configlock_other.go), kept free of build tags so its tests run on every platform.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"
)

// The O_EXCL lock file, for platforms without flock(2) (configlock_unix.go is the OS lock).
//
// fileLockConfig takes an exclusive lock file (O_EXCL, portable), waiting up to 5 s; a lock
// older than lockStale is a crashed writer's and is taken over.
func fileLockConfig(path string) (func(), error) {
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
		if seen, stale := staleLock(path); stale {
			takeOverStaleLock(path, seen) // then the deadline and the pause, like any other wait
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("config.json is locked by another roger command (%s)", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// lockStale is how old a config lock must be before it counts as a crashed writer's.
const lockStale = 30 * time.Second

// lockSeen is one observation of a lock file: its holder's token and its modification time.
type lockSeen struct {
	token string
	mod   time.Time
}

// staleLock reads the lock at path and reports whether it is older than lockStale.
func staleLock(path string) (lockSeen, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return lockSeen{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return lockSeen{}, false
	}
	seen := lockSeen{token: string(b), mod: fi.ModTime()}
	return seen, time.Since(seen.mod) > lockStale
}

// takeOverStaleLock removes the crashed writer's lock that was seen stale, and only that one.
// The decision is serialized by a second lock file, and the lock is re-read under it: it is
// removed only if it still holds the token and modification time that were seen, so a holder
// that released in between, and the fresh lock a newcomer took, are never deleted (every new
// holder writes its own random token). A takeover lock left by a crash is cleared after a while.
func takeOverStaleLock(path string, seen lockSeen) {
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
	if now, stale := staleLock(path); stale && now == seen {
		_ = os.Remove(path)
	}
}

// lockToken is a random value naming one holder of the config lock.
func lockToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
