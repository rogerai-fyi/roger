//go:build !windows

package main

import "syscall"

// edgeStatfs reads a mounted filesystem's used and total space in GB. A seam so tests can fake it.
var edgeStatfs = func(path string) (usedGB, totalGB float64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return 0, 0, false
	}
	bs := float64(st.Bsize)
	total := float64(st.Blocks) * bs
	free := float64(st.Bavail) * bs
	gb := 1024.0 * 1024 * 1024
	return (total - free) / gb, total / gb, true
}
