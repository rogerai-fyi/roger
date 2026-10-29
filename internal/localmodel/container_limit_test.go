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

// Under a GPU the limit still holds the part of the model that stays in system memory: the runtime's
// own breakdown names it (its Host row), and that part is held to the limit (founder ruling
// 2026-10-27: whenever a limit is detected, the stricter verdict wins). Every pool a machine under a
// limit is judged in carries the limit; a machine with none carries none.
func TestUnderAGPUTheHostPartIsHeldToTheContainerLimit(t *testing.T) {
	hw := detect.LocalHW{RAMKnown: true, RAMTotalMiB: 4096, MemLimitMiB: 4096, VRAMKnown: true, VRAMTotalMiB: 24576,
		GPUs: []detect.LocalGPU{{Model: "card"}}}
	none := func(int) Bandwidth { return Bandwidth{} }
	for name, b := range map[string]Budget{"video": budgetFor(hw, true, none), "system": budgetFor(hw, false, none)} {
		require.Equal(t, 4*gib, b.LimitBytes, name)
	}
	hw.MemLimitMiB = 0
	require.Zero(t, budgetFor(hw, true, none).LimitBytes, "no limit read: none")

	video := Budget{Bytes: 24 * gib, Pool: "video memory", LimitBytes: 4 * gib}
	split := func(host int64) runtime.Fitted {
		return runtime.Fitted{Fits: true, Ctx: 8192, Devices: []runtime.DeviceUse{
			{Name: "CUDA0", Model: 10 << 30, Context: 2 << 30}, {Name: "Host", Model: host}}}
	}
	ok := estimateNeeding(video, 12*gib).Settled(split(1*gib), nil)
	require.NotEqual(t, catalog.FitWontFit, ok.Fit, "1 GiB in system memory is under the 4 GiB limit")
	over := estimateNeeding(video, 12*gib).Settled(split(6*gib), nil)
	require.Equal(t, catalog.FitWontFit, over.Fit, "6 GiB left in system memory is over the 4 GiB limit")
	require.Contains(t, over.Line(), "memory limit of 4.0 GiB")
}

// Re-review 2026-10-27: every row a runtime prints for memory in system RAM counts (pinned host
// buffers and mapped CPU buffers included), never a GPU's own row.
func TestEveryHostResidentRowCountsAgainstTheLimit(t *testing.T) {
	video := Budget{Bytes: 24 * gib, Pool: "video memory across 2 GPUs", LimitBytes: 4 * gib}
	for _, name := range []string{"Host", "CPU", "CPU_Mapped", "CUDA_Host", "CUDA0_Host"} {
		f := runtime.Fitted{Fits: true, Ctx: 8192, Devices: []runtime.DeviceUse{{Name: "CUDA0", Model: 20 << 30}, {Name: name, Model: 5 << 30}}}
		require.Equal(t, catalog.FitWontFit, estimateNeeding(video, 25*gib).Settled(f, nil).Fit, name)
	}
	f := runtime.Fitted{Fits: true, Ctx: 8192, Devices: []runtime.DeviceUse{{Name: "CUDA0", Model: 20 << 30}, {Name: "CUDA1", Model: 5 << 30}}}
	require.NotEqual(t, catalog.FitWontFit, estimateNeeding(video, 25*gib).Settled(f, nil).Fit, "GPU rows are not system memory")
}
