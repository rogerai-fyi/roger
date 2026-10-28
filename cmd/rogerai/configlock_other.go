//go:build !unix && !windows

package main

// lockConfig is the lock-file scheme where there is no OS lock (filelock.go): Unix uses flock
// and Windows LockFileEx (configlock_os.go, via internal/filelock), so no shipped platform runs it.
func lockConfig(path string) (func(), error) { return fileLockConfig(path) }
