package catalog_test

// THE PRE-DOWNLOAD ARITHMETIC (features/serving/local_models.feature §4).
//
// Its one job is to never say "fits" when it does not, BEFORE gigabytes move. Once the file is on
// disk the runtime's own fitness pass is authoritative and this is not consulted again.
//
// THE NUMBERS BELOW ARE NOT INVENTED. Each was verified against a live llama.cpp run during the
// research of 2026-09-30: the server logs its KV and output buffer sizes at startup, and these
// are those figures. A formula that disagrees with the runtime is worse than no formula, because
// it disagrees quietly.
//
// The case that justifies the whole thing: a 2.33 GiB model file with 5.63 GiB of KV cache at its
// trained context. "Does a 2.5 GB file fit in 8 GB?" says yes, and the machine then dies. The
// context, not the weights, is what kills edge boxes.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/catalog"
)

const mib = int64(1) << 20
const gib = int64(1) << 30

// smolLM2 is the model the research measured against, at the context it measured.
func smolLM2() catalog.Shape {
	return catalog.Shape{FileBytes: 100 * mib, Layers: 30, KVHeads: 3, KeyDim: 64, ValueDim: 64,
		TrainCtx: 8192, Vocab: 49152}
}

// qwen3_4B is the case that kills an edge box if only the weights are counted.
func qwen3_4B() catalog.Shape {
	return catalog.Shape{FileBytes: 2503342080, Layers: 36, KVHeads: 8, KeyDim: 128, ValueDim: 128,
		TrainCtx: 40960, Vocab: 151936}
}

func TestTheKVCacheMatchesWhatTheRuntimeReports(t *testing.T) {
	// Verified live: llama.cpp logged "K (f16): 45.00 MiB, V (f16): 45.00 MiB, size = 90.00 MiB"
	// for SmolLM2-135M at -c 4096.
	n, ok := catalog.NeedAt(smolLM2(), 4096, 4, catalog.CacheF16, catalog.CacheF16)
	require.True(t, ok)
	require.Equal(t, 90*mib, n.KV, "the KV cache does not match what the runtime reports")

	// Verified live: "CPU output buffer size = 0.75 MiB" with vocab 49152 and 4 parallel slots.
	require.Equal(t, 786432, int(n.Output))

	// The weights are the file, never an estimate: the file is what gets mapped in.
	require.Equal(t, 100*mib, n.Weights)
}

func TestTheContextIsWhatKillsAnEdgeBox(t *testing.T) {
	n, ok := catalog.NeedAt(qwen3_4B(), 40960, 1, catalog.CacheF16, catalog.CacheF16)
	require.True(t, ok)
	// 36 layers x 8 kv heads x 128 dim x 40960 ctx x 2 bytes x 2 sides = 5.625 GiB.
	require.Equal(t, int64(6039797760), n.KV)
	require.Greater(t, n.KV, n.Weights, "the cache is bigger than the model and that is the point")

	// The naive check says an 8 GiB machine is fine. The real one does not.
	require.Equal(t, catalog.FitFits, catalog.AssessFit(8*gib, n.Weights),
		"the naive check that only counts weights")
	require.Equal(t, catalog.FitWontFit, catalog.AssessFit(8*gib, n.Total),
		"counting the whole need must refuse what the naive check allowed")
}

func TestAQuantisedCacheCostsLess(t *testing.T) {
	f16, _ := catalog.NeedAt(qwen3_4B(), 8192, 1, catalog.CacheF16, catalog.CacheF16)
	q8, _ := catalog.NeedAt(qwen3_4B(), 8192, 1, catalog.CacheQ8_0, catalog.CacheQ8_0)
	q4, _ := catalog.NeedAt(qwen3_4B(), 8192, 1, catalog.CacheQ4_0, catalog.CacheQ4_0)
	require.Less(t, q8.KV, f16.KV)
	require.Less(t, q4.KV, q8.KV)
	// q8_0 is 34 bytes per 32 elements against f16's 2 bytes: roughly half.
	require.InDelta(t, float64(f16.KV)*1.0625/2, float64(q8.KV), float64(mib))
}

func TestTheSumIsBrokenIntoItsTermsSoItCanBeShown(t *testing.T) {
	n, ok := catalog.NeedAt(qwen3_4B(), 4096, 1, catalog.CacheF16, catalog.CacheF16)
	require.True(t, ok)
	require.Equal(t, n.Weights+n.KV+n.Output+n.Compute+n.Headroom, n.Total,
		"the total must be the sum of the terms, or the owner cannot check it")
	for name, term := range map[string]int64{"weights": n.Weights, "kv": n.KV,
		"output": n.Output, "compute": n.Compute, "headroom": n.Headroom} {
		require.Greater(t, term, int64(0), "%s is not accounted for", name)
	}
	// The headroom matches the runtime's own default, so our answer and its answer agree.
	require.Equal(t, 1024*mib, n.Headroom)
}

func TestAShapeThatCannotBeComputedSaysSoRatherThanGuessing(t *testing.T) {
	for name, s := range map[string]catalog.Shape{
		"no file size": {Layers: 30, KVHeads: 3, KeyDim: 64, ValueDim: 64, Vocab: 100},
		"no layers":    {FileBytes: mib, KVHeads: 3, KeyDim: 64, ValueDim: 64, Vocab: 100},
		"no kv heads":  {FileBytes: mib, Layers: 30, KeyDim: 64, ValueDim: 64, Vocab: 100},
		"no key dim":   {FileBytes: mib, Layers: 30, KVHeads: 3, ValueDim: 64, Vocab: 100},
		"no vocab":     {FileBytes: mib, Layers: 30, KVHeads: 3, KeyDim: 64, ValueDim: 64},
	} {
		_, ok := catalog.NeedAt(s, 4096, 1, catalog.CacheF16, catalog.CacheF16)
		require.False(t, ok, "%s: an incomputable shape returned an answer", name)
	}
	// And a context that makes no sense is not an answer either.
	_, ok := catalog.NeedAt(smolLM2(), 0, 1, catalog.CacheF16, catalog.CacheF16)
	require.False(t, ok)
}

// It does not merely refuse: it says the largest context that WOULD fit, which is the answer an
// owner can act on.
func TestItSolvesForTheBiggestContextThatFits(t *testing.T) {
	s := qwen3_4B()
	budget := 8 * gib
	ctx, ok := catalog.MaxCtxFor(s, budget, 1, catalog.CacheF16, catalog.CacheF16)
	require.True(t, ok, "a model whose weights fit must yield some workable context")
	require.Greater(t, ctx, 0)
	require.Less(t, ctx, s.TrainCtx, "the full context does not fit in 8 GiB and it said so")

	// What it returns really does fit, and one step more does not.
	at, _ := catalog.NeedAt(s, ctx, 1, catalog.CacheF16, catalog.CacheF16)
	require.LessOrEqual(t, at.Total, budget)
	over, _ := catalog.NeedAt(s, ctx+256, 1, catalog.CacheF16, catalog.CacheF16)
	require.Greater(t, over.Total, budget, "it left room it could have used")

	// It never proposes a context beyond what the model was trained for.
	roomy, ok := catalog.MaxCtxFor(s, 200*gib, 1, catalog.CacheF16, catalog.CacheF16)
	require.True(t, ok)
	require.Equal(t, s.TrainCtx, roomy)
}

func TestAMachineTooSmallForTheWeightsAloneIsNeverToldItFits(t *testing.T) {
	s := qwen3_4B() // a 2.33 GiB file
	_, ok := catalog.MaxCtxFor(s, 2*gib, 1, catalog.CacheF16, catalog.CacheF16)
	require.False(t, ok, "no context makes a model fit a machine smaller than its weights")
}

// Ranking quants the owner has not downloaded needs bits per weight, and the real block sizes
// rather than the name. Q4 is not four bits.
func TestBitsPerWeightComeFromTheRealBlockSizes(t *testing.T) {
	for quant, want := range map[string]float64{
		"Q4_0": 4.5, "Q8_0": 8.5, "Q2_K": 2.625, "F16": 16, "BF16": 16, "F32": 32,
	} {
		got, ok := catalog.BitsPerWeight(quant)
		require.True(t, ok, quant)
		require.InDelta(t, want, got, 0.001, quant)
	}
	_, ok := catalog.BitsPerWeight("not-a-quant")
	require.False(t, ok, "an unknown quant must not be given a made-up size")
}
