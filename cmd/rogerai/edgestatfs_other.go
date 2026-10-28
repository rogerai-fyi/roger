//go:build windows

package main

// edgeStatfs has no portable reading here; disks are simply absent on this platform.
var edgeStatfs = func(path string) (usedGB, totalGB float64, ok bool) { return 0, 0, false }
