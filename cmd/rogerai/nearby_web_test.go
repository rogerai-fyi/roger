package main

// features/models/short_names_nearby.feature section 7: the TUI and the console read nearby
// suggestions from the same code as `roger model serve` with no name, and a pick from either is the
// same fetch: the exact file, against its source's digest, refused if the mapping moved.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/modelview"
	"rogerai.fm/roger/v7/internal/servedfile"
)

func TestTheScreensSuggestTheSameNearbyBandsAsTheTerminal(t *testing.T) {
	r := newNearbyRig(t, 16384)
	fits := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	huge := r.hubFileOf("big/Qwen3-8B-Huge-GGUF", "Qwen3-8B-Huge-Q8_0.gguf", "Q8_0")
	r.memberBands(t, "atlas", true, []string{"qwen3-8b", "gpt-oss-20b"}, []*servedfile.File{&fits, &huge})

	nv, err := modelWebHooks(r.cfg()).Nearby(context.Background())
	require.NoError(t, err)
	require.Equal(t, "system memory", nv.Pool)
	require.Len(t, nv.Fits, 1)
	got := nv.Fits[0]
	require.Equal(t, modelview.NearbyRow{Name: "qwen3-8b", Repo: "Qwen/Qwen3-8B-GGUF", File: qwenQ4, Quant: "Q4_K_M", SHA256: fits.SHA256,
		Bytes: got.Bytes, By: "your Edge member atlas", Fit: "fits", Short: got.Short, Line: got.Line,
		Ask: "qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas; fetch 4.3 KiB?"}, got)
	require.Contains(t, got.Short, "fits")
	require.Len(t, nv.Other, 1)
	require.Equal(t, "gpt-oss-20b", nv.Other[0].Name)
	require.Contains(t, nv.Other[0].Why, "would not fit here")

	// The terminal says the same, from the same code.
	out, err := r.do(t, "n\n", "serve")
	require.NoError(t, err)
	require.Contains(t, out, strings.TrimSuffix(got.Ask, "; fetch 4.3 KiB?"))
}

func TestAPickFromAScreenFetchesExactlyThatFileOrNothing(t *testing.T) {
	r := newNearbyRig(t, 16384)
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	r.member(t, "atlas", "qwen3-8b", &f, true)
	hooks := modelWebHooks(r.cfg())
	pick := modelview.NearbyPick{Name: "qwen3-8b", Repo: f.Repo, File: f.File, SHA256: f.SHA256}

	// A pick the name no longer maps to: refused, and asked again by showing the list again.
	other := pick
	other.SHA256 = strings.Repeat("0f", 32)
	_, err := hooks.FetchNearby(other)
	require.ErrorContains(t, err, "qwen3-8b no longer maps to that file; look again before saying yes")
	_, err = hooks.FetchNearby(modelview.NearbyPick{Name: "nowhere", Repo: f.Repo, File: f.File, SHA256: f.SHA256})
	require.ErrorContains(t, err, "nowhere is not served nearby")
	_, ok := r.home.Job()
	require.False(t, ok, "nothing was fetched")

	says, err := hooks.FetchNearby(pick)
	require.NoError(t, err)
	require.Contains(t, says, "fetching "+qwenQ4)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := r.home.Job(); ok && j.State != "fetching" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	m, err := r.home.Find("Qwen/Qwen3-8B-GGUF:Q4_K_M")
	require.NoError(t, err)
	require.Equal(t, f.SHA256, m.SHA256)
}

func TestAPickWhoseSourceDigestTheHubDoesNotListIsRefused(t *testing.T) {
	r := newNearbyRig(t, 16384)
	f := r.hubFileOf("Qwen/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
	f.SHA256 = strings.Repeat("0f", 32)
	r.member(t, "atlas", "qwen3-8b", &f, true)
	_, err := modelWebHooks(r.cfg()).FetchNearby(modelview.NearbyPick{Name: "qwen3-8b", Repo: f.Repo, File: f.File, SHA256: f.SHA256})
	require.ErrorContains(t, err, "your Edge member atlas reported sha256")
	require.ErrorContains(t, err, "never served")
	_, ok := r.home.Job()
	require.False(t, ok)
}
