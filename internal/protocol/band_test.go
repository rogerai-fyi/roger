package protocol

import (
	"strings"
	"testing"
)

// TestNewBandCodeFormat verifies the minted code's shape + entropy: a cosmetic
// "<n>.<n> MHz" prefix, a middot, and an 8-char Crockford tail grouped 4-4. The
// canonical tail round-trips out of the display, and codes don't repeat (40 bits).
func TestNewBandCodeFormat(t *testing.T) {
	display, tail := NewBandCode()
	if len(tail) != bandTailLen {
		t.Fatalf("tail %q len = %d, want %d", tail, len(tail), bandTailLen)
	}
	for _, r := range tail {
		if !strings.ContainsRune(crockfordAlphabet, r) {
			t.Fatalf("tail %q has non-Crockford symbol %q", tail, r)
		}
	}
	if !strings.Contains(display, "MHz") || !strings.Contains(display, "·") {
		t.Fatalf("display %q missing cosmetic frequency or middot", display)
	}
	// The grouped form in the display (4-4) must canonicalize back to the exact tail.
	if got := CanonicalBandTail(display); got != tail {
		t.Fatalf("canonical(display) = %q, want tail %q", got, tail)
	}
	// Unguessable: a batch of mints must be unique (40 bits => collisions vanishingly
	// rare; a repeat in 1000 draws signals a broken generator).
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		_, tl := NewBandCode()
		if seen[tl] {
			t.Fatalf("duplicate tail %q within 1000 mints - generator not random", tl)
		}
		seen[tl] = true
	}
}

// TestCanonicalBandTail verifies normalization: the cosmetic prefix, spaces, dashes,
// dots, the "MHz" unit and the middot are stripped; Crockford confusables (I/L->1,
// O->0) are mapped; case is normalized; and the tail is taken from the END so the
// leading cosmetic digits fall off. Wrong inputs that are too short yield "".
func TestCanonicalBandTail(t *testing.T) {
	// The same code typed several ways must canonicalize identically.
	want := "8F3K9M2Q"
	variants := []string{
		"147.520 MHz · 8F3K-9M2Q",
		"147.520 MHz 8F3K9M2Q",
		"8F3K-9M2Q",
		"8f3k9m2q",
		"  8F3K 9M2Q  ",
	}
	for _, v := range variants {
		if got := CanonicalBandTail(v); got != want {
			t.Errorf("canonical(%q) = %q, want %q", v, got, want)
		}
	}
	// Confusable mapping: O->0, I->1, L->1 (so a human transcription still resolves).
	if got := CanonicalBandTail("OILOABCD"); got != "0110ABCD" {
		t.Errorf("confusable map = %q, want 0110ABCD", got)
	}
	// Too short -> "" (never a valid lookup).
	if got := CanonicalBandTail("ABC"); got != "" {
		t.Errorf("short input = %q, want empty", got)
	}
	if got := CanonicalBandTail(""); got != "" {
		t.Errorf("empty input = %q, want empty", got)
	}
}

// TestBandCodeHash verifies the lookup key is over the canonical TAIL ONLY: the
// cosmetic frequency is never folded in (two displays with the SAME tail but
// DIFFERENT cosmetic MHz hash identically), and a wrong tail hashes differently.
func TestBandCodeHash(t *testing.T) {
	a := BandCodeHash("147.520 MHz · 8F3K-9M2Q")
	b := BandCodeHash("220.100 MHz · 8F3K-9M2Q") // different cosmetic, same tail
	if a != b {
		t.Errorf("cosmetic frequency leaked into the key: %q != %q", a, b)
	}
	c := BandCodeHash("147.520 MHz · 8F3K-9M2R") // one symbol off
	if a == c {
		t.Errorf("different tail hashed the same - key collision")
	}
	// An empty / garbage input hashes the empty tail (never matches a minted band) but
	// still produces a stable, fixed-length hash (constant-work resolve).
	if h := BandCodeHash("xx"); h == "" || len(h) != 64 {
		t.Errorf("garbage input hash = %q, want a 64-hex constant-work hash", h)
	}
}
