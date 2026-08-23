package protocol

import "testing"

// One spelling of a quant, everywhere.
//
// llama.cpp publishes upper-case labels ("Q4_K_M"), so upper-casing normalises them. MLX
// publishes lower-case ("4bit", "8bit-DWQ"), and "4BIT" is a spelling no publisher uses -
// an operator sees "4bit" in LM Studio and on the hub. If the wire upper-cases it, then
// detection's careful casing is undone at registration, the row shows a name nobody
// recognises, and two stations serving the same weights can land on different rows
// depending on which runtime reported them.
func TestCanonicalQuantKeepsThePublishedSpelling(t *testing.T) {
	for in, want := range map[string]string{
		// llama.cpp family: upper-casing is the normaliser.
		"q4_k_m": "Q4_K_M",
		"Q4_K_M": "Q4_K_M",
		"iq4_xs": "IQ4_XS",
		"bf16":   "BF16",
		// MLX family: lower-case is the published spelling.
		"4bit":     "4bit",
		"4BIT":     "4bit",
		"8bit":     "8bit",
		"3bit":     "3bit",
		"6bit":     "6bit",
		"2bit":     "2bit",
		"5BIT":     "5bit",
		"7bit-dwq": "7bit-DWQ",
		"8bit-dwq": "8bit-DWQ",
		"8BIT-DWQ": "8bit-DWQ",
		"dwq":      "DWQ",
		// Not the MLX family: a bare number or a longer name is untouched by that rule.
		"16bit":   "16BIT",
		"4bitish": "4BITISH",
		"":        "",
	} {
		if got := CanonicalQuant(in); got != want {
			t.Errorf("CanonicalQuant(%q) = %q, want %q", in, got, want)
		}
	}
}

// Canonicalising twice must not drift: the wire re-canonicalises on every hop.
func TestCanonicalQuantIsIdempotent(t *testing.T) {
	for _, in := range []string{"4bit", "8bit-DWQ", "Q4_K_M", "iq4_xs", "DWQ", ""} {
		once := CanonicalQuant(in)
		if twice := CanonicalQuant(once); twice != once {
			t.Errorf("CanonicalQuant(%q): %q then %q - not idempotent", in, once, twice)
		}
	}
}
