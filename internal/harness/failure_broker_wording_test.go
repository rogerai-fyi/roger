package harness

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEveryBrokerNoMatchFilterKeepsItsWords: each filter the broker names in a no_match
// ("no node offers M under F", tunnel.go), and its capability and confidential forms, keeps the
// broker's words; a plain "no node offers M" stays the no-station phrase.
func TestEveryBrokerNoMatchFilterKeepsItsWords(t *testing.T) {
	msgs := []string{"no node offers m with the tools capability", "no node offers m on a confidential node"}
	for _, f := range NoMatchFilterNames {
		msgs = append(msgs, "no node offers m under "+f)
	}
	for _, raw := range msgs {
		require.NotContains(t, ShortFailure(raw, "m"), NoStationServing("m"), raw)
	}
	require.Contains(t, NoMatchFilterNames, "min_tps")
	require.Equal(t, NoStationServing("m"), ShortFailure("no node offers m", "m"))
	require.False(t, strings.Contains(strings.Join(NoMatchFilterNames, " "), "max_price"), "the broker never names max_price")
}
