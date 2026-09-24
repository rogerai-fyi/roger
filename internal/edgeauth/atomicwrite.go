package edgeauth

import (
	"os"
	"path/filepath"
)

// atomicWriteFile writes data to path so a reader ever sees either the old file or the whole new one,
// never a half-written mix, and the bytes are on disk before the rename returns. A crash mid-write
// therefore cannot leave the authority's JSON (claims / issued / revoked / root) truncated and
// unopenable (audit 2026-09-24). The temp file is created in the same directory so the rename is a
// same-filesystem atomic operation.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
