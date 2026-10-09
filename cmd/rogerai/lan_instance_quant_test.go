package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// TestInstanceQuantIsTheServeRecordsLabel pins where an Edge member's quant comes from: its own
// serve record, the latest band or one served beside it; nothing else is a label.
func TestInstanceQuantIsTheServeRecordsLabel(t *testing.T) {
	in := edge.Instance{Model: &store.EdgeModel{Band: "qwen3-8b", Quant: "Q8_0",
		Also: []store.EdgeServed{{Band: "gpt-oss-20b", Quant: "MXFP4"}}}}
	require.Equal(t, "Q8_0", instanceQuant(in, "qwen3-8b"))
	require.Equal(t, "MXFP4", instanceQuant(in, "gpt-oss-20b"))
	require.Empty(t, instanceQuant(in, "llama-3b"))
	require.Empty(t, instanceQuant(edge.Instance{}, "qwen3-8b"), "a share-only member carries no label")
}
