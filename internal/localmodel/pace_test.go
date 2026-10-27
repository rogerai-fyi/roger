package localmodel

// FITTING AND BEING USABLE ARE TWO DIFFERENT CLAIMS (local_models.feature §4). A model that fits
// a board with slow memory still answers slowly: writing one token reads every weight once, so
// the speed is the memory's bandwidth over the bytes read per token. Roger says both, separately.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/catalog"
	"rogerai.fm/roger/v7/internal/detect"
	"rogerai.fm/roger/v7/internal/runtime"
)

const gbps = 1e9

func measured(bps float64) func(int) Bandwidth {
	return func(int) Bandwidth { return Bandwidth{BytesPerSec: bps, Measured: true, From: "measured"} }
}

func TestTheMemorySpeedComesFromTheSpecSheetOrAMeasurementAndSaysWhich(t *testing.T) {
	one := detect.LocalHW{VRAMKnown: true, VRAMTotalMiB: 24564, GPUs: []detect.LocalGPU{{Model: "NVIDIA GeForce RTX 4090", VRAMMiB: 24564}}}
	bw := bandwidthOf(one, measured(20*gbps))
	require.InDelta(t, 1008*gbps, bw.BytesPerSec, 1)
	require.False(t, bw.Measured)
	require.Contains(t, bw.From, "spec sheet")
	require.Contains(t, bw.From, "RTX 4090")

	// The longest name wins, so a SUPER is not read as the card it is a refresh of.
	require.InDelta(t, 736*gbps, bandwidthOf(detect.LocalHW{VRAMKnown: true, VRAMTotalMiB: 16376,
		GPUs: []detect.LocalGPU{{Model: "NVIDIA GeForce RTX 4080 SUPER", VRAMMiB: 16376}}}, measured(1)).BytesPerSec, 1)

	// Layers are split across cards by their memory, and a token passes through every card in
	// turn, so the time per token is the sum of each card's share over its own speed.
	mixed := detect.LocalHW{VRAMKnown: true, VRAMTotalMiB: 3*32623 + 97887, GPUs: []detect.LocalGPU{
		{Model: "NVIDIA RTX PRO 4500 Blackwell", VRAMMiB: 32623}, {Model: "NVIDIA RTX PRO 4500 Blackwell", VRAMMiB: 32623},
		{Model: "NVIDIA RTX PRO 4500 Blackwell", VRAMMiB: 32623}, {Model: "NVIDIA RTX PRO 6000 Blackwell Max-Q Workstation Edition", VRAMMiB: 97887}}}
	total := float64(3*32623 + 97887)
	want := 1 / (3*32623/total/(896*gbps) + 97887/total/(1792*gbps))
	require.InDelta(t, want, bandwidthOf(mixed, measured(1)).BytesPerSec, 1e6)

	// A card roger has no figure for makes the whole pool unknown: a guess is not a speed.
	odd := detect.LocalHW{VRAMKnown: true, VRAMTotalMiB: 16000, GPUs: []detect.LocalGPU{{Model: "Some Future Card", VRAMMiB: 16000}}}
	bw = bandwidthOf(odd, measured(1))
	require.Zero(t, bw.BytesPerSec)
	require.Contains(t, bw.Why, "Some Future Card")

	// System memory is measured on this machine, because a board's memory is what it is.
	pi := detect.LocalHW{RAMKnown: true, RAMTotalMiB: 4096}
	bw = bandwidthOf(pi, measured(4*gbps))
	require.True(t, bw.Measured)
	require.InDelta(t, 4*gbps, bw.BytesPerSec, 1)

	// One pool: a known chip reads from its sheet; an unknown one is measured and says the GPU
	// may read faster than the processor that measured it.
	mac := detect.LocalHW{UnifiedMemory: true, RAMKnown: true, RAMTotalMiB: 32768,
		GPUs: []detect.LocalGPU{{Model: "Apple M2 Max (integrated, unified memory)"}}}
	require.InDelta(t, 400*gbps, bandwidthOf(mac, measured(1)).BytesPerSec, 1)
	mac.GPUs[0].Model = "Apple M9 (integrated, unified memory)"
	bw = bandwidthOf(mac, measured(90*gbps))
	require.True(t, bw.Measured)
	require.Contains(t, bw.From, "may read faster")

	// No pool, no speed.
	require.Zero(t, bandwidthOf(detect.LocalHW{}, measured(9*gbps)).BytesPerSec)
}

func TestTheMeasurementIsReal(t *testing.T) {
	bw := MeasureRAM(4096)
	require.True(t, bw.Measured)
	require.Greater(t, bw.BytesPerSec, 1e8, "any machine that runs the tests reads faster than 100 MB/s")
	require.Contains(t, bw.From, "measured")
	require.Equal(t, bw, MeasureRAM(4096), "measured once per process: the monitor must not become the load")
}

// A buffer that fits in the processor's cache measures the cache. A server part carries hundreds of
// megabytes of it (the first live run on one read "1681 GB/s"), so the buffer is sized against the
// machine: an eighth of its memory, never under 64 MiB, never over 1 GiB.
func TestTheMeasurementIsLargerThanAnyCache(t *testing.T) {
	const mib = 1 << 20
	for _, c := range []struct{ ram, want int }{
		{0, 64 * mib}, {512, 64 * mib}, {1024, 128 * mib}, {4096, 512 * mib}, {8192, 1024 * mib}, {131072, 1024 * mib},
	} {
		require.Equal(t, c.want, measureSize(c.ram), "%d MiB of memory", c.ram)
	}
}

func TestFittingAndBeingUsableAreTwoDifferentClaims(t *testing.T) {
	// An 8 GiB Pi reading about 4 GB/s fits a 2.5 GB model - and answers it slowly.
	pi := Budget{Bytes: 8 * gib, Pool: "system memory", BW: Bandwidth{BytesPerSec: 4 * gbps, Measured: true, From: "measured 4.0 GB/s"}}
	v := Judge(qwen(2500<<20), pi, 2048)
	require.Equal(t, catalog.FitFits, v.Fit, "it fits")
	require.Less(t, v.Pace.TokPerSec, 3.0)
	require.Equal(t, "too slow to chat", v.Pace.Word, "and separately, it is not usable")
	require.NotContains(t, v.Short(), "too slow", "the fit verdict is not the speed")
	require.Contains(t, v.PaceLine(), "too slow to chat")
	require.Contains(t, v.PaceLine(), "per token", "the working is stated")

	// The same model on a fast card is both.
	gpu := Budget{Bytes: 24 * gib, Pool: "video memory", BW: Bandwidth{BytesPerSec: 1008 * gbps, From: "spec sheet"}}
	v = Judge(qwen(2500<<20), gpu, 8192)
	require.Equal(t, "usable", v.Pace.Word)
	require.Greater(t, v.Pace.TokPerSec, 100.0)

	// In between is slow, and said so.
	mid := Budget{Bytes: 16 * gib, Pool: "system memory", BW: Bandwidth{BytesPerSec: 15 * gbps, Measured: true}}
	require.Equal(t, "slow", Judge(qwen(2500<<20), mid, 2048).Pace.Word)

	// A mixture of experts reads part of its weights per token, so all roger can promise is a floor.
	moe := qwen(2500 << 20)
	moe.Experts, moe.ExpertsUsed = 128, 8
	p := Judge(moe, pi, 2048).Pace
	require.True(t, p.AtLeast)
	require.Contains(t, Judge(moe, pi, 2048).PaceLine(), "at least")

	// No bandwidth, no speed - and the fit still stands on its own.
	noBW := Budget{Bytes: 8 * gib, Pool: "system memory"}
	v = Judge(qwen(2500<<20), noBW, 2048)
	require.Equal(t, catalog.FitFits, v.Fit)
	require.Zero(t, v.Pace.TokPerSec)
	require.Contains(t, v.PaceLine(), "speed: cannot say")

	// A model that will not fit has no speed here to speak of.
	require.Empty(t, Judge(qwen(16*gib), pi, 2048).PaceLine())
}

// THE POOL IS THE ONE THE RUNTIME WILL USE. A machine with four GPUs and a CPU-only runtime serves
// from system memory at system memory's speed; judging it against the cards would promise a fit
// and a speed the runtime it actually starts will not deliver.
func TestTheBudgetIsThePoolTheRuntimeWillUse(t *testing.T) {
	hw := detect.LocalHW{VRAMKnown: true, VRAMTotalMiB: 24564, RAMKnown: true, RAMTotalMiB: 65536,
		GPUs: []detect.LocalGPU{{Model: "NVIDIA GeForce RTX 4090", VRAMMiB: 24564}}}
	b := budgetFor(hw, false, measured(30*gbps))
	require.Equal(t, "system memory", b.Pool)
	require.Equal(t, 64*gib, b.Bytes)
	require.True(t, b.BW.Measured, "and read at system memory's speed, not the card's")
	require.InDelta(t, 30*gbps, b.BW.BytesPerSec, 1)

	b = budgetFor(hw, true, measured(30*gbps))
	require.Equal(t, "video memory", b.Pool, "a runtime that offloads lives on the card")
	require.InDelta(t, 1008*gbps, b.BW.BytesPerSec, 1)

	mac := detect.LocalHW{UnifiedMemory: true, RAMKnown: true, RAMTotalMiB: 16384}
	require.Equal(t, "unified memory", budgetFor(mac, false, measured(1)).Pool, "one pool is one pool either way")

	var blind detect.LocalHW
	blind.Note("could not read /proc/meminfo")
	require.Contains(t, budgetFor(blind, false, measured(1)).Why, "/proc/meminfo")
}

// THE RUNTIME'S OWN FITNESS ANSWER WINS ONCE THE FILE IS ON DISK (local_models.feature §4): its yes
// or no is taken, with its own breakdown, and roger's estimate stands only when it could not answer.
func TestTheRuntimesAnswerWinsOnceTheFileIsOnDisk(t *testing.T) {
	pi := Budget{Bytes: 8 * gib, Pool: "system memory", BW: Bandwidth{BytesPerSec: 4 * gbps, Measured: true, From: "measured"}}
	est := Judge(qwen(2500<<20), pi, 8192)
	require.Equal(t, catalog.FitFits, est.Fit)

	// The runtime says no where roger said yes (it judges what is free NOW): the runtime wins.
	no := est.Settled(runtime.Fitted{Ctx: 8192, Said: "was unable to fit model into system memory by reducing context", MaxCtx: 4096,
		Devices: []runtime.DeviceUse{{Name: "Host", Model: 2400 << 20, Context: 1152 << 20, Compute: 300 << 20}}}, nil)
	require.Equal(t, catalog.FitWontFit, no.Fit)
	require.True(t, no.ByRuntime)
	require.Equal(t, 4096, no.MaxCtx)
	require.Contains(t, no.Line(), "will not fit at 8K context by llama.cpp's own check")
	require.Contains(t, no.Line(), "was unable to fit model into system memory")
	require.Contains(t, no.Line(), "fits at 4K")
	require.Equal(t, "model 2.3 GiB + context 1.1 GiB + compute 300.0 MiB + margin 1.0 GiB = 4.8 GiB", no.Sum(),
		"the sum is the runtime's own terms")
	require.Empty(t, no.PaceLine())

	yes := est.Settled(runtime.Fitted{Fits: true, Ctx: 8192,
		Devices: []runtime.DeviceUse{{Name: "Host", Model: 2400 << 20, Context: 1152 << 20, Compute: 300 << 20}}}, nil)
	require.Equal(t, catalog.FitFits, yes.Fit)
	require.Contains(t, yes.Line(), "fits at 8K context by llama.cpp's own check")
	require.Equal(t, est.Pace, yes.Pace, "the speed is still roger's claim, beside the fit")
	require.NotEmpty(t, yes.PaceLine())

	none := est.Settled(runtime.Fitted{Ctx: 8192, Said: "out of memory", Devices: []runtime.DeviceUse{{Name: "Host", Model: 9 << 30}}}, nil)
	require.Contains(t, none.Line(), "at no context the runtime would choose")

	// It could not answer: the estimate stands, and says it is one.
	kept := est.Settled(runtime.Fitted{}, errors.New("the runtime's fit check did not answer: exit status 1: invalid argument: -fitp"))
	require.Equal(t, est.Fit, kept.Fit)
	require.False(t, kept.ByRuntime)
	require.Contains(t, kept.Line(), "roger's estimate - the runtime's own check could not run")
}

// Found live 2026-10-02: on a fast card a small model is bound by the work every layer does for every
// token, not by reading its weights - the estimate said ~2108 tok/s for Qwen3-0.6B where the runtime
// measured 62 (busy cards) and llama-bench on an idle RTX PRO 6000 gave 538. The per-layer cost is in
// the sum, so a small model on a fast card is estimated near what such a card really does.
func TestASmallModelOnAFastCardIsBoundByItsLayers(t *testing.T) {
	small := catalog.Shape{FileBytes: 397_000_000, Layers: 28, KVHeads: 8, KeyDim: 128, ValueDim: 128, TrainCtx: 40960, Vocab: 151936}
	card := Budget{Bytes: 96 * gib, Pool: "video memory", BW: Bandwidth{BytesPerSec: 1792 * gbps, From: "spec sheet"}}
	p := Judge(small, card, 8192).Pace
	require.InDelta(t, 538, p.TokPerSec, 120, "near what llama-bench measured on that card")

	// A large model on the same card is still bound by reading its weights.
	big := small
	big.FileBytes, big.Layers = 40*gib, 64
	require.InDelta(t, 0.7*1792e9/float64(40*gib), Judge(big, card, 2048).Pace.TokPerSec, 6)

	// The CPU calibration is untouched: measured bandwidth already includes the per-layer work.
	cpu := Budget{Bytes: 64 * gib, Pool: "system memory", BW: Bandwidth{BytesPerSec: 159.7 * gbps, Measured: true}}
	require.InDelta(t, 241, Judge(small, cpu, 8192).Pace.TokPerSec, 3)
}
