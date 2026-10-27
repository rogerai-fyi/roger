package protocol

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The shared display sanitizer behind quant/weights/variant (and the taxonomy's base model and
// license) drops display-spoofing characters and caps by CHARACTERS. A bidi override can make
// one name render as another, and a zero-width or line separator breaks a row exactly as a
// newline would. offer_taxonomy.feature pins the same rules through the base model.
func TestCanonicalVariantTextDropsSpoofingCharacters(t *testing.T) {
	for _, r := range []rune{
		0x202A, 0x202B, 0x202C, 0x202D, 0x202E, // bidi embeddings/overrides
		0x2066, 0x2067, 0x2068, 0x2069, // bidi isolates
		0x200B, 0x200C, 0x200D, 0x200E, 0x200F, // zero-width + LRM/RLM
		0x2028, 0x2029, 0xFEFF, // line/paragraph separators, BOM
	} {
		if got := CanonicalVariantText("un" + string(r) + "sloth"); got != "unsloth" {
			t.Errorf("U+%04X survived: %q", r, got)
		}
	}
}

func TestCanonicalVariantTextCapsByCharacters(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{strings.Repeat("a", 40), 40},
		{strings.Repeat("a", 41), 40},
		{strings.Repeat("量", 40), 40},
		{strings.Repeat("量", 41), 40},
	} {
		got := CanonicalVariantText(tc.in)
		if n := utf8.RuneCountInString(got); n != tc.want {
			t.Errorf("%d chars of %q: kept %d, want %d", utf8.RuneCountInString(tc.in), tc.in[:3], n, tc.want)
		}
	}
}
