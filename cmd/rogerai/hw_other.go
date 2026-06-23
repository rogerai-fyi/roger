//go:build !linux && !windows

package main

// detectHW returns a human-readable CPU model for advertising a shared node.
// On platforms without a cheap, dependency-free CPU-name source (e.g. macOS,
// the BSDs), we keep it simple and report "unknown" rather than shelling out.
func detectHW() string {
	return "unknown"
}
