package tui

import (
	"bytes"
	"io"
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

// TestClipboardWriteRoutesOSC52ThroughClipboardOut pins the other half: every OSC 52
// escape (a terminal turns it into a clipboard write) goes through clipboardOut, which
// TestMain points at io.Discard, so none reaches the real terminal during the run.
func TestClipboardWriteRoutesOSC52ThroughClipboardOut(t *testing.T) {
	if clipboardOut != io.Discard {
		t.Fatal("TestMain must point clipboardOut at io.Discard")
	}
	var buf bytes.Buffer
	clipboardOut = &buf
	t.Cleanup(func() { clipboardOut = io.Discard })
	clipboardWrite("isolation probe")()
	if !strings.Contains(buf.String(), "\x1b]52;") {
		t.Fatalf("clipboardWrite's OSC 52 escape did not go through clipboardOut: %q", buf.String())
	}
}
