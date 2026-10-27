package detect

// THE ARCHITECTURE FIELDS, for deciding whether a model can run here before fetching it
// (features/serving/local_models.feature §4).
//
// This is a SECOND walk of the same header ggufMeta reads, and it is separate on purpose. That
// one answers "what should this row say" and has been correct for a long time; this one answers
// "will this run", which is a different question with a different failure mode - a display label
// that is wrong is a cosmetic bug, a fitness input that is wrong sends somebody a four gigabyte
// download onto a machine that cannot hold it. Keeping them apart means the risky new walk cannot
// break the settled one.
//
// WHY IT IS CHEAP. Everything here sits in the first megabyte of a GGUF, before the tokenizer
// arrays. Locally that is one read. Remotely it is one ranged request, which is why the same
// decision can be made about a model on HuggingFace without downloading it.
//
// THE KEYS ARE NAMESPACED BY THE FILE'S OWN ARCHITECTURE - "qwen3.block_count",
// "llama.attention.head_count_kv" - so there is no fixed key list to write. The architecture is
// read from general.architecture and the rest is resolved against it, and because a writer is
// not obliged to put that key first, every numeric key is collected and the resolution happens at
// the end.

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"

	"rogerai.fm/roger/v7/internal/catalog"
)

// WHY A SHAPE COULD NOT BE READ. These are values rather than a bare false because the four
// reasons mean completely different things to an owner, and three of them were found by running
// this against real model files rather than the ones the tests build:
//
//   - ErrNotReadable: not a GGUF, or truncated. Say so and move on.
//   - ErrIncomplete: a GGUF whose header does not carry what the estimate needs.
//   - ErrNotGenerative: an EMBEDDING model. nomic-bert has no key-value heads because it has no
//     key-value cache: it is not a thing you serve chat from. Refusing for "a missing field"
//     would send somebody hunting a bug that is not there.
//   - ErrNoPlainCache: an architecture whose cache is not the plain per-head kind the arithmetic
//     describes - MLA and its latent cache, or a hybrid with state-space layers. kimi-k3 declares
//     attention.kv_lora_rank and key_length_mla and is both. The formula would produce a
//     confident number that is simply wrong, which is worse than no number, so it declines and
//     the answer becomes "cannot say" until the runtime itself is asked.
var (
	ErrNotReadable   = errors.New("not a readable GGUF header")
	ErrIncomplete    = errors.New("the header does not carry the fields a memory estimate needs")
	ErrNotGenerative = errors.New("this is an embedding model, not one you serve chat from")
	ErrNoPlainCache  = errors.New("this architecture does not keep a plain key-value cache, so an estimate would be wrong")
)

// ReadShape reads what the fitness arithmetic needs from a GGUF on disk, or reports false.
//
// It is all-or-nothing by design: a HALF shape is worse than none, because the arithmetic would
// run on it and produce a confident number. The one thing it will substitute is the head
// dimension, which many files omit and which the runtime itself derives the same way.
func ReadShape(path string) (catalog.Shape, error) {
	f, err := os.Open(path)
	if err != nil {
		return catalog.Shape{}, ErrNotReadable
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() <= 0 {
		return catalog.Shape{}, ErrNotReadable
	}
	s, rerr := parseGGUFShape(io.LimitReader(f, ggufHeaderBudget))
	s.FileBytes = fi.Size()
	return s, rerr // on an error the shape carries only what the header SAID (Kind, BaseModel)
}

// ShapeFromHeader reads a shape from an already-fetched header prefix, for the remote case where
// the bytes came from a ranged request and the size came from the index. Same rules, no file.
func ShapeFromHeader(header []byte, fileBytes int64) (catalog.Shape, error) {
	if fileBytes <= 0 {
		return catalog.Shape{}, ErrNotReadable
	}
	s, err := parseGGUFShape(strings.NewReader(string(header)))
	if err != nil {
		return catalog.Shape{}, err
	}
	s.FileBytes = fileBytes
	return s, nil
}

// parseGGUFShape walks the KV block collecting every numeric key and the architecture, then
// resolves. A truncated or budget-exhausted read keeps whatever it had, exactly as the meta
// parser does: the resolution below decides whether that was enough.
func parseGGUFShape(r io.Reader) (catalog.Shape, error) {
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil || magic != ggufMagic {
		return catalog.Shape{}, ErrNotReadable
	}
	var version uint32
	if err := binary.Read(r, binary.LittleEndian, &version); err != nil || version < 2 || version > 3 {
		return catalog.Shape{}, ErrNotReadable
	}
	var tensorCount, kvCount uint64
	if err := binary.Read(r, binary.LittleEndian, &tensorCount); err != nil {
		return catalog.Shape{}, ErrNotReadable
	}
	if err := binary.Read(r, binary.LittleEndian, &kvCount); err != nil {
		return catalog.Shape{}, ErrNotReadable
	}
	if kvCount > 1<<20 {
		return catalog.Shape{}, ErrNotReadable
	}
	arch, kind, base, adapterType := "", "", "", ""
	nums := map[string]int{}
	counts := map[string]int{}
	for i := uint64(0); i < kvCount; i++ {
		key, err := ggufString(r)
		if err != nil {
			break // out of budget or truncated: resolve with what was read
		}
		var vtype uint32
		if err := binary.Read(r, binary.LittleEndian, &vtype); err != nil {
			break
		}
		switch vtype {
		case ggufTypeString:
			s, err := ggufString(r)
			if err != nil {
				break
			}
			switch key {
			case "general.architecture":
				arch = strings.TrimSpace(s)
			case "general.type":
				kind = strings.TrimSpace(s)
			case "general.base_model.0.name":
				base = strings.TrimSpace(s)
			case "adapter.type":
				adapterType = strings.TrimSpace(s)
			}
		case ggufTypeUint32, ggufTypeInt32:
			var n uint32
			if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
				break
			}
			nums[key] = int(n)
		case ggufTypeUint64, ggufTypeInt64:
			var n uint64
			if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
				break
			}
			nums[key] = int(n)
		case ggufTypeUint16, ggufTypeInt16:
			var n uint16
			if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
				break
			}
			nums[key] = int(n)
		case ggufTypeArray:
			// The LENGTH is what matters and it sits before the elements, so the tokenizer's
			// vocabulary is counted without any of it being read into memory.
			var elem uint32
			if err := binary.Read(r, binary.LittleEndian, &elem); err != nil {
				break
			}
			var n uint64
			if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
				break
			}
			counts[key] = int(n)
			bad := false
			for j := uint64(0); j < n && !bad; j++ {
				if err := skipGGUFValue(r, elem); err != nil {
					bad = true
				}
			}
			if bad {
				i = kvCount // the walk has desynchronised; stop rather than read nonsense
			}
		default:
			if err := skipGGUFValue(r, vtype); err != nil {
				i = kvCount
			}
		}
	}
	shape, err := resolveShape(arch, nums, counts)
	shape.Kind, shape.BaseModel, shape.Arch, shape.AdapterType = kind, base, arch, adapterType // said even when the arithmetic cannot be
	return shape, err
}

// compressedCacheFamily names the architecture families whose cache is not per-head, as a net
// under the key markers above. It matches on a PREFIX because the families are versioned
// (deepseek2, deepseek3, deepseek4) and a new version is compressed-cache too.
//
// It is deliberately short. Refusing to estimate is a real cost to an owner - they lose the
// pre-download answer and have to fetch first - so only families that are compressed by design
// belong here, never one added on a hunch.
func compressedCacheFamily(arch string) bool {
	a := strings.ToLower(strings.TrimSpace(arch))
	for _, fam := range []string{"deepseek", "kimi", "mamba", "jamba", "rwkv", "falcon-h", "falcon_h"} {
		if strings.HasPrefix(a, fam) {
			return true
		}
	}
	return false
}

// resolveShape turns the collected keys into a shape, or says why it could not. The order of the
// checks is the order of usefulness to whoever reads the answer.
func resolveShape(arch string, nums, counts map[string]int) (catalog.Shape, error) {
	if arch == "" {
		return catalog.Shape{}, ErrIncomplete // the namespaced keys cannot be found without it
	}
	at := func(suffix string) int { return nums[arch+"."+suffix] }
	has := func(suffix string) bool { _, ok := nums[arch+"."+suffix]; return ok }

	// AN ARCHITECTURE WHOSE CACHE IS NOT THE PLAIN KIND. A compressed latent cache, a recurrent
	// state, or a sparse indexed one: the per-head formula describes none of them, and a wrong
	// number delivered confidently is worse than no number. Checked BEFORE completeness, because
	// such a file usually looks complete - that is exactly how it gets through.
	//
	// TWO SIGNALS, because one was not enough and a real file proved it. The marker keys are the
	// primary test. But a deepseek4 file on this machine declares q_lora_rank and an indexer and
	// NOT kv_lora_rank, so a key list alone let it through and produced an estimate for an
	// architecture the formula does not describe. Hence the family list as a safety net: those
	// families are compressed-cache by design, whatever any single build happens to write.
	for _, k := range []string{
		"attention.kv_lora_rank", "attention.q_lora_rank",
		"attention.key_length_mla", "attention.value_length_mla",
		"attention.indexer.head_count", // sparse indexed attention
		"ssm.conv_kernel", "ssm.state_size", "ssm.inner_size",
	} {
		if has(k) {
			return catalog.Shape{}, ErrNoPlainCache
		}
	}
	if compressedCacheFamily(arch) {
		return catalog.Shape{}, ErrNoPlainCache
	}
	// AN EMBEDDING MODEL. It declares a pooling type and has no key-value heads because it has no
	// cache and generates nothing. Saying "a field is missing" would send somebody hunting a bug.
	if has("pooling_type") && !has("attention.head_count_kv") {
		return catalog.Shape{}, ErrNotGenerative
	}

	s := catalog.Shape{
		Layers:   at("block_count"),
		KVHeads:  at("attention.head_count_kv"),
		KeyDim:   at("attention.key_length"),
		ValueDim: at("attention.value_length"),
		TrainCtx: at("context_length"),
		Vocab:    at("vocab_size"),

		Experts:     at("expert_count"),
		ExpertsUsed: at("expert_used_count"),
	}
	// TWO DOCUMENTED DEFAULTS, not guesses: the runtime derives both the same way when the keys
	// are absent, and most real files leave at least one of them out.
	//   - no head_count_kv means no grouped-query attention, so every head keeps its own cache.
	//   - no key/value length means the head dimension is the embedding width over the heads.
	if s.KVHeads == 0 {
		s.KVHeads = at("attention.head_count")
	}
	if s.KeyDim == 0 || s.ValueDim == 0 {
		if emb, heads := at("embedding_length"), at("attention.head_count"); emb > 0 && heads > 0 {
			if s.KeyDim == 0 {
				s.KeyDim = emb / heads
			}
			if s.ValueDim == 0 {
				s.ValueDim = emb / heads
			}
		}
	}
	// Some writers omit vocab_size; the token array's length is the same number.
	if s.Vocab == 0 {
		s.Vocab = counts["tokenizer.ggml.tokens"]
	}
	if s.Layers <= 0 || s.KVHeads <= 0 || s.KeyDim <= 0 || s.ValueDim <= 0 || s.Vocab <= 0 {
		return catalog.Shape{}, ErrIncomplete
	}
	return s, nil
}
