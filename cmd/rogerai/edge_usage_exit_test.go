package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A usage mistake exits 2 (exitCode: 0 success, 1 a real failure, 2 misuse), on invoke and grant as
// on every other edge leaf.
func TestEdgeInvokeAndGrantUsageMistakesExitTwo(t *testing.T) {
	newFacedAuthority(t, "desk")
	for _, args := range [][]string{
		{"edge", "invoke"},
		{"edge", "invoke", "desk"},
		{"edge", "invoke", "desk", "hi", "--cap"},
		{"edge", "invoke", "desk", "hi", "--model"},
		{"edge", "invoke", "desk", "hi", "--as"},
		{"edge", "grant", "self", "operate"},
		{"edge", "grant", "self", "operate", "desk", "--for"},
		{"edge", "grant", "self", "operate", "desk", "--hops"},
		{"edge", "grant", "self", "operate", "desk", "--hops", "-1"},
		{"edge", "grant", "revoke"},
	} {
		_, code := edgeRun(t, args...)
		require.Equal(t, 2, code, "%v", args)
	}
	// A real failure is still a failure, not misuse.
	_, code := edgeRun(t, "edge", "grant", "revoke", "grant_nope")
	require.Equal(t, 1, code)
	_, code = edgeRun(t, "edge", "invoke", "ghost", "hi")
	require.Equal(t, 1, code)
}
