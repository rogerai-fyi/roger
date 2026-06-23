//go:build windows

package main

import (
	"os"
	"strings"
)

// detectHW returns a human-readable CPU model for advertising a shared node.
// On Windows there is no /proc; the processor identifier the OS exposes via the
// PROCESSOR_IDENTIFIER environment variable is the cheapest dependency-free
// source (e.g. "Intel64 Family 6 Model 158 Stepping 10, GenuineIntel"). We fall
// back to the architecture if it isn't set.
func detectHW() string {
	if id := strings.TrimSpace(os.Getenv("PROCESSOR_IDENTIFIER")); id != "" {
		return id
	}
	if arch := strings.TrimSpace(os.Getenv("PROCESSOR_ARCHITECTURE")); arch != "" {
		return arch
	}
	return "unknown"
}
