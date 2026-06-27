package glyphs

import "testing"

// withGOOS swaps the goos seam for the duration of the test and restores it after,
// so the Windows-only branches of ASCII() are reachable on a non-Windows host.
func withGOOS(t *testing.T, v string) {
	t.Helper()
	prev := goos
	goos = v
	t.Cleanup(func() { goos = prev })
}

// TestASCIIWindowsBranches drives the two ASCII() branches that only execute when
// GOOS=="windows": a known-good UTF-8 terminal keeps Unicode (ASCII false), and a
// legacy OEM console (no UTF-8 hint, no override) falls back to ASCII (true). With
// the override paths already covered, this clears the remaining ASCII() statements.
func TestASCIIWindowsBranches(t *testing.T) {
	// Start from a clean env so no host override or locale hint leaks in.
	for _, k := range []string{"ROGERAI_ASCII", "NO_UNICODE", "WT_SESSION", "LANG", "LC_ALL", "LC_CTYPE", "PYTHONIOENCODING"} {
		t.Setenv(k, "")
	}
	withGOOS(t, "windows")

	// Known-good UTF-8 terminal (Windows Terminal exports WT_SESSION) -> Unicode.
	t.Setenv("WT_SESSION", "win-term-7f3a")
	if ASCII() {
		t.Fatal("windows + WT_SESSION must keep Unicode (ASCII=false)")
	}
	if got := Current().OnAir; got != unicodeSet.OnAir {
		t.Fatalf("OnAir = %q, want Unicode %q", got, unicodeSet.OnAir)
	}

	// Legacy conhost/cmd.exe under an OEM codepage: no terminal hint -> ASCII.
	t.Setenv("WT_SESSION", "")
	if !ASCII() {
		t.Fatal("windows + legacy console (no UTF-8 hint) must fall back to ASCII")
	}
	if got := Current().Beacon; got != asciiSet.Beacon {
		t.Fatalf("Beacon = %q, want ASCII %q", got, asciiSet.Beacon)
	}
	// A UTF-8 locale hint also flips a legacy Windows console back to Unicode.
	t.Setenv("LANG", "en_US.UTF-8")
	if ASCII() {
		t.Fatal("windows + UTF-8 LANG must keep Unicode (ASCII=false)")
	}
}

// TestASCIINonWindowsSeam confirms the seam preserves the non-Windows default: any
// non-windows GOOS with no override resolves to Unicode (ASCII=false), independent
// of any terminal/locale env that happens to be set.
func TestASCIINonWindowsSeam(t *testing.T) {
	for _, k := range []string{"ROGERAI_ASCII", "NO_UNICODE", "WT_SESSION", "LANG", "LC_ALL", "LC_CTYPE", "PYTHONIOENCODING"} {
		t.Setenv(k, "")
	}
	for _, os := range []string{"linux", "darwin"} {
		withGOOS(t, os)
		if ASCII() {
			t.Errorf("GOOS=%q with no override must resolve to Unicode (ASCII=false)", os)
		}
	}
}
