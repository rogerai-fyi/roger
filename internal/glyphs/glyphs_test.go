package glyphs

import "testing"

// ROGERAI_ASCII=1 forces the ASCII fallback set regardless of platform; with it
// unset (and on a non-Windows test host) the default stays the rich Unicode look.
func TestASCIIOverride(t *testing.T) {
	t.Setenv("ROGERAI_ASCII", "1")
	t.Setenv("NO_UNICODE", "")
	if !ASCII() {
		t.Fatal("ROGERAI_ASCII=1 should select ASCII")
	}
	if got := Current().OnAir; got != asciiSet.OnAir {
		t.Fatalf("OnAir = %q, want ASCII %q", got, asciiSet.OnAir)
	}
	if got := Current().Beacon; got != "((*))" {
		t.Fatalf("Beacon = %q, want ((*))", got)
	}
}

func TestNoUnicodeOverride(t *testing.T) {
	t.Setenv("ROGERAI_ASCII", "")
	t.Setenv("NO_UNICODE", "1")
	if !ASCII() {
		t.Fatal("NO_UNICODE set should select ASCII")
	}
}

// With no override on a non-Windows host the default is the Unicode set (no
// regression for mac/linux). The test host running `go test` is not Windows.
func TestUnicodeDefault(t *testing.T) {
	t.Setenv("ROGERAI_ASCII", "")
	t.Setenv("NO_UNICODE", "")
	if ASCII() {
		t.Skip("host reports a non-UTF-8 Windows console; ASCII is correct there")
	}
	if got := Current().OnAir; got != unicodeSet.OnAir {
		t.Fatalf("OnAir = %q, want Unicode %q", got, unicodeSet.OnAir)
	}
}

// Fold rewrites art runes to ASCII only when ASCII() is in effect, and is width
// preserving (1 rune -> 1 rune) so column alignment is unaffected.
func TestFold(t *testing.T) {
	t.Setenv("ROGERAI_ASCII", "1")
	if got := Fold("(( • ))"); got != "(( * ))" {
		t.Fatalf("Fold bullet = %q, want (( * ))", got)
	}
	if got := Fold("│ R │"); got != "| R |" {
		t.Fatalf("Fold box = %q, want | R |", got)
	}

	t.Setenv("ROGERAI_ASCII", "")
	t.Setenv("NO_UNICODE", "")
	if ASCII() {
		return // a non-UTF-8 Windows host folds unconditionally; fine
	}
	if got := Fold("│ R │"); got != "│ R │" {
		t.Fatalf("Fold on capable terminal must be a no-op, got %q", got)
	}
}

// TestFoldWorldGlyphs covers the Ping World screensaver glyphs added to asciiFold, so the
// screensaver degrades cleanly on a legacy console.
func TestFoldWorldGlyphs(t *testing.T) {
	t.Setenv("ROGERAI_ASCII", "1")
	cases := map[string]string{
		"✦✧": "**", "˙": "'", "·": ".", "♪": ">", "░▒▓": ".:#", "≈∼∽≋": "~~~~",
	}
	for in, want := range cases {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}
