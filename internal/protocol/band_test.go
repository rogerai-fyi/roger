package protocol

import (
	"strings"
	"testing"
)

// TestNewBandCodeFormat verifies the minted code's shape + entropy: a cosmetic
// "<n>.<n> MHz" prefix, a middot, and an 8-char Crockford tail grouped 4-4. The
// one-time FULL code round-trips to the exact tail; codes don't repeat (40 bits).
func TestNewBandCodeFormat(t *testing.T) {
	code, display, tail := NewBandCode()
	if len(tail) != bandTailLen {
		t.Fatalf("tail %q len = %d, want %d", tail, len(tail), bandTailLen)
	}
	for _, r := range tail {
		if !strings.ContainsRune(crockfordAlphabet, r) {
			t.Fatalf("tail %q has non-Crockford symbol %q", tail, r)
		}
	}
	if !strings.Contains(code, "MHz") || !strings.Contains(code, "·") {
		t.Fatalf("code %q missing cosmetic frequency or middot", code)
	}
	// The grouped form in the one-time CODE (4-4) must canonicalize back to the exact tail.
	if got := CanonicalBandTail(code); got != tail {
		t.Fatalf("canonical(code) = %q, want tail %q", got, tail)
	}
	// The persisted DISPLAY is cosmetic but MASKED: it keeps the frequency + middot yet
	// canonicalizes to "" (no recoverable tail) - see TestBandDisplayNotRecoverable.
	if !strings.Contains(display, "MHz") || !strings.Contains(display, "·") {
		t.Fatalf("display %q missing cosmetic frequency or middot", display)
	}
	// Unguessable: a batch of mints must be unique (40 bits => collisions vanishingly
	// rare; a repeat in 1000 draws signals a broken generator).
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		_, _, tl := NewBandCode()
		if seen[tl] {
			t.Fatalf("duplicate tail %q within 1000 mints - generator not random", tl)
		}
		seen[tl] = true
	}
}

// TestMaskBandDisplay is the MIGRATION pin: MaskBandDisplay rewrites a LEGACY (pre-fix)
// persisted display - which embedded the secret tail verbatim ("freq · TAIL"), so it
// resolved the band straight out of stored state - into the MASKED, NON-RECOVERABLE
// cosmetic form, keeping the cosmetic frequency but dropping the tail. It is IDEMPOTENT
// (an already-masked display is returned unchanged) and its output can never resolve a
// band. This is the per-row transform the one-time store re-mask migration applies.
func TestMaskBandDisplay(t *testing.T) {
	// A legacy display IS the resolvable code (pre-fix the display == the full code).
	legacy := "147.520 MHz · 8F3K-9M2Q"
	if CanonicalBandTail(legacy) == "" {
		t.Fatalf("legacy display %q should be recoverable (it embeds the tail) - test premise wrong", legacy)
	}
	masked := MaskBandDisplay(legacy)

	// The cosmetic frequency is preserved; only the tail is replaced by the mask token.
	if !strings.HasPrefix(masked, "147.520 MHz · ") {
		t.Errorf("masked %q dropped the cosmetic frequency", masked)
	}
	if !strings.Contains(masked, maskedTail) {
		t.Errorf("masked %q is missing the %q mask token", masked, maskedTail)
	}
	// NON-RECOVERABLE: it canonicalizes to no tail and hashes away from the band's key.
	if got := CanonicalBandTail(masked); got != "" {
		t.Errorf("masked display %q still canonicalizes to a recoverable tail %q", masked, got)
	}
	if BandCodeHash(masked) == BandCodeHash(legacy) {
		t.Errorf("masked display %q still hashes to the band's lookup key - it can resolve the band", masked)
	}
	// IDEMPOTENT: re-masking an already-masked display is a no-op (so a re-run changes
	// nothing) and equals a fresh mint's masked shape.
	if again := MaskBandDisplay(masked); again != masked {
		t.Errorf("MaskBandDisplay not idempotent: %q -> %q", masked, again)
	}
	if _, fresh, _ := NewBandCode(); MaskBandDisplay(fresh) != fresh {
		t.Errorf("a freshly-minted display %q is changed by re-masking - it should already be masked", fresh)
	}
	// DEFENSIVE: an unrecognized display with no " · " separator (never produced by any
	// mint) must STILL yield a non-recoverable result - even when it ends in a full run of
	// Crockford symbols that would otherwise canonicalize to a tail.
	if got := MaskBandDisplay("garbage-no-separator-ABCDEFGH"); CanonicalBandTail(got) != "" {
		t.Errorf("no-separator mask %q is still recoverable (tail %q)", got, CanonicalBandTail(got))
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

// TestBandDisplayNotRecoverable is the SECURITY pin: the PERSISTED cosmetic display must
// NOT be able to reconstruct/resolve the band. Only the one-time full code (shown once at
// mint) may resolve it. Previously the display embedded the secret tail, so anyone with
// read access to persisted state could recover the band code - this pins that it cannot.
func TestBandDisplayNotRecoverable(t *testing.T) {
	code, display, tail := NewBandCode()

	// The one-time FULL code DOES resolve the band (it carries the secret tail).
	if CanonicalBandTail(code) != tail {
		t.Fatalf("one-time code must resolve: canonical(code=%q) = %q, want tail %q", code, CanonicalBandTail(code), tail)
	}
	if BandCodeHash(code) != BandCodeHash(tail) {
		t.Fatalf("one-time code must hash to the band's lookup key")
	}

	// The PERSISTED display must NOT reconstruct the tail, and must NOT resolve the band.
	if got := CanonicalBandTail(display); got != "" {
		t.Fatalf("persisted display %q reconstructs a tail %q - it must be masked (non-recoverable)", display, got)
	}
	if BandCodeHash(display) == BandCodeHash(tail) {
		t.Fatalf("persisted display %q hashes to the band's lookup key - it can resolve the band", display)
	}
	// And the display must differ from the one-time code (it is not just the code re-shown).
	if display == code {
		t.Fatalf("persisted display equals the one-time code %q - the secret would be stored", code)
	}
}
