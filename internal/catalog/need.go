package catalog

// HOW MUCH MEMORY A MODEL NEEDS, BEFORE IT IS DOWNLOADED
// (features/serving/local_models.feature §4).
//
// This exists to answer ONE question honestly: is it worth spending gigabytes of somebody's
// bandwidth on a model that cannot run here? It is a PRE-DOWNLOAD estimate and nothing more.
// Once the file is on disk, llama.cpp has a fitness pass of its own - on by default, with a
// standalone binary that prints the settings it would choose - and that answer is authoritative.
// We do not re-derive one. A second opinion that disagrees with the runtime is worse than no
// second opinion, because it disagrees quietly.
//
// THE CASE THAT JUSTIFIES IT. A 2.33 GiB model file carries 5.63 GiB of KV cache at its trained
// context. "Does a 2.5 GB file fit in 8 GB?" says yes, and the machine then dies. The context,
// not the weights, is what kills edge boxes, and any answer that does not name the context it was
// computed at is not an answer.
//
// EVERY FORMULA HERE WAS CHECKED AGAINST A LIVE RUN rather than derived on paper: llama.cpp logs
// its KV and output buffer sizes at startup, and the tests carry those exact figures. The one
// term that is NOT exact is the compute buffer, which has no clean closed form; it is budgeted
// generously and the runtime corrects it after the download. That is said out loud rather than
// dressed up as precision.

import "strings"

// Shape is what a model's own header says about itself: the only inputs the arithmetic takes from
// the model side. Every one of them is readable from the first megabyte of a GGUF file, or from
// the index that describes it, so a decision costs a megabyte rather than a download.
type Shape struct {
	FileBytes int64 // exact, from the index or the file on disk. Never estimated.
	Layers    int   // <arch>.block_count
	KVHeads   int   // <arch>.attention.head_count_kv
	KeyDim    int   // <arch>.attention.key_length
	ValueDim  int   // <arch>.attention.value_length
	TrainCtx  int   // <arch>.context_length - what it was trained for, the ceiling we propose
	Vocab     int   // the tokenizer's size, for the output buffer

	// A mixture of experts reads only ExpertsUsed of its Experts per token, which changes how fast
	// it answers but not what it needs to fit. Both zero for a dense model.
	Experts     int // <arch>.expert_count
	ExpertsUsed int // <arch>.expert_used_count

	// What the header says the file IS (general.type): "model", "adapter" (a LoRA that goes
	// beside its base) or "mmproj" (a vision projector). Empty when the header does not say.
	// Carried even when the shape is incomplete, because whether a file is a model at all is
	// decided before any arithmetic is tried.
	Kind      string
	BaseModel string // general.base_model.0.name: what an adapter was trained on, when recorded
	Arch      string // general.architecture, as the header spells it
	// AdapterType is adapter.type for a Kind of "adapter": "lora", or "control_vector", which
	// llama-server takes through another flag and would answer nonsense under --lora.
	AdapterType string
}

// CacheKind is the width of one KV element, which the owner can choose and which roughly halves
// the cache each step down. Expressed as a RATIO rather than a float so the arithmetic is exact:
// q8_0 is 34 bytes per 32 elements, and 1.0625 in floating point invites rounding drift into a
// number people will check against a log line.
type CacheKind struct {
	Name string
	Num  int64 // bytes
	Den  int64 // per this many elements
}

var (
	CacheF16  = CacheKind{"f16", 2, 1}
	CacheBF16 = CacheKind{"bf16", 2, 1}
	CacheF32  = CacheKind{"f32", 4, 1}
	CacheQ8_0 = CacheKind{"q8_0", 34, 32}
	CacheQ4_0 = CacheKind{"q4_0", 18, 32}
)

// needHeadroom is what we leave the rest of the machine. It matches the runtime's own default for
// the same purpose ON PURPOSE, so our pre-download answer and its post-download answer agree
// rather than arguing in front of the owner.
const needHeadroom = int64(1024) << 20

// minComputeReserve floors the compute buffer, which scales with the batch and the model's width
// and has no closed form we are willing to claim.
const minComputeReserve = int64(128) << 20

// Need is the memory one model needs at ONE context, broken into its terms. It is broken up
// because the spec requires the owner to be able to see the sum rather than be handed a verdict:
// a number you cannot check is a number you cannot argue with.
type Need struct {
	Weights  int64 // the file itself, which is mapped in
	KV       int64 // the cache, at the context this was computed for
	Output   int64 // the logits buffer
	Compute  int64 // the scratch the runtime needs, budgeted not derived
	Headroom int64 // what is left for the rest of the machine
	Total    int64 // the sum of the above, and what a budget is compared against
	Ctx      int   // the context it was computed at, because a total without one means nothing
}

// NeedAt computes the need at a given context and parallelism. It returns false rather than a
// guess when the shape is missing anything the sum requires: an unknown input must never become a
// confident answer, because the owner acts on it by spending bandwidth.
func NeedAt(s Shape, ctx, parallel int, k, v CacheKind) (Need, bool) {
	if s.FileBytes <= 0 || s.Layers <= 0 || s.KVHeads <= 0 || s.KeyDim <= 0 || s.ValueDim <= 0 ||
		s.Vocab <= 0 || ctx <= 0 || k.Den <= 0 || v.Den <= 0 {
		return Need{}, false
	}
	if parallel < 1 {
		parallel = 1
	}
	n := Need{Weights: s.FileBytes, Ctx: ctx, Headroom: needHeadroom}
	n.KV = kvBytes(s, ctx, k, v)
	n.Output = int64(s.Vocab) * int64(parallel) * 4
	n.Compute = minComputeReserve
	if by := s.FileBytes / 20; by > n.Compute { // five per cent of the weights
		n.Compute = by
	}
	n.Total = n.Weights + n.KV + n.Output + n.Compute + n.Headroom
	return n, true
}

// kvBytes is the cache, exactly: ctx * layers * kv-heads * (key + value) per token. Integer
// arithmetic throughout, with the two ratios put over a common denominator so a quantised cache
// does not lose a byte to rounding.
func kvBytes(s Shape, ctx int, k, v CacheKind) int64 {
	perToken := (int64(s.KeyDim)*k.Num*v.Den + int64(s.ValueDim)*v.Num*k.Den)
	return int64(ctx) * int64(s.Layers) * int64(s.KVHeads) * perToken / (k.Den * v.Den)
}

// ctxStep is the granularity MaxCtxFor answers in. A context is a batching quantity, and offering
// somebody 7331 tokens would be answering a question nobody asked.
const ctxStep = 256

// MaxCtxFor is the largest context this model can run in at this budget, rounded down to
// something worth saying. It exists because refusing is not an answer an owner can act on: the
// useful reply to "it does not fit" is "it fits at this size".
//
// It returns false when NO context works, which is the honest answer for a machine that cannot
// hold the weights at all - and that check is separate on purpose, because swap will happily let
// a machine pretend otherwise right up until it stops responding.
func MaxCtxFor(s Shape, budget int64, parallel int, k, v CacheKind) (int, bool) {
	probe, ok := NeedAt(s, ctxStep, parallel, k, v)
	if !ok || budget <= 0 {
		return 0, false
	}
	if probe.Weights+probe.Output+probe.Compute+probe.Headroom >= budget {
		return 0, false // no room for a cache of any size: the weights alone have lost
	}
	best := 0
	for ctx := ctxStep; ; ctx += ctxStep {
		if s.TrainCtx > 0 && ctx > s.TrainCtx {
			break // never propose more context than the model was trained for
		}
		n, ok := NeedAt(s, ctx, parallel, k, v)
		if !ok || n.Total > budget {
			break
		}
		best = ctx
	}
	// The trained context itself is a legitimate answer and is not a multiple of the step.
	if s.TrainCtx > 0 && best > 0 && best+ctxStep > s.TrainCtx {
		if n, ok := NeedAt(s, s.TrainCtx, parallel, k, v); ok && n.Total <= budget {
			best = s.TrainCtx
		}
	}
	return best, best > 0
}

// bitsPerWeight is the real cost of a quantisation, from ggml's own block layouts rather than
// from the name. "Q4" is not four bits: a q4_0 block is 2 bytes of scale plus 16 bytes of nibbles
// for 32 weights, which is 4.5. Guessing from the digit in the name under-reports every K-quant.
var bitsPerWeight = map[string]float64{
	"Q4_0": 4.5, "Q4_1": 5.0, "Q5_0": 5.5, "Q5_1": 6.0, "Q8_0": 8.5, "Q8_1": 9.0,
	"Q2_K": 2.625, "Q3_K": 3.4375, "Q4_K": 4.5, "Q5_K": 5.5, "Q6_K": 6.5625, "Q8_K": 8.5,
	"IQ1_S": 1.5625, "IQ2_XXS": 2.0625, "IQ2_XS": 2.3125, "IQ3_XXS": 3.0625, "IQ4_XS": 4.25,
	"IQ4_NL": 4.5,
	"F16":    16, "BF16": 16, "F32": 32, "F64": 64,
}

// BitsPerWeight is how many bits one weight costs at this quantisation, for RANKING models that
// are not downloaded yet. It is never used where a real file size is available: the file size is
// exact and this is not.
//
// It reports false for a quantisation it does not know, because inventing a size for an unfamiliar
// label is how an owner ends up downloading something twice the size they were told.
func BitsPerWeight(quant string) (float64, bool) {
	q := strings.ToUpper(strings.TrimSpace(quant))
	if b, ok := bitsPerWeight[q]; ok {
		return b, true
	}
	// The K-quants carry a size suffix (Q4_K_M, Q3_K_S) that does not change the block layout.
	if i := strings.LastIndex(q, "_"); i > 0 {
		if b, ok := bitsPerWeight[q[:i]]; ok {
			return b, true
		}
	}
	return 0, false
}
