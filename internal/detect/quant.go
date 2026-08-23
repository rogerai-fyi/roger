package detect

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"rogerai.fm/roger/v6/internal/protocol"
)

// THE QUANT LABEL: what an operator and a consumer both call these weights.
//
// The label is stored VERBATIM, never bucketed into "4-bit". Q4_K_M and IQ4_XS are both
// four-bit and people choose between them on purpose (r/LocalLLaMA is full of exactly that
// argument), so collapsing them destroys the distinction the whole feature exists to make.
//
// Three sources, in descending order of how much they actually know:
//
//  1. the runtime's own string  - ollama's details.quantization_level ("Q4_K_M")
//  2. the file's general.file_type - the enum llama.cpp stamped when it quantized
//  3. the FILE NAME              - "…-Q4_K_M.gguf", which is what the publisher called it
//
// A model ID is NOT a source. An id containing "Q4_K_M" is a string someone typed; the
// loaded file and the runtime are what the process is really running. Reading the id would
// let a station be honestly mislabelled by whoever named it.

// ftypeLabels maps general.file_type to the label people actually say.
//
// Transcribed from llama.cpp's LLAMA_FTYPE enum (include/llama.h). The gaps are real: 4-6
// and 33-35 are removed types, and a file still carrying one is not given a modern label
// it does not have. An UNKNOWN value maps to nothing rather than to a guess - a wrong
// quant label is worse than an absent one, because a consumer filtering on Q4_K_M would
// silently get something else.
var ftypeLabels = map[uint32]string{
	0: "F32", 1: "F16",
	2: "Q4_0", 3: "Q4_1",
	7: "Q8_0", 8: "Q5_0", 9: "Q5_1",
	10: "Q2_K", 11: "Q3_K_S", 12: "Q3_K_M", 13: "Q3_K_L",
	14: "Q4_K_S", 15: "Q4_K_M", 16: "Q5_K_S", 17: "Q5_K_M", 18: "Q6_K",
	19: "IQ2_XXS", 20: "IQ2_XS", 21: "Q2_K_S", 22: "IQ3_XS", 23: "IQ3_XXS",
	24: "IQ1_S", 25: "IQ4_NL", 26: "IQ3_S", 27: "IQ3_M", 28: "IQ2_S", 29: "IQ2_M",
	30: "IQ4_XS", 31: "IQ1_M", 32: "BF16",
	36: "TQ1_0", 37: "TQ2_0", 38: "MXFP4_MOE", 39: "NVFP4",
}

// quantFromFileType renders a file_type enum, or "" when the value is not one we can name.
func quantFromFileType(v uint32, set bool) string {
	if !set {
		return ""
	}
	return ftypeLabels[v]
}

// quantInName finds a quant label inside a GGUF FILE NAME.
//
// Anchored to a separator on both sides so "Q4_K_M" is matched in
// "Qwen3.8-27B-Q4_K_M.gguf" but a model called "IQ1" never turns a name into a quant by
// accident. Case-insensitive because publishers are not consistent, then upper-cased,
// because the label is a name and "q4_k_m" and "Q4_K_M" are the same weights.
var quantNameRe = regexp.MustCompile(`(?i)(^|[-_.])(` +
	`IQ[1-4](_[A-Z]+)+|` + // IQ4_XS, IQ2_XXS, IQ3_M
	`Q[2-8]_K(_[SML])?|` + // Q4_K_M, Q6_K, Q2_K_S
	`Q[2-8]_[01]|` + // Q4_0, Q5_1, Q8_0
	`TQ[12]_0|MXFP4(_MOE)?|NVFP4|BF16|FP16|F16|FP32|F32|FP8|AWQ|GPTQ|` +
	// MLX (Apple Silicon) names its quants by bit width, not by llama.cpp's K-quant
	// scheme: "Qwen3-30B-A3B-4bit", "...-8bit-DWQ". DWQ is a distinct RECIPE at the same
	// width, so it is kept rather than folded into "4bit" - the same reason Q4_K_M and
	// IQ4_XS stay apart.
	`[3468]BIT(-DWQ)?|DWQ` +
	`)($|[-_.])`)

// canonicalQuantCase fixes the ONE family where upper-casing changes the name rather
// than normalising it. llama.cpp's labels are published upper-case ("Q4_K_M"), so
// upper-casing is a no-op that makes "q4_k_m" agree with them. MLX's are published
// lower-case ("4bit", "8bit-DWQ"), and "4BIT" is a spelling no publisher uses - it would
// not match what an operator sees in LM Studio or on the hub, and two stations serving
// the same weights would end up on different rows depending on which runtime reported.
// It delegates to protocol, which is the layer detection and the consumer share: the
// canonical form has to be identical at every hop, and two copies of this rule is how the
// wire came to disagree with what detection produced.
func canonicalQuantCase(s string) string { return protocol.CanonicalQuant(s) }

func quantInName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	m := quantNameRe.FindStringSubmatch(base)
	if len(m) < 3 {
		return ""
	}
	return canonicalQuantCase(strings.ToUpper(m[2]))
}

// quantLabel resolves the best available label from every source, most-authoritative
// first. runtime is what the server said about the LOADED model (ollama's
// quantization_level); meta is the file's own header; path is the loaded file.
//
// The order matters and is not arbitrary: the runtime describes what is in memory right
// now, the header describes the file it was built from, and the name is what a human
// called it. They usually agree; when they do not, the earlier one is the one serving
// requests.
func quantLabel(runtime string, meta ggufMeta, path string) string {
	if s := strings.ToUpper(strings.TrimSpace(runtime)); s != "" && s != "UNKNOWN" {
		return canonicalQuantCase(s)
	}
	if s := quantFromFileType(meta.FileType, meta.FileTypeSet); s != "" {
		return s
	}
	return quantInName(path)
}

// ── DECODE TARGETS ───────────────────────────────────────────────────────────
//
// These live HERE, not in detect.go, and that placement is enforced by a spec.
// features/share/hosting_compatibility.feature asserts that detect.go contains no
// reference to quantization, weight downloads, GPU offload or child processes, on the
// reasoning that "this is the file that would need to know about model files,
// quantization, or child processes" if RogerAI ever drifted from a protocol client into an
// inference-engine wrapper.
//
// That guardrail is right and this change keeps it: detect.go ORCHESTRATES (it asks an
// HTTP endpoint and hands the body over), while every byte of quant-format knowledge sits
// in this file and gguf.go. Nothing here downloads weights, sets a GPU layer count, or
// starts a process - it reads labels the runtime and the file already carry.

// OllamaDetails is the per-model block Ollama returns on /api/tags and /api/show.
type OllamaDetails struct {
	QuantizationLevel string `json:"quantization_level"`
}

// OllamaTagModel is one entry of Ollama's /api/tags fleet listing.
type OllamaTagModel struct {
	Name    string        `json:"name"`
	Model   string        `json:"model"`
	Details OllamaDetails `json:"details"`
}

// quantFromDetails renders the runtime's own label, or "" when it said nothing usable.
func quantFromDetails(d OllamaDetails) string { return quantLabel(d.QuantizationLevel, ggufMeta{}, "") }

// quantFromShow resolves a label from an /api/show response: the runtime string first,
// then general.file_type out of the GGUF key/value map.
func quantFromShow(d OllamaDetails, fileType uint32, fileTypeSet bool) string {
	return quantLabel(d.QuantizationLevel, ggufMeta{FileType: fileType, FileTypeSet: fileTypeSet}, "")
}

// modelInfoVariants pulls the publisher axes out of Ollama's model_info - the GGUF
// key/value map, which is why the context window is read from "<arch>.context_length"
// elsewhere. Both keys are optional in the spec and usually absent; an absent key returns
// "" and is never presented as a value.
func modelInfoVariants(info map[string]json.RawMessage) (weights, variant string) {
	return ggufStringKey(info, "general.quantized_by"), ggufStringKey(info, "general.finetune")
}

// ggufStringKey reads one string out of a GGUF key/value map decoded as JSON.
func ggufStringKey(info map[string]json.RawMessage, key string) string {
	raw, ok := info[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// ggufFileTypeKey reads general.file_type. The bool separates "absent" from a real 0,
// which means all-F32 and is a value rather than a gap.
func ggufFileTypeKey(info map[string]json.RawMessage) (uint32, bool) {
	raw, ok := info["general.file_type"]
	if !ok {
		return 0, false
	}
	var n uint32
	if json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	return n, true
}

// headerVariants renders a file header into the three display labels. path is the loaded
// file, used as the last-resort source for the compression label.
func headerVariants(meta ggufMeta, path string) (quant, weights, variant string) {
	return quantLabel("", meta, path), meta.QuantizedBy, meta.Finetune
}

// LMStudioModel is one entry of LM Studio's /api/v0/models. It carries the variant axes
// for BOTH formats it serves: GGUF quants ("Q4_K_M") and MLX quants ("4bit"), told apart
// by CompatibilityType. Publisher is the "who built these weights" axis - the same thing
// the GGUF header calls general.quantized_by.
type LMStudioModel struct {
	ID                string `json:"id"`
	Quantization      string `json:"quantization"`
	Publisher         string `json:"publisher"`
	CompatibilityType string `json:"compatibility_type"`
	LoadedCtx         int    `json:"loaded_context_length"`
	MaxCtx            int    `json:"max_context_length"`
}

// lmStudioVariants reads the quant and the weights producer off one LM Studio entry.
//
// Everything returned is REPORTED BY THE SERVER about the file it loaded - nothing is
// inferred from the model id. An entry that omits a field yields empty, which renders as
// absent.
func lmStudioVariants(m LMStudioModel) (quant, weights string) {
	quant = canonicalQuantCase(strings.ToUpper(strings.TrimSpace(m.Quantization)))
	if strings.EqualFold(quant, "UNKNOWN") {
		quant = ""
	}
	// A publisher is only a WEIGHTS producer when someone re-published the weights.
	// "lmstudio-community" and "mlx-community" are exactly that. The field is taken
	// verbatim either way; deciding which publishers "count" would be an editorial call
	// this layer has no business making.
	return quant, strings.TrimSpace(m.Publisher)
}
