package main

// toweredge_policy_test.go pins the small pure-policy functions of the edge path that had
// never run: the four checks that make an attachment dispatchable, the clamp on the Tower's
// revenue share, the nil-safety of the best-effort recorders, and the PCG shim the
// placement RNG rides on.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	randv2 "math/rand/v2"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/towercore/attach"
	"rogerai.fm/roger/v6/internal/towercore/attempt"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
)

// The FOUR checks, each refusing alone. The comment on targetFromAttachment says every rule
// about dispatchability lives there so the batch and singular paths cannot fork; this test
// is what makes that claim survivable - a fifth rule added to one caller inline would leave
// this green while the comment went stale, but a rule REMOVED from the shared judgement
// fails here by name.
func TestWhatMakesAnAttachmentDispatchable(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	good := attach.Attachment{
		StationID: "st-1", State: attach.StateActive, Epoch: 3,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: "tw-1"},
		AssertionKey: hex.EncodeToString(pub),
		SessionKey:   strings.Repeat("ab", 32),
	}

	tgt, ok := targetFromAttachment("tw-1", "st-1", "m", "text", good)
	require.True(t, ok)
	require.Equal(t, int64(3), tgt.StationEpoch, "the epoch fence rides the target")
	require.Equal(t, []byte(pub), []byte(tgt.AssertionKey))

	cases := map[string]func(attach.Attachment) attach.Attachment{
		"not live": func(a attach.Attachment) attach.Attachment {
			a.State = "revoked"
			return a
		},
		"rehomed since the projection": func(a attach.Attachment) attach.Attachment {
			a.Origin.TowerID = "tw-elsewhere"
			return a
		},
		"unreadable assertion key": func(a attach.Attachment) attach.Attachment {
			a.AssertionKey = "zz"
			return a
		},
		"short assertion key": func(a attach.Attachment) attach.Attachment {
			a.AssertionKey = "abcd"
			return a
		},
		"unreadable session key": func(a attach.Attachment) attach.Attachment {
			a.SessionKey = "zz"
			return a
		},
		"short session key": func(a attach.Attachment) attach.Attachment {
			// Dispatching here would mean relaying content in the clear.
			a.SessionKey = "abcd"
			return a
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, ok := targetFromAttachment("tw-1", "st-1", "m", "text", mutate(good))
			require.False(t, ok)
		})
	}
}

// The best-effort recorders are called from paths that must not change their answer because
// the ledger hiccuped - so they must be safe on a broker with no tower subsystem at all.
func TestBestEffortRecordersAreNilSafe(t *testing.T) {
	b := &broker{}
	b.noteAttempt("att-1", attempt.Observation{Kind: attempt.KindExecutionFailed, EvidenceHash: "e", Reason: "r", ReleaseID: "rel"})
	b.forgetRoutable("tw-1")
	// Reaching this line IS the assertion: neither call may panic or block.
}

// The Tower's revenue share comes from an env var, and the clamp is a money control: an
// absurd value must fall back to the default rather than pay an absurd share.
func TestEdgeTowerRateClampsTheMoneySplit(t *testing.T) {
	cases := map[string]float64{
		"":     edgeTowerRateDefault,
		"0.15": 0.15,
		"0":    0,
		"1":    1,
		"junk": edgeTowerRateDefault,
		"-0.1": edgeTowerRateDefault,
		"1.5":  edgeTowerRateDefault,
	}
	for v, want := range cases {
		t.Setenv("ROGERAI_TOWER_REVENUE_RATE", v)
		require.Equal(t, want, edgeTowerRate(), "ROGERAI_TOWER_REVENUE_RATE=%q", v)
	}
}

// The PCG shim: Uint64 delegates, Int63 stays non-negative, and Seed is DELIBERATELY a
// no-op - the source is constructed seeded, and honouring a re-seed would silently narrow
// the state a caller thought it had.
func TestThePCGShimKeepsItsContract(t *testing.T) {
	src := &pcgSource{p: randv2.NewPCG(1, 2)}
	a := src.Uint64()
	src.Seed(42) // must change nothing
	b := &pcgSource{p: randv2.NewPCG(1, 2)}
	_ = b.Uint64()
	require.Equal(t, b.Uint64(), src.Uint64(), "Seed re-seeded the source: state was silently narrowed")
	_ = a
	for i := 0; i < 100; i++ {
		require.GreaterOrEqual(t, src.Int63(), int64(0))
	}
}

// A row with no node id carries no load - and the load reader must not invent one.
func TestEdgeCandidateLoadOfAnUnnamedRowIsZero(t *testing.T) {
	b := &broker{}
	require.Zero(t, b.edgeCandidateLoad(fleet.Station{}))
	b.inflight = map[string]int{"n1": 2}
	b.edgeLoad = map[string]int{"n1": 3}
	require.Equal(t, 5, b.edgeCandidateLoad(fleet.Station{NodeID: "n1"}),
		"the sum must include both local halves")
}
