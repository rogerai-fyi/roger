// Package gguftest writes minimal GGUF headers for tests in any package: a header that SAYS
// things (string keys only) and carries no tensors and nothing the fit arithmetic could use.
package gguftest

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// WriteStrings writes a GGUF v3 file at path whose header holds only the string keys in kv.
func WriteStrings(t testing.TB, path string, kv map[string]string) {
	t.Helper()
	b := []byte("GGUF")
	b = binary.LittleEndian.AppendUint32(b, 3)
	b = binary.LittleEndian.AppendUint64(b, 0)
	b = binary.LittleEndian.AppendUint64(b, uint64(len(kv)))
	str := func(s string) {
		b = binary.LittleEndian.AppendUint64(b, uint64(len(s)))
		b = append(b, s...)
	}
	for k, v := range kv {
		str(k)
		b = binary.LittleEndian.AppendUint32(b, 8) // string
		str(v)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
