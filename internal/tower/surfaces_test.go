package tower

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// features/join/surfaces.feature: the sentences the CLI, the TUI and the console all take from
// here, so they cannot drift apart.
func TestTheJoinSentencesAreTheSpecsWords(t *testing.T) {
	require.Equal(t, "invite for one device, valid 15 min:", InviteHeading(JoinClient, 15*time.Minute))
	require.Equal(t, "invite for one station, valid 15 min:", InviteHeading(JoinStation, 15*time.Minute))
	require.Equal(t, "on the other machine run:", InviteNext(JoinClient))
	require.Equal(t, "on the station run:", InviteNext(JoinStation))
	require.Equal(t, "laptop-x wants to join · amber-river-71",
		AskLine(PairRequest{Name: "laptop-x", Kind: JoinClient, Phrase: "amber-river-71"}))
	require.Equal(t, "kitchen-pi (a station) wants to join · a-b-c-01 · serving: qwen3-8b, smollm2",
		AskLine(PairRequest{Name: "kitchen-pi", Kind: JoinStation, Phrase: "a-b-c-01", Models: []string{"qwen3-8b", "smollm2"}}))
	require.Equal(t, "approve only if laptop-x shows the same phrase", ApproveHint("laptop-x"))
}

func TestMinutesLeftRoundsUpAndSaysExpired(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	for _, c := range []struct {
		left time.Duration
		want string
	}{
		{15 * time.Minute, "15 min left"},
		{14*time.Minute + time.Second, "15 min left"},
		{61 * time.Second, "2 min left"},
		{time.Minute, "1 min left"},
		{time.Second, "1 min left"},
		{0, "expired"},
		{-time.Minute, "expired"},
	} {
		require.Equal(t, c.want, MinutesLeft(now.Add(c.left).UnixNano(), now), c.left)
	}
}
