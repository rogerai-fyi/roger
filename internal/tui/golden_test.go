package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenFile compares a rendered frame with testdata/<name>. UPDATE_GOLDEN=1 writes the file
// instead, so a deliberate change to the look is one re-run plus a reviewed diff.
func goldenFile(t *testing.T, name, got string) error {
	t.Helper()
	p := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(got), 0o644)
	}
	want, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("no golden %s (write it with UPDATE_GOLDEN=1): %w", p, err)
	}
	if string(want) == got {
		return nil
	}
	w, g := strings.Split(string(want), "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return fmt.Errorf("%s differs at line %d:\nwant %q\ngot  %q", p, i+1, a, b)
		}
	}
	return fmt.Errorf("%s differs", p)
}
