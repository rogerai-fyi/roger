//go:build !unix && !windows

package main

// lockConfig is the lock-file scheme where there is no OS lock (filelock.go): Unix uses flock
// (configlock_unix.go) and Windows LockFileEx (configlock_windows.go), so no shipped platform runs it.
func lockConfig(path string) (func(), error) { return fileLockConfig(path) }
