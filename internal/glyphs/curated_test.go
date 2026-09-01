package glyphs

// The curated mark joins the shared iconography (audit round 5): style.go hardcoded a
// non-ASCII '»', bypassing the ASCII fallback every sibling glyph routes through - the
// one reason this package exists.

import "testing"

func TestCuratedGlyphExistsInBothSets(t *testing.T) {
	if unicodeSet.Curated == "" || asciiSet.Curated == "" {
		t.Fatalf("curated glyph missing: unicode %q ascii %q", unicodeSet.Curated, asciiSet.Curated)
	}
	for _, r := range asciiSet.Curated {
		if r > 0x7f {
			t.Fatalf("the ASCII fallback %q is not ASCII", asciiSet.Curated)
		}
	}
	taken := []string{unicodeSet.OnAir, unicodeSet.OffAir, unicodeSet.Verify, unicodeSet.Lineage, unicodeSet.AgentReady, unicodeSet.Vision}
	for _, g := range taken {
		if g == unicodeSet.Curated {
			t.Fatalf("the curated mark %q reuses an existing glyph", g)
		}
	}
}
