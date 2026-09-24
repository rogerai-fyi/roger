//go:build !unix

package edgeauth

import "os"

// On platforms without flock (Windows), the in-process mutex is the only serialisation. An Edge
// authority runs on the owner's always-on machine (Linux/macOS in practice); a Windows authority
// falls back to single-process safety, which is the same guarantee it had before.
func flockExclusive(f *os.File) error { return nil }

func flockUnlock(f *os.File) error { return nil }
