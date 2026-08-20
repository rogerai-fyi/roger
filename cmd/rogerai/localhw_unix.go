//go:build !windows

package main

import "syscall"

// diskFreeMiB reports free space on the filesystem holding path, in MiB.
//
// It uses Bavail rather than Bfree deliberately: Bfree counts the blocks the kernel is
// holding in reserve for root, which an operator running `roger share` as themselves can
// never have. Reporting the reserve as available would overstate the disk by up to five
// percent of the volume on a default ext4, which is exactly the kind of small confident
// overclaim this whole check exists to avoid.
//
// The two casts are load-bearing for the build rather than for the arithmetic: Statfs_t's
// Bsize is int64 on Linux and uint32 on Darwin/BSD, and widening both operands to uint64
// is the one expression that compiles on all of them.
func diskFreeMiB(path string) (int, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	bsize := uint64(st.Bsize)
	if bsize == 0 {
		return 0, false
	}
	free := bsize * uint64(st.Bavail)
	return int(free / (1024 * 1024)), true
}
