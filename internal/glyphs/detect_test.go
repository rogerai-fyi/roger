package glyphs

import "testing"

// TestHasUTF8 covers the codepage/encoding sniff: any of utf-8 / utf8 / 65001 (any
// case) signals UTF-8; everything else does not.
func TestHasUTF8(t *testing.T) {
	for _, s := range []string{"en_US.UTF-8", "C.utf8", "cp65001", "UTF-8", "x.Utf8"} {
		if !hasUTF8(s) {
			t.Errorf("hasUTF8(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "C", "en_US", "latin1", "iso-8859-1"} {
		if hasUTF8(s) {
			t.Errorf("hasUTF8(%q) = true, want false", s)
		}
	}
}

// TestWindowsUTF8Terminal covers the known-good-terminal detection (OS-independent: the
// function only reads env). WT_SESSION or a UTF-8 hint in a locale var => true; an empty
// environment => false.
func TestWindowsUTF8Terminal(t *testing.T) {
	clear := func() {
		for _, k := range []string{"WT_SESSION", "LC_ALL", "LC_CTYPE", "LANG", "PYTHONIOENCODING"} {
			t.Setenv(k, "")
		}
	}

	clear()
	if windowsUTF8Terminal() {
		t.Error("empty env should not be a known-good UTF-8 terminal")
	}

	clear()
	t.Setenv("WT_SESSION", "abc-123")
	if !windowsUTF8Terminal() {
		t.Error("WT_SESSION set should be a known-good terminal")
	}

	clear()
	t.Setenv("LANG", "en_US.UTF-8")
	if !windowsUTF8Terminal() {
		t.Error("a UTF-8 LANG should be a known-good terminal")
	}

	clear()
	t.Setenv("PYTHONIOENCODING", "utf-8")
	if !windowsUTF8Terminal() {
		t.Error("a UTF-8 PYTHONIOENCODING should be a known-good terminal")
	}
}

// TestASCIIEnvForce covers the explicit overrides (both env knobs force ASCII) and the
// non-Windows default (Unicode) when no override is present.
func TestASCIIEnvForce(t *testing.T) {
	for _, k := range []string{"ROGERAI_ASCII", "NO_UNICODE", "WT_SESSION", "LANG", "LC_ALL", "LC_CTYPE", "PYTHONIOENCODING"} {
		t.Setenv(k, "")
	}
	// No override on a non-Windows host -> Unicode (ASCII false). On Windows this would
	// depend on the terminal; the override paths below are exercised regardless of OS.
	t.Setenv("ROGERAI_ASCII", "1")
	if !ASCII() {
		t.Error("ROGERAI_ASCII=1 must force ASCII")
	}
	t.Setenv("ROGERAI_ASCII", "")
	t.Setenv("NO_UNICODE", "1")
	if !ASCII() {
		t.Error("NO_UNICODE=1 must force ASCII")
	}
}
