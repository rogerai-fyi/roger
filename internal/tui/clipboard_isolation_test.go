package tui

import (
	"os/exec"
	"testing"
)

// TestClipboardIsolated pins that the package run never reaches the developer's real
// clipboard: clipboardWrite tests used to put "payload" on it, and the long-lived
// wl-copy server they spawned outlived the run.
func TestClipboardIsolated(t *testing.T) {
	if copyToClipboard("isolation probe") {
		t.Fatal("copyToClipboard reached a real clipboard tool during tests")
	}
}

// TestCopyToClipboardToolOutcome drives the tool path with stand-ins: a tool that exits 0
// copies, one that fails falls through to the next and reports no copy.
func TestCopyToClipboardToolOutcome(t *testing.T) {
	prev := clipboardLookPath
	t.Cleanup(func() { clipboardLookPath = prev })
	for _, tc := range []struct {
		bin  string
		want bool
	}{{"true", true}, {"false", false}} {
		path, err := exec.LookPath(tc.bin)
		if err != nil {
			t.Skipf("no %q binary on this platform", tc.bin)
		}
		clipboardLookPath = func(string) (string, error) { return path, nil }
		if got := copyToClipboard("x"); got != tc.want {
			t.Errorf("tool %q: copyToClipboard = %v, want %v", tc.bin, got, tc.want)
		}
	}
}
