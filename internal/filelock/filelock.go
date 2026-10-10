// Package filelock is the one OS file lock roger uses: an exclusive, advisory lock on an open
// file that the operating system releases when its holder exits, crashed or not. It is flock on
// Unix and LockFileEx on Windows. Every caller locks a dedicated lock file (never a data file),
// because Windows byte-range locks are mandatory: a locked data file could not be read by
// another handle.
//
// It imports nothing of roger's, so Core-free packages (edgeauth, the standalone serve path)
// may use it.
package filelock
