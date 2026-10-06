//go:build !unix

package main

// lockConfig is the lock-file scheme where there is no flock(2) (filelock.go); Unix uses the
// OS lock (configlock_unix.go).
func lockConfig(path string) (func(), error) { return fileLockConfig(path) }
