package update

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestReplaceSelf checks the platform replace path swaps the new bytes into the
// target. replaceSelf takes the self/tmp paths as args (the seam), so it runs
// against temp files - no real executable, no exec. Per-platform: Unix takes the
// single-rename branch, Windows the rename-aside dance; either way the target
// must end up holding the new bytes.
func TestReplaceSelf(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "rogerai")
	tmp := filepath.Join(dir, ".rogerai-upgrade-123")
	if err := os.WriteFile(self, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmp, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceSelf(self, tmp); err != nil {
		t.Fatalf("replaceSelf: %v", err)
	}
	got, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW" {
		t.Fatalf("self bytes = %q, want NEW", string(got))
	}
	// On Windows the renamed-aside .old may linger (locked); on Unix there is no
	// sidecar. Both are acceptable - we only assert the swap itself landed.
	if runtime.GOOS != "windows" {
		if _, err := os.Stat(self + sidecarSuffix); !os.IsNotExist(err) {
			t.Fatalf("unexpected sidecar on non-windows: %v", err)
		}
	}
}
