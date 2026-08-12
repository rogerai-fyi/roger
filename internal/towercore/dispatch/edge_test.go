package dispatch

// edge_test.go covers the edge grant and the evidence settlement rests on.
//
// Contract: features/tower/edge_dispatch.feature.

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func edgeRegistry(t *testing.T, now time.Time) (*Registry, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	r := NewWithStore(Config{
		Network:  "roger-public",
		Signer:   priv,
		Lifetime: time.Minute,
		Now:      func() time.Time { return now },
	}, nil)
	return r, pub
}

func edgeTarget(t *testing.T) (EdgeTarget, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return EdgeTarget{
		TowerID: "tw-1", StationID: "st-1", StationEpoch: 3,
		Model: "m", Modality: "text", RelayName: "st-1.relay.example",
		MaxIn: 1000, MaxOut: 2000, AssertionKey: pub,
	}, priv
}

// The load-bearing difference from a relayed grant: it authorizes a SCOPE, and there is no
// request digest in it because Roger Core has not seen the request.
func TestAnEdgeGrantBoundsTheAttemptWithoutCommittingToARequest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, core := edgeRegistry(t, now)
	tgt, _ := edgeTarget(t)

	g, err := r.MintEdge(tgt)
	require.NoError(t, err)
	require.NotEmpty(t, g.AttemptID)
	require.NotEmpty(t, g.Nonce)
	require.Equal(t, int64(1000), g.MaxIn)
	require.Equal(t, int64(2000), g.MaxOut)
	require.Equal(t, "st-1.relay.example", g.RelayName)
	require.Equal(t, now.Add(time.Minute).Unix(), g.Deadline.Unix())
	require.NotContains(t, string(g.Signed), "request_digest",
		"an edge grant cannot commit to a request Roger Core has not seen")

	// And any request within the ceiling is authorized by it.
	got, err := ParseEdgeGrant(g.Signed, core, "roger-public", "st-1", []byte("anything"), now)
	require.NoError(t, err)
	require.Equal(t, g.AttemptID, got.AttemptID)
	require.Equal(t, g.Nonce, got.Nonce)
	require.Equal(t, int64(3), got.StationEpoch)
	require.Equal(t, "st-1.relay.example", got.RelayName)
}

// Every attempt gets its own. Two attempts sharing a nonce would make the one-use check pass
// for whichever arrived second.
func TestEveryEdgeGrantIsDistinct(t *testing.T) {
	r, _ := edgeRegistry(t, time.Unix(1_700_000_000, 0))
	tgt, _ := edgeTarget(t)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		g, err := r.MintEdge(tgt)
		require.NoError(t, err)
		require.False(t, seen[g.AttemptID], "attempt id repeated")
		require.False(t, seen[g.Nonce], "nonce repeated")
		seen[g.AttemptID], seen[g.Nonce] = true, true
	}
}

// The ceilings are the ONLY thing bounding an edge attempt now that no digest does, so a
// missing one is refused rather than defaulted. A zero meaning "unlimited" would make
// forgetting a field indistinguishable from authorizing everything.
func TestAnEdgeGrantWithoutItsBoundsIsRefused(t *testing.T) {
	r, _ := edgeRegistry(t, time.Unix(1_700_000_000, 0))
	base, _ := edgeTarget(t)

	for name, mutate := range map[string]func(*EdgeTarget){
		"no tower":     func(g *EdgeTarget) { g.TowerID = "" },
		"no station":   func(g *EdgeTarget) { g.StationID = "" },
		"no model":     func(g *EdgeTarget) { g.Model = "" },
		"no modality":  func(g *EdgeTarget) { g.Modality = "" },
		"no relay":     func(g *EdgeTarget) { g.RelayName = "" },
		"no max in":    func(g *EdgeTarget) { g.MaxIn = 0 },
		"no max out":   func(g *EdgeTarget) { g.MaxOut = 0 },
		"negative in":  func(g *EdgeTarget) { g.MaxIn = -1 },
		"no key":       func(g *EdgeTarget) { g.AssertionKey = nil },
		"short key":    func(g *EdgeTarget) { g.AssertionKey = []byte{1, 2, 3} },
		"negative out": func(g *EdgeTarget) { g.MaxOut = -5 },
	} {
		t.Run(name, func(t *testing.T) {
			tgt := base
			mutate(&tgt)
			_, err := r.MintEdge(tgt)
			require.Error(t, err)
		})
	}
}

// A grant for another Station is somebody else's authorization. A relay holding one and
// pointing it at this Station is precisely what it is positioned to do.
func TestAnEdgeGrantForAnotherStationIsRefused(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, core := edgeRegistry(t, now)
	tgt, _ := edgeTarget(t)
	g, err := r.MintEdge(tgt)
	require.NoError(t, err)

	_, err = ParseEdgeGrant(g.Signed, core, "roger-public", "st-OTHER", []byte("x"), now)
	require.ErrorContains(t, err, `for Station "st-1"`)
}

func TestAnExpiredEdgeGrantIsRefused(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, core := edgeRegistry(t, now)
	tgt, _ := edgeTarget(t)
	g, err := r.MintEdge(tgt)
	require.NoError(t, err)

	_, err = ParseEdgeGrant(g.Signed, core, "roger-public", "st-1", []byte("x"),
		now.Add(2*time.Minute))
	require.ErrorIs(t, err, ErrExpired)
}

// The ceiling is what the digest used to be: without it, one authorization would cover as
// much work as the caller cared to ask for.
func TestARequestOverTheCeilingIsRefusedBeforeAnythingIsSpent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, core := edgeRegistry(t, now)
	tgt, _ := edgeTarget(t)
	tgt.MaxIn = 8
	g, err := r.MintEdge(tgt)
	require.NoError(t, err)

	_, err = ParseEdgeGrant(g.Signed, core, "roger-public", "st-1", make([]byte, 9), now)
	require.ErrorContains(t, err, "the grant allows 8")

	_, err = ParseEdgeGrant(g.Signed, core, "roger-public", "st-1", make([]byte, 8), now)
	require.NoError(t, err, "exactly the ceiling is within it")
}

// A DIFFERENT SIGNED TYPE, and this is the test that matters for it. If edge and relayed
// grants shared a type, a relayed grant could be presented on the edge path - where nothing
// checks a digest - and the check binding the request would simply not run.
func TestARelayedGrantCannotBeUsedAsAnEdgeGrant(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, core := edgeRegistry(t, now)
	tgt, _ := edgeTarget(t)

	relayed, err := r.Mint(Target{
		TowerID: tgt.TowerID, StationID: tgt.StationID, Model: tgt.Model,
		Modality: tgt.Modality, AssertionKey: tgt.AssertionKey,
	}, []byte("the authorized request"))
	require.NoError(t, err)

	_, err = ParseEdgeGrant(relayed.Signed, core, "roger-public", "st-1", []byte("substituted"), now)
	require.Error(t, err, "a relayed grant must not authorize an edge attempt")

	// And the reverse, so neither direction is a way in.
	edge, err := r.MintEdge(tgt)
	require.NoError(t, err)
	_, err = ParseGrant(edge.Signed, core, "roger-public", "st-1", []byte("x"), now)
	require.Error(t, err)
}

func TestAnEdgeGrantSignedByAnybodyElseIsRefused(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, _ := edgeRegistry(t, now)
	tgt, _ := edgeTarget(t)
	g, err := r.MintEdge(tgt)
	require.NoError(t, err)

	impostor, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, err = ParseEdgeGrant(g.Signed, impostor, "roger-public", "st-1", []byte("x"), now)
	require.ErrorContains(t, err, "not signed by Roger Core")

	// A grant for another network fails on the signature, because the network is bound into
	// it rather than compared afterwards.
	_, err = ParseEdgeGrant(g.Signed, nil, "roger-private", "st-1", []byte("x"), now)
	require.Error(t, err)
}

func TestAnUnreadableEdgeGrantIsRefusedRatherThanGuessedAt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	_, core := edgeRegistry(t, now)
	_, err := ParseEdgeGrant([]byte("{not json"), core, "roger-public", "st-1", nil, now)
	require.Error(t, err)
}
