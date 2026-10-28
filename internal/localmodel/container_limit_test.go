package localmodel

// Finding 5b, ruled 2026-10-27 (features/serving/local_models.feature, "under a container memory
// limit, the stricter of the two verdicts wins"): the runtime judges the memory it sees as free, which
// inside a container is the host's. ONLY when a container memory limit governs the budget is roger's
// own sum held against it too; everywhere else the runtime's answer wins.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/catalog"
	"rogerai.fm/roger/v7/internal/detect"
	"rogerai.fm/roger/v7/internal/runtime"
)


func estimateNeeding(b Budget, need int64) Verdict {
	v := Verdict{Budget: b, Ctx: 8192, Need: catalog.Need{Weights: need, Total: need}}
	v.Fit = catalog.AssessFit(b.Bytes, need)
	return v
}

func TestUnderAContainerLimitTheStricterVerdictWins(t *testing.T) {
	yes := runtime.Fitted{Fits: true, Ctx: 8192, Devices: []runtime.DeviceUse{{Name: "Host", Model: 2 << 30}}}
	no := runtime.Fitted{Ctx: 8192, Said: "was unable to fit model into system memory", MaxCtx: 2048,
		Devices: []runtime.DeviceUse{{Name: "Host", Model: 9 << 30}}}
	host := Budget{Bytes: 64 * gib, Pool: "system memory"}
	limited := func(n int64) Budget { return Budget{Bytes: n, Pool: "system memory", LimitBytes: n} }

	for name, c := range map[string]struct {
		b    Budget
		rt   runtime.Fitted
		fits bool
		says string
	}{
		"no limit, the runtime says yes":         {host, yes, true, ""},
		"no limit, the runtime says no":          {host, no, false, "was unable to fit"},
		"a limit above roger's sum, runtime yes": {limited(16 * gib), yes, true, ""},
		"a limit below roger's sum, runtime yes": {limited(4 * gib), yes, false, "memory limit of 4.0 GiB"},
		"a limit below roger's sum, runtime no":  {limited(4 * gib), no, false, "was unable to fit"},
	} {
		v := estimateNeeding(c.b, 6*gib).Settled(c.rt, nil)
		require.Equal(t, c.fits, v.Fit != catalog.FitWontFit, name)
		if c.says != "" {
			require.Contains(t, v.Line(), c.says, name)
		}
	}
}

// A limit governs system memory: a budget in a GPU's own memory carries none, so the runtime's answer
// stands there; system and unified memory carry the limit the machine reads.
func TestAContainerLimitGovernsSystemMemoryNotVideoMemory(t *testing.T) {
	hw := detect.LocalHW{RAMKnown: true, RAMTotalMiB: 4096, MemLimitMiB: 4096, VRAMKnown: true, VRAMTotalMiB: 24576,
		GPUs: []detect.LocalGPU{{Model: "card"}}}
	require.Zero(t, budgetFor(hw, true, func(int) Bandwidth { return Bandwidth{} }).LimitBytes, "video memory: no cgroup limit")
	require.Equal(t, 4*gib, budgetFor(hw, false, func(int) Bandwidth { return Bandwidth{} }).LimitBytes, "system memory: the limit")
	hw.UnifiedMemory = true
	require.Equal(t, 4*gib, budgetFor(hw, true, func(int) Bandwidth { return Bandwidth{} }).LimitBytes, "unified memory: the limit")
	hw.MemLimitMiB, hw.UnifiedMemory = 0, false
	require.Zero(t, budgetFor(hw, false, func(int) Bandwidth { return Bandwidth{} }).LimitBytes, "no limit read: none")
}
