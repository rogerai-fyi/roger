package localmodel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/detect/gguftest"
)

// edge_device_runtime.feature §2 "an adapter is part of the entry and served with its base": the
// Jetson decision model is Wave Nano plus the fire-decision LoRA (`llama-server --lora`). The
// adapter is found by name among the files the owner copied in, refused when the header does not
// say it is one, and the serve answers under "<base>+<adapter>" so nobody mistakes it for the base.
func TestAnAdapterIsServedBesideItsBaseUnderACombinedBand(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	dir := filepath.Join(h.Models(), "wave")
	base := filepath.Join(dir, "Tiny-Q4_K_M.gguf")
	gguftest.WriteStrings(t, base, map[string]string{"general.architecture": "llama", "general.type": "model"})
	lora := filepath.Join(dir, "fire-decision-adapter-f16.gguf")
	gguftest.WriteStrings(t, lora, map[string]string{"general.architecture": "llama", "general.type": "adapter",
		"adapter.type": "lora", "general.base_model.0.name": "Nano Sft Instruct V3D"})
	proj := filepath.Join(dir, "mmproj-f16.gguf")
	gguftest.WriteStrings(t, proj, map[string]string{"general.architecture": "clip", "general.type": "mmproj"})

	a, err := AdapterAt(lora)
	require.NoError(t, err)
	require.Equal(t, "fire-decision-adapter", a.Name())
	require.Equal(t, "llama", a.Arch)
	require.Equal(t, "Nano Sft Instruct V3D", a.Base)
	cvec := filepath.Join(dir, "calm-control-vector.gguf")
	gguftest.WriteStrings(t, cvec, map[string]string{"general.architecture": "llama", "general.type": "adapter", "adapter.type": "control_vector"})
	for _, p := range []string{base, proj, cvec} {
		_, err := AdapterAt(p)
		require.Error(t, err, p)
		require.Contains(t, err.Error(), "is not a LoRA adapter")
	}
	_, err = h.FindAdapter("calm-control-vector")
	require.ErrorIs(t, err, ErrUnknownAdapter, "a control vector is not found as a LoRA")
	_, err = AdapterAt(filepath.Join(dir, "absent.gguf"))
	require.Error(t, err)

	for _, q := range []string{"fire-decision-adapter-f16", "fire-decision-adapter", "FIRE-decision-adapter-f16.gguf"} {
		got, err := h.FindAdapter(q)
		require.NoError(t, err, q)
		require.Equal(t, lora, got.Path)
	}
	_, err = h.FindAdapter("Tiny-Q4_K_M")
	require.ErrorIs(t, err, ErrUnknownAdapter, "a model is not found as an adapter")
	gguftest.WriteStrings(t, filepath.Join(h.Models(), "other", "fire-decision-adapter-f16.gguf"),
		map[string]string{"general.architecture": "llama", "general.type": "adapter", "adapter.type": "lora"})
	_, err = h.FindAdapter("fire-decision-adapter")
	require.Error(t, err)
	require.Contains(t, err.Error(), "2 adapter files")

	m, err := FromPath(base)
	require.NoError(t, err)
	o := ServeOpts{Model: m, Adapter: &a, Ctx: 2048, Budget: Budget{Bytes: 2 * gib}}
	args := strings.Join(Args(o, 9, "/k"), " ")
	require.Contains(t, args, "--lora "+lora)
	require.Contains(t, args, "--alias tiny+fire-decision-adapter")
	require.Equal(t, "tiny+fire-decision-adapter", o.Band())
	require.Equal(t, "tiny", ServeOpts{Model: m}.Band(), "no adapter, the base's own band")

	o.Exe, o.Tag, o.ReadyWithin, o.Env = os.Args[0], "bX", 10*time.Second, map[string]string{"FAKE_LLAMA": "1"}
	srv, s, err := h.Serve(context.Background(), o)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	require.Equal(t, "tiny+fire-decision-adapter", s.Band)
	require.Equal(t, "fire-decision-adapter-f16.gguf", s.Adapter)
	live, ok := h.Live()
	require.True(t, ok)
	require.Equal(t, s.Adapter, live.Adapter, "the record says the adapter")
}
