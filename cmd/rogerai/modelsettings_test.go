package main

// local_models.feature §11 (approved 2026-10-02): the serving settings are one function behind every
// surface, and what serves is measured.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/localmodel"
)

func TestModelSettingsAreOneFunctionForEverySurface(t *testing.T) {
	r := newModelRig(t, 16384)
	h := modelWebHooks(config{})
	v, err := h.View()
	require.NoError(t, err)
	require.Equal(t, "auto", v.Settings.Runtime)
	require.Equal(t, "off", v.Settings.Idle)
	require.Zero(t, v.Settings.DiskBudget)
	require.Equal(t, []string{"auto", "cpu", "gpu", "vulkan", "cuda"}, v.Settings.Choices)
	require.NotEmpty(t, v.Settings.Here, "what the choice means on THIS machine")

	says, err := h.Set("runtime", "cpu")
	require.NoError(t, err)
	require.Contains(t, says, "the cpu build")
	require.Contains(t, says, "applies from the next serve")
	says, err = h.Set("idle", "2h")
	require.NoError(t, err)
	require.Contains(t, says, "idle timeout: 2h0m0s")
	says, err = h.Set("budget", "20G")
	require.NoError(t, err)
	require.Contains(t, says, "budget for models: 20.0 GiB")
	v, _ = h.View()
	require.Equal(t, "cpu", v.Settings.Runtime)
	require.Equal(t, "2h0m0s", v.Settings.Idle)
	require.Equal(t, int64(20)<<30, v.Settings.DiskBudget)

	_, err = h.Set("speed", "fast")
	require.ErrorContains(t, err, "runtime, idle or budget")
	_, err = h.Set("idle", "soon")
	require.Error(t, err)

	// Lowering the budget below what is on disk removes nothing; it says what the next fetch would.
	p, err := r.home.PathFor("acme/Big-GGUF", "big.gguf")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, make([]byte, 3<<20), 0o644))
	rec, _ := json.Marshal(localmodel.Model{ID: "acme/Big-GGUF:Q4_K_M", Repo: "acme/Big-GGUF", File: "big.gguf", Bytes: 3 << 20, SHA256: "ab", Band: "big"})
	require.NoError(t, os.WriteFile(p+".roger.json", rec, 0o644))
	says, err = h.Set("budget", "1M")
	require.NoError(t, err)
	require.Contains(t, says, "nothing is removed now")
	require.Contains(t, says, "acme/Big-GGUF:Q4_K_M")
	require.Contains(t, says, "at the next fetch")
	_, err = r.home.Find("acme/Big-GGUF:Q4_K_M")
	require.NoError(t, err, "still on disk")
}

func TestModelAServedModelShowsItsMeasuredSpeedAndBuild(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	var view localmodelView
	modelUntilDone = func(context.Context) {
		v, err := modelWebView()
		require.NoError(t, err)
		require.NotNil(t, v.Serving)
		view = localmodelView{build: v.Serving.Build, measured: v.Serving.Measured}
		st, _ := r.run(t, "status")
		require.Contains(t, st, "speed    50.0 tok/s measured", "status says the measurement")
		now := edgeModelNow()
		require.Equal(t, "cpu", now.Build)
		require.InDelta(t, 50, now.TokPerSec, 0.5, "the member's card carries the measurement")
	}
	_, err = r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.Equal(t, "cpu", view.build)
	require.InDelta(t, 50, view.measured, 0.5, "5 tokens in 0.1 s, the runtime's own count")
	_ = localmodel.Settings{}
}

type localmodelView struct {
	build    string
	measured float64
}
