package protocol

import (
	"strings"
	"testing"
)

// Validate is the strict operator-input check (declaration.feature); Canonical is the lenient
// broker normalization. Whatever Validate accepts, Canonical must keep - an accepted value
// that then vanishes on air would be the very surprise Validate exists to prevent.
func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tx       Taxonomy
		modality string
		err      string // "" = accepted
	}{
		{"empty", Taxonomy{}, "", ""},
		{"full", Taxonomy{Category: "Vision", Subcategory: "OCR", Tags: []string{"PPE", "smoke fire"}, Tuning: "Finetune",
			BaseModel: "Qwen/Qwen3-VL-8B", ContentPolicy: "abliterated", License: "MIT"}, "", ""},
		{"category only", Taxonomy{Category: "signals"}, "", ""},
		{"unknown category", Taxonomy{Category: "medical"}, "", "unknown category"},
		{"sub without category", Taxonomy{Subcategory: "ocr"}, "", "needs a category"},
		{"foreign sub", Taxonomy{Category: "vision", Subcategory: "code"}, "", "not a vision subcategory"},
		{"chat claims tts", Taxonomy{Category: "audio", Subcategory: "tts"}, "", "only for a tts model"},
		{"tts as chat", Taxonomy{Category: "language"}, ModalityTTS, "always audio/tts"},
		{"stt wrong sub", Taxonomy{Category: "audio", Subcategory: "tts"}, ModalitySTT, "always audio/stt"},
		{"tts audio ok", Taxonomy{Category: "audio", Tags: []string{"radio"}}, ModalityTTS, ""},
		{"invalid tag", Taxonomy{Tags: []string{"a/b"}}, "", "invalid tag"},
		{"reserved tag", Taxonomy{Tags: []string{"Triage"}}, "", "reserved tag"},
		{"nine tags", Taxonomy{Tags: strings.Split("a,b,c,d,e,f,g,h,i", ",")}, "", "at most 8 tags"},
		{"eight tags with repeats", Taxonomy{Tags: strings.Split("a,b,c,d,e,f,g,h,a,A", ",")}, "", ""},
		{"thousands of tags", Taxonomy{Tags: make([]string, 5000)}, "", "invalid tag"},
		{"unknown tuning", Taxonomy{Tuning: "lora"}, "", "unknown tuning"},
		{"blank tuning", Taxonomy{Tuning: "  "}, "", ""},
		{"unknown policy", Taxonomy{ContentPolicy: "nsfw"}, "", "unknown content policy"},
		{"blank policy", Taxonomy{ContentPolicy: "  "}, "", ""},
		{"base model at limit", Taxonomy{BaseModel: strings.Repeat("b", 128)}, "", ""},
		{"base model over", Taxonomy{BaseModel: strings.Repeat("b", 129)}, "", "at most 128 characters"},
		{"license at limit", Taxonomy{License: strings.Repeat("許", 64)}, "", ""},
		{"license over", Taxonomy{License: strings.Repeat("l", 65)}, "", "at most 64 characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tx.Validate(tc.modality)
			if tc.err == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				c := tc.tx.Canonical(tc.modality)
				if len(c.Tags) != len(dedupe(tc.tx.Tags)) || (tc.tx.Category != "" && c.Category == "") ||
					(strings.TrimSpace(tc.tx.BaseModel) != "" && c.BaseModel == "") {
					t.Fatalf("accepted but Canonical dropped part of it: %+v -> %+v", tc.tx, c)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("want an error mentioning %q, got %v", tc.err, err)
			}
		})
	}
}

func dedupe(tags []string) []string {
	var out []string
	for _, raw := range tags {
		if tag, ok := canonicalTag(raw); ok && !strings.Contains(strings.Join(out, ","), tag) {
			out = append(out, tag)
		}
	}
	return out
}

func TestSplitCategoryAndDeclared(t *testing.T) {
	for in, want := range map[string][2]string{"vision/ocr": {"vision", "ocr"}, "vision": {"vision", ""}, "": {"", ""}, "a/b/c": {"a", "b/c"}} {
		if c, s := SplitCategory(in); c != want[0] || s != want[1] {
			t.Errorf("SplitCategory(%q) = %q,%q", in, c, s)
		}
	}
	if (Taxonomy{TaxonomySource: SourceDeclared}).Declared() {
		t.Error("a claim source alone is not a declaration")
	}
	for _, tx := range []Taxonomy{{Category: "x"}, {Subcategory: "x"}, {Tags: []string{"x"}}, {Tuning: "x"}, {BaseModel: "x"}, {ContentPolicy: "x"}, {License: "x"}} {
		if !tx.Declared() {
			t.Errorf("%+v should count as declared", tx)
		}
	}
}
