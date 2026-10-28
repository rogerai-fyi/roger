package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The empty LAN dial's ways to get a band name the simple-join commands (founder ruling
// 2026-10-11). `roger lan add` / `roger edge adopt` keep working but are not the advice.
func TestLANEmptyHintNamesTheJoinCommands(t *testing.T) {
	text := lanEmptyText(nil)
	require.NotContains(t, text, "roger lan add")
	require.NotContains(t, text, "roger edge adopt")
	require.Contains(t, text, "roger edge invite")
	require.Contains(t, text, "roger join")
}
