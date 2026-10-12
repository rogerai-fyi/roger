package tower

// features/models/short_names_nearby.feature "a station on a pinned Tower reports what it serves, as
// its own word": the Tower keeps a Station's file report only for a model it routes to that Station,
// and only one that names exactly one file; a later report replaces it.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/servedfile"
)

func TestATowerKeepsOnlyACleanReportForAModelItRoutes(t *testing.T) {
	st := bootstrappedTower(t)
	_, err := st.AttachStation("st-1", "kh-1", []string{"qwen3-8b"})
	require.NoError(t, err)
	good := servedfile.File{Repo: "Qwen/Qwen3-8B-GGUF", File: "Qwen3-8B-Q4_K_M.gguf", Quant: "Q4_K_M", SHA256: strings.Repeat("ab", 32)}

	got, err := st.SetStationFiles("st-1", map[string]servedfile.File{
		"qwen3-8b":  good,
		"not-mine":  good,                                            // a model the Tower does not route to it
		"qwen3-8b2": {Repo: "x", File: "../y.gguf", SHA256: "short"}, // and nothing malformed
	})
	require.NoError(t, err)
	require.Equal(t, map[string]servedfile.File{"qwen3-8b": good}, got.Files)

	sts, err := st.Stations()
	require.NoError(t, err)
	require.Equal(t, good, sts[0].Files["qwen3-8b"], "it is kept")

	again, err := st.SetStationFiles("st-1", map[string]servedfile.File{"qwen3-8b": good})
	require.NoError(t, err)
	require.Equal(t, got.Files, again.Files, "the same report changes nothing")

	bad := good
	bad.SHA256 = "nope"
	cleared, err := st.SetStationFiles("st-1", map[string]servedfile.File{"qwen3-8b": bad})
	require.NoError(t, err)
	require.Nil(t, cleared.Files, "a report that names no file replaces the last one with nothing")

	_, err = st.SetStationFiles("st-none", nil)
	require.ErrorContains(t, err, "no attached Station")
}
