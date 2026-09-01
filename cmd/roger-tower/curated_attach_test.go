package main

// features/curated/curated_tower.feature, operator side: attaching a curated station is
// one flag, the label rides listing and routing receipts, and everything stays free.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttachCuratedStationFlowsThroughListingAndRouting(t *testing.T) {
	dir := bootstrappedDir(t)

	out, err := runCLI(t, "attach", "--dir", dir, "--station", "st-or", "--key", "sk1", "--models", "gpt-4o", "--curated", "openrouter")
	require.NoError(t, err)
	require.Contains(t, out, "curated via openrouter")
	require.Contains(t, out, "free", "the standalone plane never bills, curated included")

	out, err = runCLI(t, "stations", "--dir", dir)
	require.NoError(t, err)
	require.Contains(t, out, "st-or")
	require.Contains(t, out, "curated via openrouter", "the listing must not blur a proxy into local hardware")

	out, err = runCLI(t, "route", "--dir", dir, "--client", "alice", "--model", "gpt-4o")
	require.NoError(t, err)
	require.Contains(t, out, "curated via openrouter")
	require.Contains(t, out, "free")
	require.NotContains(t, strings.ToLower(out), "rogerai")
}

func TestAttachCuratedRequiresAProvider(t *testing.T) {
	dir := bootstrappedDir(t)
	_, err := runCLI(t, "attach", "--dir", dir, "--station", "st-or", "--key", "sk1", "--models", "m", "--curated", "  ")
	require.Error(t, err, "an unnamed proxy is refused")
}
