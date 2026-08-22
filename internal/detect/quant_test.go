package detect

import "testing"

// THE LABEL IS STORED VERBATIM. Q4_K_M and IQ4_XS are both four-bit and people choose
// between them on purpose, so anything that collapsed them into "4-bit" would destroy the
// distinction this whole feature exists to make.
func TestQuantLabelsAreNotBucketed(t *testing.T) {
	a := quantLabel("", ggufMeta{FileType: 15, FileTypeSet: true}, "")
	b := quantLabel("", ggufMeta{FileType: 30, FileTypeSet: true}, "")
	if a != "Q4_K_M" || b != "IQ4_XS" {
		t.Fatalf("got %q and %q, want Q4_K_M and IQ4_XS", a, b)
	}
	if a == b {
		t.Error("two different four-bit quants collapsed to the same label")
	}
}

// AN UNKNOWN ENUM VALUE MAPS TO NOTHING, never to a guess. A consumer filtering on Q4_K_M
// who silently got something else is the exact failure this feature is meant to prevent -
// and a removed ftype (4, 5, 6, 33-35) must not be handed a modern label it does not have.
func TestAnUnknownFileTypeYieldsNoLabel(t *testing.T) {
	for _, v := range []uint32{4, 5, 6, 33, 34, 35, 200, 65535} {
		if got := quantFromFileType(v, true); got != "" {
			t.Errorf("file_type %d mapped to %q - an unnameable value must stay unnamed", v, got)
		}
	}
	// And "absent" is distinct from 0, which is a REAL value (all-F32).
	if got := quantFromFileType(0, false); got != "" {
		t.Errorf("an absent file_type produced %q", got)
	}
	if got := quantFromFileType(0, true); got != "F32" {
		t.Errorf("file_type 0 is all-F32, got %q", got)
	}
}

// The file NAME is a real source - it is what the publisher called these weights.
func TestQuantFromAFileName(t *testing.T) {
	for name, want := range map[string]string{
		"Qwen3.8-27B-Q4_K_M.gguf":                 "Q4_K_M",
		"/models/unsloth/qwen3.8-27b-iq4_xs.gguf": "IQ4_XS",
		"Meta-Llama-3.1-8B-Instruct-Q8_0.gguf":    "Q8_0",
		"model-BF16.gguf":                         "BF16",
		"gpt-oss-20b-MXFP4_MOE.gguf":              "MXFP4_MOE",
		"Qwen3.8-27B-Q6_K.gguf":                   "Q6_K",
	} {
		if got := quantInName(name); got != want {
			t.Errorf("quantInName(%q) = %q, want %q", name, got, want)
		}
	}
}

// IT MUST NOT INVENT ONE. A name with no quant in it, or a fragment that merely looks like
// one, produces nothing - otherwise a station is honestly mislabelled by whoever named the
// file.
func TestQuantInNameInventsNothing(t *testing.T) {
	for _, name := range []string{
		"", "model.gguf", "qwen3.8-27b.gguf",
		"IQ1",                  // a bare fragment, not a quant
		"my-Q4-model.gguf",     // Q4 alone is not a label
		"weird-Q99_K_M.gguf",   // not a real level
		"llama-2-7b-chat.gguf", // nothing quant-ish at all
	} {
		if got := quantInName(name); got != "" {
			t.Errorf("quantInName(%q) invented %q", name, got)
		}
	}
}

// THE SOURCE ORDER IS THE POINT: the runtime describes what is in MEMORY, the header
// describes the FILE it was built from, the name is what a human called it. When they
// disagree, the earlier one is the one actually serving requests.
func TestTheRuntimeLabelWinsOverTheFileAndTheName(t *testing.T) {
	meta := ggufMeta{FileType: 18, FileTypeSet: true} // Q6_K in the header
	got := quantLabel("Q4_K_M", meta, "something-IQ4_XS.gguf")
	if got != "Q4_K_M" {
		t.Errorf("got %q - the loaded runtime's own label must win", got)
	}
	// With no runtime label, the header beats the name.
	if got := quantLabel("", meta, "something-IQ4_XS.gguf"); got != "Q6_K" {
		t.Errorf("got %q, want the header's Q6_K over the file name", got)
	}
	// With neither, the name is all there is.
	if got := quantLabel("", ggufMeta{}, "something-IQ4_XS.gguf"); got != "IQ4_XS" {
		t.Errorf("got %q, want IQ4_XS from the name", got)
	}
	// With nothing at all, nothing.
	if got := quantLabel("", ggufMeta{}, ""); got != "" {
		t.Errorf("got %q from no source at all", got)
	}
}

// Ollama says "unknown" for models it cannot classify. That is an absence wearing a word,
// and it must not become a label a consumer could filter on.
func TestOllamasUnknownIsTreatedAsAbsent(t *testing.T) {
	if got := quantLabel("unknown", ggufMeta{}, "x-Q4_K_M.gguf"); got != "Q4_K_M" {
		t.Errorf("got %q - \"unknown\" must fall through to a real source", got)
	}
	if got := quantLabel("unknown", ggufMeta{}, ""); got != "" {
		t.Errorf("got %q - \"unknown\" must not become a label", got)
	}
}

// The label is a NAME, so case is normalised: a publisher writing q4_k_m means the same
// weights as one writing Q4_K_M, and a filter must match both.
func TestQuantLabelsNormaliseCase(t *testing.T) {
	if got := quantLabel("q4_k_m", ggufMeta{}, ""); got != "Q4_K_M" {
		t.Errorf("got %q, want the upper-cased label", got)
	}
	if got := quantInName("model-q5_k_s.gguf"); got != "Q5_K_S" {
		t.Errorf("got %q, want Q5_K_S", got)
	}
}
