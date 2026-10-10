//go:build !unix

package main

import "os"

// fileInode has no portable inode off unix; the atomic-write check reports that it cannot tell.
func fileInode(os.FileInfo) (uint64, bool) { return 0, false }

// fileOwnerUID has no portable owner id off unix.
func fileOwnerUID(os.FileInfo) (int, bool) { return 0, false }
