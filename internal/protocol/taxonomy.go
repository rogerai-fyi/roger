package protocol

import (
	"slices"
	"strings"
)

// Taxonomy is an offer's optional description of what the model is FOR
// (MODEL-CATEGORIES-DESIGN-2026-10-05, features/categories/offer_taxonomy.feature).
//
// Nothing here is verified. Like Quant/Weights/Variant these are DISPLAY + FILTER attributes:
// the broker routes on none of them, and every field is omitempty and EXCLUDED from the
// registration possession proof (regSigningBytes), so a node and a broker on different
// binaries never disagree about the signed bytes. TaxonomySource says who made the claim -
// the operator ("declared") or the node's detector ("inferred"); "verified" is never accepted
// from the wire, the CapTools discipline.
type Taxonomy struct {
	Category       string   `json:"category,omitempty"`
	Subcategory    string   `json:"subcategory,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Tuning         string   `json:"tuning,omitempty"`
	BaseModel      string   `json:"base_model,omitempty"`
	ContentPolicy  string   `json:"content_policy,omitempty"`
	License        string   `json:"license,omitempty"`
	TaxonomySource string   `json:"taxonomy_source,omitempty"`
}

// Claim sources. Only these two can arrive from a node.
const (
	sourceDeclared = "declared"
	sourceInferred = "inferred"
)

// categories is the v1 CLOSED category -> subcategory list. A category groups browse and
// routing, so an unknown one is dropped (like an unknown capability) rather than becoming a
// new aisle on the dial for every typo. Growth goes into tags; a new entry here is a
// versioned, spec'd change.
var categories = map[string][]string{
	"language":   {"chat", "code", "reasoning", "summarize", "translate", "extraction", "classification", "creative"},
	"vision":     {"vlm", "ocr", "document", "detection", "segmentation", "classification"},
	"audio":      {"tts", "stt", "audio-classification"},
	"retrieval":  {"embeddings", "rerank"},
	"generation": {"image", "video", "music"},
	"signals":    {"time-series", "anomaly", "tabular"},
}

var knownTunings = map[string]bool{
	"base": true, "instruct": true, "finetune": true, "merge": true, "quantized": true, "adapter": true,
}

// contentPolicies maps every accepted spelling to its grade. "abliterated" is a technique for
// removing refusals; what a consumer cares about is the outcome, so it reads as uncensored.
var contentPolicies = map[string]string{
	"aligned": "aligned", "reduced-refusal": "reduced-refusal",
	"uncensored": "uncensored", "abliterated": "uncensored",
}

// reservedTags are dropped: a tag is a public claim about intended use, and the eight-markets
// rule (features/web/healthcare_defense_markets.feature) holds for it too - healthcare means
// equipment and facilities, never patients; defense means sustainment, never effects.
var reservedTags = map[string]bool{
	"diagnosis": true, "triage": true, "dosing": true, "radiology": true, "patient": true,
	"medical-imaging": true, "targeting": true, "target-acquisition": true, "weapons": true, "lethal": true,
}

const (
	maxTags       = 8
	tagMax        = 32
	baseModelMax  = 128
	licenseTextMx = 64
)

func normWord(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// normalize canonicalizes the taxonomy in place. modality is the offer's (already
// normalized) modality, which always wins over a declared category: it decides billing and
// routing, so a category can never contradict it.
func (t *Taxonomy) normalize(modality string) {
	t.Category = normWord(t.Category)
	t.Subcategory = normWord(t.Subcategory)
	if subs, ok := categories[t.Category]; !ok {
		t.Category, t.Subcategory = "", ""
	} else if !slices.Contains(subs, t.Subcategory) {
		t.Subcategory = ""
	}
	t.Tags = canonicalTags(t.Tags)
	if t.Tuning = normWord(t.Tuning); !knownTunings[t.Tuning] {
		t.Tuning = ""
	}
	t.BaseModel = boundedText(t.BaseModel, baseModelMax)
	t.ContentPolicy = contentPolicies[normWord(t.ContentPolicy)]
	t.License = boundedText(t.License, licenseTextMx)

	// Who made the claim is only meaningful when a claim survived; read before the modality
	// fix below, which is the broker's own derivation and not anyone's claim.
	if t.Category == "" && len(t.Tags) == 0 && t.Tuning == "" && t.BaseModel == "" &&
		t.ContentPolicy == "" && t.License == "" {
		t.TaxonomySource = ""
	} else if src := normWord(t.TaxonomySource); src == sourceInferred {
		t.TaxonomySource = sourceInferred
	} else {
		t.TaxonomySource = sourceDeclared
	}

	switch modality {
	case ModalityTTS, ModalitySTT:
		t.Category, t.Subcategory = "audio", modality
	default:
		if t.Subcategory == ModalityTTS || t.Subcategory == ModalitySTT {
			t.Subcategory = ""
		}
	}
}

// canonicalTags lowercases, hyphenates runs of space/underscore/hyphen, trims edge hyphens,
// and keeps only [a-z0-9-] tags of at most tagMax that are not reserved - deduplicated, in
// declared order, at most maxTags. A tag that fails is dropped, never repaired: a tag is a
// filter key, and a silently rewritten one would match things its author did not mean.
func canonicalTags(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range in {
		if len(out) == maxTags {
			break
		}
		tag, ok := canonicalTag(raw)
		if !ok || seen[tag] || reservedTags[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

func canonicalTag(raw string) (string, bool) {
	var b strings.Builder
	hyphen := false
	for _, r := range normWord(raw) {
		switch {
		case r == ' ' || r == '\t' || r == '_' || r == '-':
			hyphen = b.Len() > 0
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if hyphen {
				b.WriteByte('-')
				hyphen = false
			}
			b.WriteRune(r)
		default:
			return "", false
		}
	}
	tag := b.String()
	return tag, tag != "" && len(tag) <= tagMax
}
