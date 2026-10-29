package localmodel

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/detect/gguftest"
)

// edge_device_runtime.feature §2 "an adapter file is not offered as a model" / "a projector file is
// not offered as a model": the header's own word decides, not the file name. Found 2026-10-24 when
// the Wave fire-decision adapter, copied in beside its base, was listed and then served as a model.
func TestAnAdapterOrProjectorByHeaderIsNotAModelRow(t *testing.T) {
	h := Home{Dir: t.TempDir()}
	base := filepath.Join(h.Models(), "wave", "wave-nano-1p45b-Q4_K_M.gguf")
	gguftest.WriteStrings(t, base, map[string]string{"general.architecture": "llama", "general.type": "model"})
	adapter := filepath.Join(h.Models(), "wave", "fire-decision-adapter-f16.gguf")
	gguftest.WriteStrings(t, adapter, map[string]string{"general.architecture": "llama", "general.type": "adapter",
		"adapter.type": "lora", "general.base_model.0.name": "Nano Sft Instruct V3D"})
	proj := filepath.Join(h.Models(), "wave", "nano-vl-v7cropcpen-proj-f16.gguf") // not named mmproj-*
	gguftest.WriteStrings(t, proj, map[string]string{"general.architecture": "clip", "general.type": "mmproj"})
	unsaid := filepath.Join(h.Models(), "wave", "old-style-Q8_0.gguf") // a header with no general.type
	gguftest.WriteStrings(t, unsaid, map[string]string{"general.architecture": "llama"})

	all, err := h.List()
	require.NoError(t, err)
	ids := []string{}
	for _, m := range all {
		ids = append(ids, m.ID)
	}
	require.ElementsMatch(t, []string{"wave-nano-1p45b-Q4_K_M", "old-style-Q8_0"}, ids, "only models are rows")

	for _, c := range []struct{ path, says, notSays string }{
		{adapter, "a LoRA adapter for Nano Sft Instruct V3D", ""},
		{proj, "a vision projector", ""},
	} {
		_, err := FromPath(c.path)
		require.Error(t, err, c.path)
		require.Contains(t, err.Error(), c.says)
		require.Contains(t, err.Error(), filepath.Base(c.path), "the refusal names the file")
	}
	_, err = FromPath(base)
	require.NoError(t, err)
	_, err = FromPath(unsaid)
	require.NoError(t, err, "a header that does not say is a model on the owner's word, as before")
	require.Contains(t, UnservableAt(filepath.Join(h.Models(), "mmproj-x.gguf")), "projector", "the name still decides when it is plain")
}
