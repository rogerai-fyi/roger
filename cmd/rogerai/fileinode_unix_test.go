//go:build unix

package main

import (
	"os"
	"syscall"
)

// fileInode is the file's inode, so a test can tell a rename-into-place from an in-place write.
func fileInode(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Ino), true
}

// fileOwnerUID is the file's owning user id.
func fileOwnerUID(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
