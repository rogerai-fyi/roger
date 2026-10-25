package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/localmodel"
)

func ggufSaying(t *testing.T, path string, kv map[string]string) {
	t.Helper()
	b := []byte("GGUF")
	b = binary.LittleEndian.AppendUint32(b, 3)
	b = binary.LittleEndian.AppendUint64(b, 0)
	b = binary.LittleEndian.AppendUint64(b, uint64(len(kv)))
	str := func(s string) {
		b = binary.LittleEndian.AppendUint64(b, uint64(len(s)))
		b = append(b, s...)
	}
	for k, v := range kv {
		str(k)
		b = binary.LittleEndian.AppendUint32(b, 8)
		str(v)
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, b, 0o600))
}

// edge_device_runtime.feature §2: `roger model serve <base> --adapter <name|path>`. The adapter is
// taken by name from the files the owner copied in or by path, refused when it is not an adapter
// or was trained for another model family, and the detached child is handed the same file.
func TestServeAdapterIsResolvedAndCheckedAgainstItsBase(t *testing.T) {
	useTempConfig(t)
	h := modelHome()
	dir := filepath.Join(h.Models(), "wave")
	base := filepath.Join(dir, "wave-nano-1p45b-Q4_K_M.gguf")
	ggufSaying(t, base, map[string]string{"general.architecture": "llama", "general.type": "model"})
	lora := filepath.Join(dir, "fire-decision-adapter-f16.gguf")
	ggufSaying(t, lora, map[string]string{"general.architecture": "llama", "general.type": "adapter"})
	foreign := filepath.Join(dir, "qwen-tune-f16.gguf")
	ggufSaying(t, foreign, map[string]string{"general.architecture": "qwen3", "general.type": "adapter"})
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
