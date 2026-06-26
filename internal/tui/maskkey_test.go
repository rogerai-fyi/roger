package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestMaskKey: the last 4 CHARACTERS stay intact and the rest become bullets, including
// for non-ASCII keys (the regression: byte-slicing k[len(k)-4:] could split a multi-byte
// rune and emit garbled/invalid UTF-8).
func TestMaskKey(t *testing.T) {
	if got := maskKey("sk-abcd1234"); got != strings.Repeat("•", 7)+"1234" {
		t.Errorf("maskKey ascii = %q, want 7 bullets + 1234", got)
	}
	if got := maskKey("abc"); got != "•••" {
		t.Errorf("maskKey short = %q, want all bullets", got)
	}
	if got := maskKey(""); got != "" {
		t.Errorf("maskKey empty = %q", got)
	}

	// A key ending in multi-byte runes: the tail must be the last 4 RUNES, intact + valid.
	k := "secret-naïve-Ωπ"
	r := []rune(k)
	wantTail := string(r[len(r)-4:])
	got := maskKey(k)
	if !utf8.ValidString(got) {
		t.Fatalf("maskKey produced invalid UTF-8 for a multi-byte key: %q", got)
	}
	if !strings.HasSuffix(got, wantTail) {
		t.Errorf("maskKey(%q) = %q, want it to end with the last 4 runes %q", k, got, wantTail)
	}
	if utf8.RuneCountInString(got) != len(r) {
		t.Errorf("maskKey should preserve the rune count: got %d, want %d", utf8.RuneCountInString(got), len(r))
	}
}
