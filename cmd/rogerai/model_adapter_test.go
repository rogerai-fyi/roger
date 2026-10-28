package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/detect/gguftest"
	"rogerai.fm/roger/v7/internal/localmodel"
)

// edge_device_runtime.feature §2: `roger model serve <base> --adapter <name|path>`. The adapter is
// taken by name from the files the owner copied in or by path, refused when it is not an adapter
// or was trained for another model family, and the detached child is handed the same file.
func TestServeAdapterIsResolvedAndCheckedAgainstItsBase(t *testing.T) {
	useTempConfig(t)
	h := modelHome()
	dir := filepath.Join(h.Models(), "wave")
	base := filepath.Join(dir, "wave-nano-1p45b-Q4_K_M.gguf")
	gguftest.WriteStrings(t, base, map[string]string{"general.architecture": "llama", "general.type": "model"})
	lora := filepath.Join(dir, "fire-decision-adapter-f16.gguf")
	gguftest.WriteStrings(t, lora, map[string]string{"general.architecture": "llama", "general.type": "adapter", "adapter.type": "lora"})
	foreign := filepath.Join(dir, "qwen-tune-f16.gguf")
	gguftest.WriteStrings(t, foreign, map[string]string{"general.architecture": "qwen3", "general.type": "adapter", "adapter.type": "lora"})
	m, err := localmodel.FromPath(base)
	require.NoError(t, err)

	a, err := parseModelArgv([]string{"wave-nano-1p45b", "--adapter", "fire-decision-adapter", "--detach"})
	require.NoError(t, err)
	require.Equal(t, "fire-decision-adapter", a.adapter)
	_, err = parseModelArgv([]string{"x", "--adapter", ""})
	require.Error(t, err)

	none, err := modelAdapterFor(h, m, "")
	require.NoError(t, err)
	require.Nil(t, none)
	byName, err := modelAdapterFor(h, m, "fire-decision-adapter")
	require.NoError(t, err)
	require.Equal(t, lora, byName.Path)
	byPath, err := modelAdapterFor(h, m, lora)
	require.NoError(t, err)
	require.Equal(t, lora, byPath.Path)

	_, err = modelAdapterFor(h, m, "no-such-adapter")
	require.Error(t, err)
	require.Contains(t, err.Error(), `no adapter "no-such-adapter" on this machine`)
	_, err = modelAdapterFor(h, m, base)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not a LoRA adapter")
	_, err = modelAdapterFor(h, m, "qwen-tune")
	require.Error(t, err)
	require.Contains(t, err.Error(), "was trained for a qwen3 model")
	require.Contains(t, err.Error(), "is llama")
}
