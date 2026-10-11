package edge

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
)

// features/models/short_names_nearby.feature "an Edge member reports the digest of the file it serves".
const reportSHA = "2e8040ceae78a1b2c3d4e5f60718293a4b5c6d7e8f9001122334455667788990"

func TestAMembersReportKeepsTheFileAndDigestItServes(t *testing.T) {
	got := CleanModel(&EdgeModel{Band: "qwen3-8b", ID: "Qwen/Qwen3-8B-GGUF:Q4_K_M", Quant: "Q4_K_M",
		File: "Qwen3-8B-Q4_K_M.gguf", SHA256: reportSHA,
		Also: []store.EdgeServed{{Band: "smol", ID: "a/b:Q8_0", Quant: "Q8_0", File: "b-Q8_0.gguf", SHA256: reportSHA}}})
	require.Equal(t, "Qwen3-8B-Q4_K_M.gguf", got.File)
	require.Equal(t, reportSHA, got.SHA256)
	require.Equal(t, "b-Q8_0.gguf", got.Also[0].File)
	require.Equal(t, reportSHA, got.Also[0].SHA256)
}

func TestAMembersMalformedFileReportNamesNoFile(t *testing.T) {
	got := CleanModel(&EdgeModel{Band: "qwen3-8b", ID: "Qwen/Qwen3-8B-GGUF:Q4_K_M",
		File: "../../etc/passwd.gguf", SHA256: strings.ToUpper(reportSHA),
		Also: []store.EdgeServed{{Band: "smol", ID: "a/b:Q8_0", File: "x.gguf", SHA256: "short"}}})
	require.Empty(t, got.File, "a file report that cannot name one file is dropped, never half-trusted")
	require.Empty(t, got.SHA256)
	require.Empty(t, got.Also[0].File)
	require.Empty(t, got.Also[0].SHA256)
	require.Equal(t, "qwen3-8b", got.Band, "the band itself still stands")
}
