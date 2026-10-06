package tui

import (
	"io"
	"os"
	"os/exec"
	"strings"
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

// TestClipboardWriteEmitsNoOSC52InTests pins the other half: the OSC 52 escape a terminal
// turns into a clipboard write never reaches the real stdout during the package run.
func TestClipboardWriteEmitsNoOSC52InTests(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	clipboardWrite("isolation probe")()
	os.Stdout = prev
	w.Close()
	out, _ := io.ReadAll(r)
	if strings.Contains(string(out), "\x1b]52;") {
		t.Fatalf("clipboardWrite printed an OSC 52 clipboard escape to stdout: %q", out)
	}
}
