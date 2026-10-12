package main

// features/models/short_names_nearby.feature "an Edge member reports the digest of the file it
// serves": a member's own report names the exact repository file and sha256 behind each band.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/servedfile"
	"rogerai.fm/roger/v6/internal/store"
)

func TestAMembersReportNamesTheFileAndDigestItServes(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	var file, sha, id string
	modelUntilDone = func(context.Context) {
		now := edgeModelNow()
		file, sha, id = now.File, now.SHA256, now.ID
	}
	_, err = r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.Equal(t, "acme/Tiny-GGUF:Q4_K_M", id)
	require.Equal(t, "Tiny-Q4_K_M.gguf", file)
	require.Len(t, sha, 64, "the digest the fetch verified")
}

// A station names the file behind each model roger serves itself, and nothing for a model it does
// not know the file of (short_names_nearby.feature "a station on a pinned Tower reports what it
// serves, as its own word").
func TestAStationKnowsTheFileOfEachModelRogerServes(t *testing.T) {
	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	var files map[string]servedfile.File
	modelUntilDone = func(context.Context) { files = stationFiles() }
	_, err = r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.Contains(t, files, "tiny")
	require.Equal(t, "acme/Tiny-GGUF", files["tiny"].Repo)
	require.Equal(t, "Tiny-Q4_K_M.gguf", files["tiny"].File)
	require.Len(t, files["tiny"].SHA256, 64)
}

// A member's record maps its latest band and the ones beside it to their files; a band it does not
// list, or a record without files, maps nothing.
func TestAMembersRecordMapsEachOfItsBandsToItsFile(t *testing.T) {
	const sha = "2e8040ceae78a1b2c3d4e5f60718293a4b5c6d7e8f9001122334455667788990"
	m := &store.EdgeModel{Band: "tiny", ID: "acme/Tiny-GGUF:Q4_K_M", Quant: "Q4_K_M", File: "Tiny-Q4_K_M.gguf", SHA256: sha,
		Also: []store.EdgeServed{{Band: "big", ID: "acme/Big-GGUF:Q8_0", Quant: "Q8_0", File: "Big-Q8_0.gguf", SHA256: sha},
			{Band: "old", ID: "acme/Old-GGUF:Q4_0"}}}
	require.Equal(t, "acme/Tiny-GGUF", edgeBandFile(m, "tiny").Repo)
	require.Equal(t, "Big-Q8_0.gguf", edgeBandFile(m, "big").File)
	require.Nil(t, edgeBandFile(m, "old"), "an entry without a file maps nothing")
	require.Nil(t, edgeBandFile(m, "none"))
	require.Nil(t, edgeBandFile(nil, "tiny"))
}

// The size a member reports reaches the dial with its file.
func TestAMembersRecordCarriesTheSizeOfItsFile(t *testing.T) {
	const sha = "2e8040ceae78a1b2c3d4e5f60718293a4b5c6d7e8f9001122334455667788990"
	m := &store.EdgeModel{Band: "tiny", ID: "acme/Tiny-GGUF:Q4_K_M", Quant: "Q4_K_M", File: "Tiny-Q4_K_M.gguf", SHA256: sha, Bytes: 4321,
		Also: []store.EdgeServed{{Band: "big", ID: "acme/Big-GGUF:Q8_0", Quant: "Q8_0", File: "Big-Q8_0.gguf", SHA256: sha, Bytes: 99}}}
	require.Equal(t, int64(4321), edgeBandFile(m, "tiny").Bytes)
	require.Equal(t, int64(99), edgeBandFile(m, "big").Bytes)

	r := newModelRig(t, 16384)
	_, err := r.run(t, "get", "acme/Tiny-GGUF:Q4_K_M", "--yes")
	require.NoError(t, err)
	var now *store.EdgeModel
	modelUntilDone = func(context.Context) { now = edgeModelNow() }
	_, err = r.run(t, "serve", "tiny")
	require.NoError(t, err)
	require.Equal(t, int64(len(r.files["Tiny-Q4_K_M.gguf"])), now.Bytes, "what this machine reports of its own serve")
}
