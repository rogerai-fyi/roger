package protocol

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
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
	SourceDeclared = "declared"
	SourceInferred = "inferred"
)

// TaxonomyVersion is the version of the category list below; bump it with any change to it.
const TaxonomyVersion = 1

// CategoryDef is one category and its subcategories, in display order.
type CategoryDef struct {
	Name          string
	Subcategories []string
}

// categories is the v1 CLOSED category -> subcategory list, in canonical display order. A
// category groups browse and routing, so an unknown one is dropped (like an unknown
// capability) rather than becoming a new aisle on the dial for every typo. Growth goes into
// tags; a new entry here is a versioned, spec'd change.
var categories = []CategoryDef{
	{"language", []string{"chat", "code", "reasoning", "summarize", "translate", "extraction", "classification", "creative"}},
	{"vision", []string{"vlm", "ocr", "document", "detection", "segmentation", "classification"}},
	{"audio", []string{"tts", "stt", "audio-classification"}},
	{"retrieval", []string{"embeddings", "rerank"}},
	{"generation", []string{"image", "video", "music"}},
	{"signals", []string{"time-series", "anomaly", "tabular"}},
}

// Categories returns a copy of the category list, so no importer can widen the closed set.
func Categories() []CategoryDef {
	out := make([]CategoryDef, len(categories))
	for i, c := range categories {
		out[i] = CategoryDef{c.Name, slices.Clone(c.Subcategories)}
	}
	return out
}

func subcategoriesOf(cat string) ([]string, bool) {
	for _, c := range categories {
		if c.Name == cat {
			return c.Subcategories, true
		}
	}
	return nil, false
}

// KnownCategory reports whether cat (canonical form) is in the list.
func KnownCategory(cat string) bool { _, ok := subcategoriesOf(cat); return ok }

// KnownSubcategory reports whether sub belongs to cat, or to any category when cat is "".
func KnownSubcategory(cat, sub string) bool {
	for _, c := range categories {
		if (cat == "" || c.Name == cat) && slices.Contains(c.Subcategories, sub) {
			return true
		}
	}
	return false
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
	MaxTags       = 8
	tagMax        = 32
	baseModelMax  = 128
	licenseTextMx = 64
)

func normWord(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Canonical returns a normalized copy, for offers that reach a view without passing
// registration (shared-registry mirror, lazy learn, DB re-hydrate).
func (t Taxonomy) Canonical(modality string) Taxonomy {
	t.normalize(modality) // t is a copy; canonicalTags builds a fresh slice
	return t
}

// CanonicalTag canonicalizes one tag as an offer's tags are; ok is false when it is invalid
// or reserved.
func CanonicalTag(raw string) (string, bool) {
	tag, ok := canonicalTag(raw)
	return tag, ok && !reservedTags[tag]
}

// CanonicalContentPolicy returns the grade for a spelling, "" when it is unknown.
func CanonicalContentPolicy(s string) string { return contentPolicies[normWord(s)] }

// normalize canonicalizes the taxonomy in place. modality is the offer's (already
// normalized) modality, which always wins over a declared category: it decides billing and
// routing, so a category can never contradict it.
func (t *Taxonomy) normalize(modality string) {
	t.Category = normWord(t.Category)
	t.Subcategory = normWord(t.Subcategory)
	if subs, ok := subcategoriesOf(t.Category); !ok {
		t.Category, t.Subcategory = "", ""
	} else if !slices.Contains(subs, t.Subcategory) {
		t.Subcategory = ""
	}
	t.Tags = canonicalTags(t.Tags)
	if t.Tuning = normWord(t.Tuning); !knownTunings[t.Tuning] {
		t.Tuning = ""
	}
	t.BaseModel = boundedText(t.BaseModel, baseModelMax)
	t.ContentPolicy = CanonicalContentPolicy(t.ContentPolicy)
	t.License = boundedText(t.License, licenseTextMx)

	// Who made the claim is only meaningful when a claim survived. A voice offer's category is
	// always the broker's own derivation from its modality (below), never anyone's claim - also
	// on a re-normalize, where the derived category is already set.
	voice := modality == ModalityTTS || modality == ModalitySTT
	if (t.Category == "" || voice) && len(t.Tags) == 0 && t.Tuning == "" && t.BaseModel == "" &&
		t.ContentPolicy == "" && t.License == "" {
		t.TaxonomySource = ""
	} else if src := normWord(t.TaxonomySource); src == SourceInferred {
		t.TaxonomySource = SourceInferred
	} else {
		t.TaxonomySource = SourceDeclared
	}

	if voice {
		t.Category, t.Subcategory = "audio", modality
	} else if t.Subcategory == ModalityTTS || t.Subcategory == ModalitySTT {
		t.Subcategory = ""
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
		if len(out) == MaxTags {
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

// Validate checks an operator's declaration STRICTLY (features/categories/declaration.feature).
// The broker normalizes leniently - an unknown value is dropped - but an operator typing a
// category deserves to be told it was wrong rather than see it vanish. The messages name what
// is valid. modality is the offer's: a voice model is always audio/<modality>.
func (t Taxonomy) Validate(modality string) error {
	cat, sub := normWord(t.Category), normWord(t.Subcategory)
	if modality == ModalityTTS || modality == ModalitySTT {
		if (cat != "" && cat != "audio") || (sub != "" && sub != modality) {
			return fmt.Errorf("a %s model is always audio/%s", modality, modality)
		}
	} else if sub == ModalityTTS || sub == ModalitySTT {
		return fmt.Errorf("audio/%s is only for a %s model", sub, sub)
	}
	switch subs, ok := subcategoriesOf(cat); {
	case cat == "" && sub != "":
		return fmt.Errorf("subcategory %q needs a category", sub)
	case cat != "" && !ok:
		names := make([]string, len(categories))
		for i, c := range categories {
			names[i] = c.Name
		}
		return fmt.Errorf("unknown category %q (valid: %s)", t.Category, strings.Join(names, ", "))
	case sub != "" && !slices.Contains(subs, sub):
		return fmt.Errorf("%q is not a %s subcategory (valid: %s)", sub, cat, strings.Join(subs, ", "))
	}
	var tags []string
	for _, raw := range t.Tags {
		tag, ok := canonicalTag(raw)
		switch {
		case !ok:
			return fmt.Errorf("invalid tag %q (use a-z, 0-9 and -, at most %d characters)", raw, tagMax)
		case reservedTags[tag]:
			return fmt.Errorf("reserved tag %q", tag)
		case !slices.Contains(tags, tag):
			if tags = append(tags, tag); len(tags) > MaxTags {
				return fmt.Errorf("at most %d tags", MaxTags)
			}
		}
	}
	// Refused, not cut: the broker would truncate silently, and an operator's text arriving
	// shortened is the same surprise as a category arriving dropped.
	if n := utf8.RuneCountInString(strings.TrimSpace(t.BaseModel)); n > baseModelMax {
		return fmt.Errorf("base model: at most %d characters (got %d)", baseModelMax, n)
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(t.License)); n > licenseTextMx {
		return fmt.Errorf("license: at most %d characters (got %d)", licenseTextMx, n)
	}
	if v := normWord(t.Tuning); v != "" && !knownTunings[v] {
		return fmt.Errorf("unknown tuning %q (valid: base, instruct, finetune, merge, quantized, adapter)", t.Tuning)
	}
	if normWord(t.ContentPolicy) != "" && CanonicalContentPolicy(t.ContentPolicy) == "" {
		return fmt.Errorf("unknown content policy %q (valid: aligned, reduced-refusal, uncensored)", t.ContentPolicy)
	}
	return nil
}

// Declare validates an operator's declaration for an offer of modality and returns it in the
// canonical form it is stored in (no claim source: the source is stamped when it goes on air).
func (t Taxonomy) Declare(modality string) (Taxonomy, error) {
	if err := t.Validate(modality); err != nil {
		return Taxonomy{}, err
	}
	t = t.Canonical(modality)
	t.TaxonomySource = ""
	return t, nil
}

// OnAir is what an offer carries given a stored declaration and the inference: the declaration
// as a WHOLE, labelled declared, when a claim survives canonicalization; otherwise the inference.
// One label, one author (founder ruling 2026-10-05). A stored entry that canonicalizes to
// nothing - hand-edited to an unknown category, say - therefore never hides the inference.
func (t Taxonomy) OnAir(modality string, inferred Taxonomy) Taxonomy {
	t.TaxonomySource = ""
	if c := t.Canonical(modality); c.TaxonomySource != "" {
		c.TaxonomySource = SourceDeclared
		return c
	}
	return inferred
}

// SplitCategory splits "cat/sub" (the form operators type) into its parts.
func SplitCategory(s string) (cat, sub string) {
	cat, sub, _ = strings.Cut(s, "/")
	return cat, sub
}

// Declared reports whether any claim field is set (the claim source alone is not a claim).
func (t Taxonomy) Declared() bool {
	return t.Category != "" || t.Subcategory != "" || len(t.Tags) > 0 || t.Tuning != "" ||
		t.BaseModel != "" || t.ContentPolicy != "" || t.License != ""
}
