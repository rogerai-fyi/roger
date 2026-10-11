package client

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The LAN refusal's ways to get a band name the simple-join commands (founder ruling 2026-10-11).
func TestLANRefusalNamesTheJoinCommands(t *testing.T) {
	for _, edgeOnly := range []bool{false, true} {
		text := lanNothingServes("m", edgeOnly)
		require.NotContains(t, text, "roger lan add")
		require.NotContains(t, text, "roger edge adopt")
		require.Contains(t, text, "roger edge invite")
		if !edgeOnly {
			require.Contains(t, text, "roger join")
		}
	}
}
