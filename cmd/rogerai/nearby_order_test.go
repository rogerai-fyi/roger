package main

// features/models/short_names_nearby.feature section 4, "the order roger resolves a name in": a
// file path, then a hub reference (the author's word), then a name on this disk, then a name
// served nearby (shown before fetching), then a hub lookup the owner picks from; and --hub skips
// every nearby source.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTheOrderANameIsResolvedIn(t *testing.T) {
	t.Run("a file path is served as that file", func(t *testing.T) {
		r := newNearbyRig(t, 16384)
		p := filepath.Join(t.TempDir(), "my-model.gguf")
		require.NoError(t, os.WriteFile(p, modelGGUF(4000), 0o644))
		out, err := r.do(t, "", "serve", p)
		require.NoError(t, err)
		require.Contains(t, out, `on the air locally: "my-model"`)
		require.Empty(t, r.hub.searched(), "a path never asks the hub")
	})

	t.Run("a hub reference is the author's word, even when the name is served nearby", func(t *testing.T) {
		r := newNearbyRig(t, 16384)
		f := r.hubFileOf("lookalike/Qwen3-8B-GGUF", "Qwen3-8B-Q4_K_M.gguf", "Q4_K_M")
		r.member(t, "atlas", "qwen3-8b", &f, true)
		out, err := r.do(t, "", "serve", "Qwen/Qwen3-8B-GGUF:Q4_K_M", "--yes")
		require.NoError(t, err)
		require.Contains(t, out, "fetched and verified: Qwen/Qwen3-8B-GGUF:Q4_K_M")
		require.NotContains(t, out, "as served by")
	})

	t.Run("a name on this disk is the file on disk", func(t *testing.T) {
		r := newNearbyRig(t, 16384)
		_, err := r.do(t, "", "get", "Qwen/Qwen3-8B-GGUF:Q4_K_M", "--yes")
		require.NoError(t, err)
		f := r.hubFileOf("lookalike/Qwen3-8B-GGUF", "Qwen3-8B-Q4_K_M.gguf", "Q4_K_M")
		r.member(t, "atlas", "qwen3-8b", &f, true)
		out, err := r.do(t, "", "serve", "qwen3-8b")
		require.NoError(t, err)
		require.Contains(t, out, `on the air locally: "qwen3-8b" - Qwen/Qwen3-8B-GGUF:Q4_K_M`)
		require.NotContains(t, out, "as served by", "a name on this disk is never mapped again")
		require.False(t, r.hub.searchedFor("qwen3-8b"))
	})

	t.Run("a name served nearby is its mapping, shown before anything is fetched", func(t *testing.T) {
		r := newNearbyRig(t, 16384)
		f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", "Qwen3-8B-Q4_K_M.gguf", "Q4_K_M")
		r.member(t, "atlas", "qwen3-8b", &f, true)
		out, err := r.do(t, "n\n", "serve", "qwen3-8b")
		require.NoError(t, err)
		require.Contains(t, out, "qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas; fetch 4.3 KiB? [y/N]")
		require.Contains(t, out, "not fetched. Nothing changed.")
		require.False(t, r.hub.downloaded("Qwen3-8B-Q4_K_M.gguf"))
		require.False(t, r.hub.searchedFor("qwen3-8b"), "a name served nearby is never searched for")

		out, err = r.do(t, "y\n", "serve", "qwen3-8b")
		require.NoError(t, err)
		require.Contains(t, out, "fetched and verified: Qwen/Qwen3-8B-GGUF:Q4_K_M")
		require.Contains(t, out, `on the air locally: "qwen3-8b"`)
	})

	t.Run("a name nowhere nearby is looked up on the hub", func(t *testing.T) {
		r := newNearbyRig(t, 16384)
		out, err := r.do(t, "n\n", "serve", "qwen3-8b")
		require.NoError(t, err)
		require.Contains(t, out, "qwen3-8b is not served nearby; looking it up on the model hub, because you asked to serve it")
		require.True(t, r.hub.searchedFor("qwen3-8b"))
		require.False(t, r.hub.downloaded("Qwen3-8B-Q4_K_M.gguf"))
	})

	t.Run("--hub skips every nearby source", func(t *testing.T) {
		r := newNearbyRig(t, 16384)
		f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", "Qwen3-8B-Q4_K_M.gguf", "Q4_K_M")
		r.member(t, "atlas", "qwen3-8b", &f, true)
		out, err := r.do(t, "n\n", "serve", "qwen3-8b", "--hub")
		require.NoError(t, err)
		require.NotContains(t, out, "as served by")
		require.Contains(t, out, "looking it up on the model hub")
		require.True(t, r.hub.searchedFor("qwen3-8b"))
	})
}
