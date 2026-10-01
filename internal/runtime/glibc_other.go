//go:build !linux

package runtime

// hostGlibc does not apply off Linux: macOS and Windows builds carry no glibc floor, so there is
// nothing to check and reporting unknown is the honest answer rather than a fabricated version.
func hostGlibc() (major, minor int, ok bool) { return 0, 0, false }
