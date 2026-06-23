//go:build linux

package main

import (
	"os"
	"strings"
)

// detectHW returns a human-readable CPU model for advertising a shared node.
// On Linux it reads the kernel's /proc/cpuinfo.
func detectHW() string {
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "model name") {
				if i := strings.Index(line, ":"); i >= 0 {
					return strings.TrimSpace(line[i+1:])
				}
			}
		}
	}
	return "unknown"
}
