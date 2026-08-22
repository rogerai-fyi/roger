package protocol

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// THE VARIANT FIELDS AND THE POSSESSION PROOF (MODEL-VARIANTS-DESIGN-2026-08-22).
//
// Quant/Weights/Variant are optional, later-added DISPLAY attributes - they tell two
// offers of the same model id apart. Like Capabilities before them they are EXCLUDED from
// the registration signature, and for the same reason, which has already caused one real
// outage:
//
// A node signs its registration; the broker re-marshals it to verify. If the field is part
// of the signed bytes, then ANY version skew changes those bytes and the signature fails.
// Both directions break:
//
//   - NEW node -> OLD broker: the broker's struct has no such field, json drops it on
//     unmarshal, the re-marshal omits it, and the bytes no longer match what was signed.
//   - OLD node -> NEW broker: the broker's struct has the field as a nil/empty value, and
//     without omitempty it serializes as a key the node never signed over.
//
// Excluding the field from the proof fixes both, because then NEITHER side ever had it in
// the signed bytes. That is what these lock.

func variantReg(t *testing.T) (NodeRegistration, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	return NodeRegistration{
		NodeID: "amber-fox-qwen", PubKey: hex.EncodeToString(pub), TS: 123,
		Offers: []ModelOffer{{Model: "qwen3.8-27b", PriceIn: 1, PriceOut: 2}},
	}, priv
}

// THE HEADLINE: a signature made WITHOUT the variant fields still verifies once a newer
// binary fills them in - the NEW-node/OLD-broker and OLD-node/NEW-broker skew.
func TestRegistrationSignatureIgnoresTheVariantFields(t *testing.T) {
	reg, priv := variantReg(t)
	reg.SignRegistration(priv)
	if !reg.VerifyRegistration() {
		t.Fatal("baseline (no variant fields) must verify")
	}
	for _, tc := range []struct{ quant, weights, variant string }{
		{"Q4_K_M", "unsloth", "thinking"},
		{"IQ4_XS", "", ""},
		{"", "bartowski", ""},
		{"", "", ""},
	} {
		reg.Offers[0].Quant = tc.quant
		reg.Offers[0].Weights = tc.weights
		reg.Offers[0].Variant = tc.variant
		if !reg.VerifyRegistration() {
			t.Fatalf("variant fields %+v must not affect the possession proof", tc)
		}
		// And verification must not MUTATE the caller's offer - regSigningBytes works on
		// a deep copy, so the values the node meant to advertise survive the check.
		if reg.Offers[0].Quant != tc.quant || reg.Offers[0].Weights != tc.weights ||
			reg.Offers[0].Variant != tc.variant {
			t.Fatalf("VerifyRegistration mutated the offer: %+v", reg.Offers[0])
		}
	}
	// The proof still covers what it is FOR: the real offer terms.
	reg.Offers[0].PriceOut = 999
	if reg.VerifyRegistration() {
		t.Error("a mutated price must still fail verification")
	}
}

// AND THE OTHER DIRECTION: signing WITH the fields set must produce the same signature as
// signing without them. If it did not, a node that detected a quant would sign different
// bytes from one that did not, and an older broker would reject it.
func TestSigningIsIdenticalWithAndWithoutVariants(t *testing.T) {
	bare, priv := variantReg(t)
	bare.SignRegistration(priv)

	rich := bare
	rich.Offers = []ModelOffer{{
		Model: "qwen3.8-27b", PriceIn: 1, PriceOut: 2,
		Quant: "Q4_K_M", Weights: "unsloth", Variant: "thinking",
	}}
	rich.SignRegistration(priv)

	if bare.Sig != rich.Sig {
		t.Errorf("signatures differ with and without the variant fields:\n bare: %s\n rich: %s",
			bare.Sig, rich.Sig)
	}
}

// EMPTY MUST SERIALIZE TO NO KEY. Without omitempty an old node and a new one produce
// different JSON for the same offer, which is the exact shape of the 401 outage.
func TestEmptyVariantFieldsSerializeToNothing(t *testing.T) {
	b, err := json.Marshal(ModelOffer{Model: "m", PriceIn: 1, PriceOut: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"quant", "weights", "variant"} {
		if strings.Contains(string(b), `"`+key+`"`) {
			t.Errorf("an empty offer emitted %q: %s", key, b)
		}
	}
	// And a SET field does appear, or the wire carries nothing useful.
	b2, _ := json.Marshal(ModelOffer{Model: "m", Quant: "Q4_K_M", Weights: "unsloth", Variant: "thinking"})
	for _, want := range []string{`"quant":"Q4_K_M"`, `"weights":"unsloth"`, `"variant":"thinking"`} {
		if !strings.Contains(string(b2), want) {
			t.Errorf("a set offer is missing %s: %s", want, b2)
		}
	}
}

// NORMALIZE IS THE TRUST BOUNDARY for these fields, exactly as it is for the billing unit.
// They are node-supplied strings that land on a terminal row and in a browser table, so an
// unbounded or control-laden one is a layout weapon rather than a label.
func TestNormalizeBoundsTheVariantFields(t *testing.T) {
	long := strings.Repeat("A", 500)
	o := ModelOffer{Model: "m", Quant: long, Weights: long, Variant: long}
	o.Normalize()
	for name, got := range map[string]string{"quant": o.Quant, "weights": o.Weights, "variant": o.Variant} {
		if len(got) > variantTextMax {
			t.Errorf("%s survived at %d bytes - a node could push every column off the dial", name, len(got))
		}
	}
}

// CONTROL CHARACTERS ARE REMOVED, not escaped. A node that could embed an ANSI escape or a
// newline in its "weights" could repaint another operator's screen or break a row in half.
func TestNormalizeStripsControlCharactersFromVariants(t *testing.T) {
	o := ModelOffer{
		Model:   "m",
		Quant:   "Q4\x1b[31m_K_M",
		Weights: "uns\nloth",
		Variant: "think\ting\x00",
	}
	o.Normalize()
	for name, got := range map[string]string{"quant": o.Quant, "weights": o.Weights, "variant": o.Variant} {
		for _, r := range got {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				t.Errorf("%s kept control character %q in %q", name, r, got)
			}
		}
	}
	if o.Weights != "unsloth" {
		t.Errorf("weights = %q, want the text with the newline removed", o.Weights)
	}
}

// The quant label is UPPER-CASED because it is a name: a publisher writing q4_k_m means the
// same weights as one writing Q4_K_M, and a filter has to match both.
func TestNormalizeUppercasesTheQuantLabelOnly(t *testing.T) {
	o := ModelOffer{Model: "m", Quant: " q4_k_m ", Weights: "Unsloth", Variant: "Thinking"}
	o.Normalize()
	if o.Quant != "Q4_K_M" {
		t.Errorf("quant = %q, want Q4_K_M", o.Quant)
	}
	// Weights and variant are NAMES people wrote, not labels - their case is theirs.
	if o.Weights != "Unsloth" || o.Variant != "Thinking" {
		t.Errorf("a publisher's own capitalisation was changed: %q / %q", o.Weights, o.Variant)
	}
}

// UNLIKE CAPABILITIES, the quant vocabulary is OPEN. A capability GRANTS behaviour so an
// unknown one is dropped; a quant only DESCRIBES, and its vocabulary genuinely moves -
// MXFP4_MOE and NVFP4 are recent llama.cpp additions and the next one is in no list we
// could ship today. Dropping unknowns here would erase the distinctions the field exists
// to carry.
func TestAnUnfamiliarQuantSurvivesNormalize(t *testing.T) {
	for _, q := range []string{"NVFP4", "MXFP4_MOE", "SOMETHING_NEW_2027", "AWQ", "MLX-4BIT"} {
		o := ModelOffer{Model: "m", Quant: q}
		o.Normalize()
		if o.Quant != strings.ToUpper(q) {
			t.Errorf("quant %q was dropped or altered to %q", q, o.Quant)
		}
	}
	// And an empty one stays empty - absent is absent, never a placeholder.
	o := ModelOffer{Model: "m", Quant: "   "}
	o.Normalize()
	if o.Quant != "" {
		t.Errorf("blank quant became %q", o.Quant)
	}
}
