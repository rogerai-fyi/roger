//go:build !unix

package edge

import "os"

// Without flock (Windows) the in-process mutex is the only serialisation - single-process safety,
// the same guarantee the authority's own files have there.
func flockExclusive(f *os.File) error { return nil }

func flockUnlock(f *os.File) error { return nil }
