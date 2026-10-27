package detect

import (
	"regexp"
	"slices"
	"strings"

	"rogerai.fm/roger/v6/internal/protocol"
)

// inferRules are the ordered, conservative id hints behind InferTaxonomy
// (features/categories/inference.feature). The FIRST match wins, so the order is the policy:
// a reranker named after an embedding family is a reranker; an OCR model that also sees
// images is OCR. Image-capable models (detected capability or the shared vision hint set)
// are checked after these and before code, so a coder that sees images reads as vision.
var inferRules = []struct {
	category, sub string
	re            *regexp.Regexp
}{
	{"retrieval", "rerank", regexp.MustCompile(`rerank|cross-encoder`)},
	{"retrieval", "embeddings", regexp.MustCompile(`embed|bge-|minilm|(^|[/_-])(e5|gte)-`)},
	// "ocr" as a word end or after a separator: olmocr, got-ocr2, paddleocr - not socratic.
	{"vision", "ocr", regexp.MustCompile(`ocr($|[^a-z])|(^|[^a-z])ocr`)},
	{"vision", "document", regexp.MustCompile(`docling|layoutlm|donut`)},
	{"vision", "detection", regexp.MustCompile(`yolo|detr|grounding-dino`)},
}

// codeHint: "coder" only as its own word (qwen3-coder, deepseek-coder), never inside
// encoder/decoder; plus the code families whose names do not say "coder".
var codeHint = regexp.MustCompile(`(^|[^a-z])coder|starcoder|codestral|codellama|codegemma|codeqwen|devstral|granite-code|stable-code`)

// uncensoredHint marks a model whose name says its refusals were removed.
var uncensoredHint = regexp.MustCompile(`abliterated|uncensored`)

// InferTaxonomy is the node's best-effort guess at what a model is for, from what detection
// already knows: its id, detected modality and detected capabilities. Labelled "inferred";
// it never invents tags, tuning, lineage or a license.
func InferTaxonomy(model, modality string, caps []string) protocol.Taxonomy {
	id := strings.ToLower(strings.TrimSpace(model))
	if id == "" {
		return protocol.Taxonomy{}
	}
	t := protocol.Taxonomy{TaxonomySource: protocol.SourceInferred}
	t.Category, t.Subcategory = inferCategory(id, modality, caps)
	if uncensoredHint.MatchString(id) {
		t.ContentPolicy = "uncensored"
	}
	return t
}

func inferCategory(id, modality string, caps []string) (string, string) {
	if modality == protocol.ModalityTTS || modality == protocol.ModalitySTT {
		return "audio", modality
	}
	for _, r := range inferRules {
		if r.re.MatchString(id) {
			return r.category, r.sub
		}
	}
	if slices.Contains(caps, protocol.CapVision) || visionFromID(id) {
		return "vision", "vlm"
	}
	if codeHint.MatchString(id) {
		return "language", "code"
	}
	return "language", "chat"
}

// InferredTaxonomy is the guess for one model this scan found.
func (f Found) InferredTaxonomy(model string) protocol.Taxonomy {
	return InferTaxonomy(model, f.Modality[model], f.Capabilities[model])
}
