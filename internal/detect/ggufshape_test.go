package detect

// THE ARCHITECTURE FIELDS a fitness decision needs, read from the same header the display axes
// come from (features/serving/local_models.feature §4).
//
// The point of reading them HERE is that the whole set arrives in the first megabyte of a GGUF,
// before the tokenizer arrays. So deciding whether a 4 GB model can run on this box costs one
// ranged read, not a download. The keys are namespaced by the architecture the file declares
// ("qwen3.block_count", "llama.attention.head_count_kv"), which is why general.architecture has
// to be read first and why this cannot be a fixed key list.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func shapeKVs() []kv {
	return []kv{
		{"general.architecture", ggufTypeString, "qwen3"},
		{"general.name", ggufTypeString, "Qwen3 4B"},
		{"qwen3.block_count", ggufTypeUint32, uint32(36)},
		{"qwen3.context_length", ggufTypeUint32, uint32(40960)},
		{"qwen3.embedding_length", ggufTypeUint32, uint32(2560)},
		{"qwen3.attention.head_count", ggufTypeUint32, uint32(32)},
		{"qwen3.attention.head_count_kv", ggufTypeUint32, uint32(8)},
		{"qwen3.attention.key_length", ggufTypeUint32, uint32(128)},
		{"qwen3.attention.value_length", ggufTypeUint32, uint32(128)},
		{"qwen3.vocab_size", ggufTypeUint32, uint32(151936)},
	}
}

func writeGGUF(t *testing.T, kvs []kv, size int64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "model.gguf")
	b := ggufBytes(t, 3, kvs)
	if pad := size - int64(len(b)); pad > 0 {
		b = append(b, make([]byte, pad)...)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAShapeIsReadFromTheHeaderAndTheFileSize(t *testing.T) {
	path := writeGGUF(t, shapeKVs(), 2503342080>>8) // a real size, scaled so the test is not 2 GB
	got, err := ReadShape(path)
	if err != nil {
		t.Fatalf("a complete header yielded no shape: %v", err)
	}
	for name, pair := range map[string][2]int{
		"layers": {got.Layers, 36}, "kv heads": {got.KVHeads, 8},
		"key dim": {got.KeyDim, 128}, "value dim": {got.ValueDim, 128},
		"trained context": {got.TrainCtx, 40960}, "vocab": {got.Vocab, 151936},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %d, want %d", name, pair[0], pair[1])
		}
	}
	// The file size is the file's, never a field inside it: the bytes on disk are what get mapped.
	if fi, err := os.Stat(path); err != nil || got.FileBytes != fi.Size() {
		t.Errorf("FileBytes = %d, want the real size on disk", got.FileBytes)
	}
}

// Many files omit key_length and value_length. The convention llama.cpp itself uses is
// embedding / heads, and taking it is better than refusing a shape we can compute.
func TestAMissingKeyLengthFallsBackTheWayTheRuntimeDoes(t *testing.T) {
	kvs := []kv{}
	for _, e := range shapeKVs() {
		if e.key == "qwen3.attention.key_length" || e.key == "qwen3.attention.value_length" {
			continue
		}
		kvs = append(kvs, e)
	}
	got, err := ReadShape(writeGGUF(t, kvs, 4096))
	if err != nil {
		t.Fatalf("a shape with no explicit head dim was refused: %v", err)
	}
	if got.KeyDim != 80 || got.ValueDim != 80 { // 2560 / 32
		t.Errorf("head dim = %d/%d, want 80/80 from embedding / heads", got.KeyDim, got.ValueDim)
	}
}

// A vocab_size key is not always present. The token array's LENGTH is, and its length prefix sits
// before the strings, so it is readable without paying for the array.
func TestTheVocabComesFromTheTokenArrayWhenItIsNotDeclared(t *testing.T) {
	kvs := []kv{}
	for _, e := range shapeKVs() {
		if e.key == "qwen3.vocab_size" {
			continue
		}
		kvs = append(kvs, e)
	}
	kvs = append(kvs, kv{"tokenizer.ggml.tokens", ggufTypeArray, []string{"a", "b", "c", "d", "e"}})
	got, err := ReadShape(writeGGUF(t, kvs, 4096))
	if err != nil {
		t.Fatalf("a shape with no vocab_size was refused: %v", err)
	}
	if got.Vocab != 5 {
		t.Errorf("vocab = %d, want the token array's length", got.Vocab)
	}
}

func TestAnIncompleteHeaderYieldsNoShapeRatherThanHalfOne(t *testing.T) {
	// NOT head_count_kv: its absence is a documented default (no grouped-query attention), which
	// TestTheKVHeadCountFallsBackToOnePerHead covers. These three have no fallback.
	for _, drop := range []string{"qwen3.block_count", "general.architecture"} {
		kvs := []kv{}
		for _, e := range shapeKVs() {
			if e.key != drop {
				kvs = append(kvs, e)
			}
		}
		if _, err := ReadShape(writeGGUF(t, kvs, 4096)); err == nil {
			t.Errorf("a header missing %q still produced a shape", drop)
		}
	}
	if _, err := ReadShape(filepath.Join(t.TempDir(), "nothing.gguf")); !errors.Is(err, ErrNotReadable) {
		t.Errorf("a file that does not exist gave %v, want ErrNotReadable", err)
	}
}

// The architecture prefix is the file's own, so a fixed key list would read nothing for any model
// that is not the one it was written for.
func TestTheKeysAreNamespacedByWhateverArchitectureTheFileDeclares(t *testing.T) {
	kvs := []kv{
		{"general.architecture", ggufTypeString, "llama"},
		{"llama.block_count", ggufTypeUint32, uint32(32)},
		{"llama.attention.head_count", ggufTypeUint32, uint32(32)},
		{"llama.attention.head_count_kv", ggufTypeUint32, uint32(8)},
		{"llama.embedding_length", ggufTypeUint32, uint32(4096)},
		{"llama.context_length", ggufTypeUint32, uint32(8192)},
		{"llama.vocab_size", ggufTypeUint32, uint32(128256)},
	}
	got, err := ReadShape(writeGGUF(t, kvs, 4096))
	if err != nil {
		t.Fatalf("a llama header yielded no shape: %v", err)
	}
	if got.Layers != 32 || got.KVHeads != 8 || got.TrainCtx != 8192 {
		t.Errorf("read the wrong namespace: %+v", got)
	}
}

// Most real files leave head_count_kv out when every head keeps its own cache. The runtime
// assumes that too, so refusing would reject a model that runs perfectly well.
func TestTheKVHeadCountFallsBackToOnePerHead(t *testing.T) {
	kvs := []kv{}
	for _, e := range shapeKVs() {
		if e.key != "qwen3.attention.head_count_kv" {
			kvs = append(kvs, e)
		}
	}
	got, err := ReadShape(writeGGUF(t, kvs, 4096))
	if err != nil {
		t.Fatalf("a model without grouped-query attention was refused: %v", err)
	}
	if got.KVHeads != 32 {
		t.Errorf("kv heads = %d, want the full head count", got.KVHeads)
	}
}

// FOUND BY RUNNING THIS AGAINST REAL MODELS ON THIS MACHINE, which is the only reason these two
// cases are here: both look like ordinary complete headers and both would have produced a
// confident, wrong answer.

// nomic-bert is an embedding model. It has no key-value heads because it has no cache and
// generates nothing, so the honest refusal names that rather than blaming a missing field.
func TestAnEmbeddingModelIsRefusedForBeingOne(t *testing.T) {
	kvs := []kv{
		{"general.architecture", ggufTypeString, "nomic-bert"},
		{"nomic-bert.block_count", ggufTypeUint32, uint32(12)},
		{"nomic-bert.context_length", ggufTypeUint32, uint32(2048)},
		{"nomic-bert.embedding_length", ggufTypeUint32, uint32(768)},
		{"nomic-bert.attention.head_count", ggufTypeUint32, uint32(12)},
		{"nomic-bert.pooling_type", ggufTypeUint32, uint32(1)},
	}
	_, err := ReadShape(writeGGUF(t, kvs, 4096))
	if !errors.Is(err, ErrNotGenerative) {
		t.Errorf("an embedding model gave %v, want ErrNotGenerative", err)
	}
}

// kimi-k3 keeps a compressed latent cache and has state-space layers. The per-head formula
// describes neither, so it declines rather than returning a number that is simply wrong.
func TestAnArchitectureWithoutAPlainCacheDeclinesToEstimate(t *testing.T) {
	for _, marker := range []string{"attention.kv_lora_rank", "attention.key_length_mla", "ssm.conv_kernel"} {
		kvs := append(shapeKVs(), kv{"qwen3." + marker, ggufTypeUint32, uint32(512)})
		_, err := ReadShape(writeGGUF(t, kvs, 4096))
		if !errors.Is(err, ErrNoPlainCache) {
			t.Errorf("a file declaring %s gave %v, want ErrNoPlainCache", marker, err)
		}
	}
}

// A deepseek4 file on this machine declares q_lora_rank and an attention indexer but NOT
// kv_lora_rank, so the marker list alone let it through and estimated for an architecture the
// formula does not describe. Both signals are pinned here: the wider marker set, and the family
// net that catches a build which writes none of them.
func TestACompressedCacheIsCaughtByTheMarkerOrTheFamily(t *testing.T) {
	for _, marker := range []string{"attention.q_lora_rank", "attention.indexer.head_count",
		"attention.kv_lora_rank", "ssm.inner_size"} {
		kvs := append(shapeKVs(), kv{"qwen3." + marker, ggufTypeUint32, uint32(1536)})
		if _, err := ReadShape(writeGGUF(t, kvs, 4096)); !errors.Is(err, ErrNoPlainCache) {
			t.Errorf("a file declaring %s gave %v, want ErrNoPlainCache", marker, err)
		}
	}
	// And a family that is compressed by design, even declaring none of the markers.
	for _, arch := range []string{"deepseek2", "deepseek4", "kimi-k3", "mamba", "rwkv7"} {
		kvs := []kv{
			{"general.architecture", ggufTypeString, arch},
			{arch + ".block_count", ggufTypeUint32, uint32(32)},
			{arch + ".attention.head_count", ggufTypeUint32, uint32(32)},
			{arch + ".attention.head_count_kv", ggufTypeUint32, uint32(8)},
			{arch + ".embedding_length", ggufTypeUint32, uint32(4096)},
			{arch + ".context_length", ggufTypeUint32, uint32(8192)},
			{arch + ".vocab_size", ggufTypeUint32, uint32(128256)},
		}
		if _, err := ReadShape(writeGGUF(t, kvs, 4096)); !errors.Is(err, ErrNoPlainCache) {
			t.Errorf("architecture %q gave %v, want ErrNoPlainCache", arch, err)
		}
	}
	// The net stays short: an ordinary per-head model is not caught by it.
	for _, arch := range []string{"qwen3", "llama", "gemma3", "phi3", "mistral"} {
		if compressedCacheFamily(arch) {
			t.Errorf("%q was refused by the family net and should not be", arch)
		}
	}
}
