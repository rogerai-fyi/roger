package localmodel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A models directory that is a symlink (a shared cache linked in) is listed like a real one:
// roger model list and status see what roger model serve already serves from it.
func TestListFollowsASymlinkedModelsDirectory(t *testing.T) {
	real := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(real, "SmolLM2-135M-Instruct-Q4_K_M.gguf"), []byte("gguf"), 0o644))
	h := Home{Dir: t.TempDir()}
	require.NoError(t, os.MkdirAll(filepath.Dir(h.Models()), 0o755))
	require.NoError(t, os.Symlink(real, h.Models()))

	got, err := h.List()
	require.NoError(t, err)
	require.Len(t, got, 1, "the linked directory's model is listed")
	require.Equal(t, "SmolLM2-135M-Instruct-Q4_K_M", got[0].ID)
}
