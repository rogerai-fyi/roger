package structural

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateSourceAbsent(t *testing.T) {
	root := t.TempDir()
	if !PrivateSourceAbsent(root) {
		t.Fatal("a tree without the broker source must read as the public export")
	}
	if err := os.MkdirAll(filepath.Join(root, BrokerDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if PrivateSourceAbsent(root) {
		t.Fatal("a tree with the broker source must read as the private source")
	}
}
