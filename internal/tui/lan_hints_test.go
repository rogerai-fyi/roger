package tui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The TUI's empty LAN dial names the simple-join commands (founder ruling 2026-10-11).
func TestLANEmptyViewNamesTheJoinCommands(t *testing.T) {
	text := stripANSI(model{}.lanEmptyView(100))
	require.NotContains(t, text, "roger lan add")
	require.NotContains(t, text, "roger edge adopt")
	require.Contains(t, text, "roger edge invite")
	require.Contains(t, text, "roger join")
}
