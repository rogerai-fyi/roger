package glyphs

import "testing"

// TestFoldEllipsis pins the GUEST-OPERATOR-PLATES.md §7 rule "all `…` fold to
// `...` under ASCII per glyphs.Fold": the one-rune ellipsis expands to three
// dots (asciiFold is rune-to-rune, so this is a string-level pre-pass), and
// Fold stays byte-identical when ASCII mode is off.
func TestFoldEllipsis(t *testing.T) {
	t.Setenv("ROGERAI_ASCII", "1")
	if got := Fold("patching opencode through on qwen3-32b-fp8…"); got != "patching opencode through on qwen3-32b-fp8..." {
		t.Fatalf("… must fold to ... under ASCII, got %q", got)
	}
	if got := Fold("H E R M E S · nous research"); got != "H E R M E S . nous research" {
		t.Fatalf("· folds to . under ASCII (lockup separators), got %q", got)
	}
	t.Setenv("ROGERAI_ASCII", "")
	if got := Fold("PATCHING YOU THROUGH…"); got != "PATCHING YOU THROUGH…" {
		t.Fatalf("Fold must be a no-op off ASCII, got %q", got)
	}
}
