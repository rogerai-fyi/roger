package tower

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// joinStation joins a Station named name, serving models, with a station invite limited to limit
// (nil = no limit).
func joinStation(t *testing.T, st *State, name string, models, limit []string) Station {
	t.Helper()
	_, code, err := st.CreateJoinInvitation(JoinStation, "", limit, 15*time.Minute)
	require.NoError(t, err)
	pub, kh := joinClient(t)
	res, err := st.ConsumeJoin(JoinRequest{Kind: JoinStation, Proof: joinProof(st, code, "n-"+name, pub),
		Nonce: "n-" + name, PubHex: pub, KeyHash: kh, Name: name, Models: models})
	require.NoError(t, err)
	return res.Station
}

// features/join/invite.feature, "A station's model list follows what it actually serves": a
// station's poll reports what it serves now, and the Tower routes exactly that, never more than
// the owner allowed.
func TestAStationsModelsFollowWhatItServes(t *testing.T) {
	st, err := Init(t.TempDir(), ModeStandalone)
	require.NoError(t, err)

	// Joined with an unlimited invite: it may serve whatever it reports.
	open := joinStation(t, st, "kitchen-pi", []string{"qwen3-8b"}, nil)
	got, changed, err := st.SetStationModels(open.ID, []string{"gpt-oss-20b"})
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, []string{"gpt-oss-20b"}, got.Models)
	got, changed, err = st.SetStationModels(open.ID, []string{"gpt-oss-20b"})
	require.NoError(t, err)
	require.False(t, changed, "an unchanged report writes nothing")
	require.Equal(t, []string{"gpt-oss-20b"}, got.Models)

	// Joined with an invite limited to qwen3-8b: a report never widens past the owner's limit.
	limited := joinStation(t, st, "garage-pi", []string{"qwen3-8b"}, []string{"qwen3-8b"})
	got, _, err = st.SetStationModels(limited.ID, []string{"qwen3-8b", "gpt-oss-20b"})
	require.NoError(t, err)
	require.Equal(t, []string{"qwen3-8b"}, got.Models)
	got, _, err = st.SetStationModels(limited.ID, []string{"gpt-oss-20b"})
	require.NoError(t, err)
	require.Empty(t, got.Models, "a limited station that serves nothing it may serve is routed nothing")

	// Attached by hand: the models the owner typed are its limit.
	op := bootstrapped(t, st)
	_ = op
	hand, err := st.AttachStation("attic", "attic-key", []string{"llama-8b"})
	require.NoError(t, err)
	got, _, err = st.SetStationModels(hand.ID, []string{"llama-8b", "gpt-oss-20b"})
	require.NoError(t, err)
	require.Equal(t, []string{"llama-8b"}, got.Models)

	// The list the Tower routes is what Stations() shows.
	list, err := st.Stations()
	require.NoError(t, err)
	byID := map[string][]string{}
	for _, s := range list {
		byID[s.ID] = s.Models
	}
	require.Equal(t, []string{"gpt-oss-20b"}, byID["kitchen-pi"])
	require.Empty(t, byID["garage-pi"])

	_, _, err = st.SetStationModels("nobody", []string{"m"})
	require.Error(t, err, "an unknown station is refused")
}

// bootstrapped admits an operator on st, so a Station can be attached by hand.
func bootstrapped(t *testing.T, st *State) string {
	t.Helper()
	inv, code, err := st.CreateInvitation(testClientKey, time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, testClientKey)
	require.NoError(t, err)
	return testClientKey
}

// A station's report is bounded before it is kept: at most MaxStationModels clean model ids, each
// at most 256 bytes, so a station cannot grow the Tower's registry without limit.
func TestAStationsReportIsBounded(t *testing.T) {
	st, err := Init(t.TempDir(), ModeStandalone)
	require.NoError(t, err)
	open := joinStation(t, st, "kitchen-pi", []string{"qwen3-8b"}, nil)

	many := make([]string, MaxStationModels+1)
	for i := range many {
		many[i] = "m" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	for name, bad := range map[string][]string{
		"too many":     many,
		"empty id":     {""},
		"padded id":    {" qwen3-8b"},
		"control char": {"qwen\n3"},
		"overlong id":  {string(make([]byte, 257))},
	} {
		_, _, err := st.SetStationModels(open.ID, bad)
		require.Error(t, err, name)
	}
	sts, _ := st.Stations()
	require.Equal(t, []string{"qwen3-8b"}, sts[0].Models, "a refused report changes nothing")

	// A join that reports too many models is refused the same way (the uniform refusal).
	_, code, err := st.CreateJoinInvitation(JoinStation, "", nil, 15*time.Minute)
	require.NoError(t, err)
	pub, kh := joinClient(t)
	_, err = st.ConsumeJoin(JoinRequest{Kind: JoinStation, Proof: joinProof(st, code, "n-many", pub),
		Nonce: "n-many", PubHex: pub, KeyHash: kh, Name: "flood", Models: many})
	require.Error(t, err)
}
