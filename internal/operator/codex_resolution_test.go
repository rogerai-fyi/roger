package operator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveGuestBinaryFindsCommonNPMGlobalCodex(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".npm-global", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveGuestBinary("codex", home, func(string) (string, error) {
		return "", os.ErrNotExist
	})
	if err != nil || got != path {
		t.Fatalf("resolveGuestBinary = (%q, %v), want %q", got, err, path)
	}
}
