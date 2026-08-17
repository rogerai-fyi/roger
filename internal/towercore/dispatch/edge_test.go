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
		MaxIn: 1000, MaxOut: 2000, AssertionKey: pub, ConsumerKey: consumerPub(),
	}, priv
}

func consumerPub() ed25519.PublicKey {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return pub
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
		"negative out":     func(g *EdgeTarget) { g.MaxOut = -5 },
		"negative tok in":  func(g *EdgeTarget) { g.MaxTokIn = -1 },
		"negative tok out": func(g *EdgeTarget) { g.MaxTokOut = -5 },
		"no consumer":      func(g *EdgeTarget) { g.ConsumerKey = nil },
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

// EdgeGrantCeiling reads the authorized bounds at settlement - after the deadline, with no
// request - so it must verify the signature and Station without ParseEdgeGrant's deadline or
// size checks, which would reject a perfectly good settlement.
func TestEdgeGrantCeilingReadsBoundsAfterTheDeadline(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, core := edgeRegistry(t, now)
	tgt, _ := edgeTarget(t)
	tgt.MaxIn, tgt.MaxOut = 111, 222
	g, err := r.MintEdge(tgt)
	require.NoError(t, err)

	// Well past the deadline, which is exactly when settlement runs.
	maxIn, maxOut, err := EdgeGrantCeiling(g.Signed, core, "roger-public", "st-1")
	require.NoError(t, err)
	require.Equal(t, int64(111), maxIn)
	require.Equal(t, int64(222), maxOut)

	// A grant for another Station cannot pass its ceiling into this Station's money path.
	_, _, err = EdgeGrantCeiling(g.Signed, core, "roger-public", "st-OTHER")
	require.ErrorContains(t, err, "not this one")

	// Nor one Core did not sign.
	impostor, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, _, err = EdgeGrantCeiling(g.Signed, impostor, "roger-public", "st-1")
	require.Error(t, err)
}

// Option C per-token billing rides token ceilings ALONGSIDE the byte ceilings. They survive
// mint -> sign -> parse, are readable at settlement via EdgeGrantTokenCeiling, and are
// Station-bound so a substituted grant cannot pass a bogus ceiling into the money path.
func TestEdgeGrantCarriesOptionalTokenCeilings(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, core := edgeRegistry(t, now)
	tgt, _ := edgeTarget(t)
	tgt.MaxTokIn, tgt.MaxTokOut = 300, 800

	g, err := r.MintEdge(tgt)
	require.NoError(t, err)
	require.Equal(t, int64(300), g.MaxTokIn)
	require.Equal(t, int64(800), g.MaxTokOut)

	got, err := ParseEdgeGrant(g.Signed, core, "roger-public", "st-1", []byte("x"), now)
	require.NoError(t, err)
	require.Equal(t, int64(300), got.MaxTokIn)
	require.Equal(t, int64(800), got.MaxTokOut)
	// The byte ceilings remain, independent of the token ceilings.
	require.Equal(t, int64(1000), got.MaxIn)
	require.Equal(t, int64(2000), got.MaxOut)

	ti, to, err := EdgeGrantTokenCeiling(g.Signed, core, "roger-public", "st-1")
	require.NoError(t, err)
	require.Equal(t, int64(300), ti)
	require.Equal(t, int64(800), to)

	// A grant minted for st-1 must not yield a ceiling when read as another Station's.
	_, _, err = EdgeGrantTokenCeiling(g.Signed, core, "roger-public", "st-OTHER")
	require.Error(t, err)

	// Nor when read against an impostor signer: the ceiling is only meaningful because Core set it.
	impostor, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, _, err = EdgeGrantTokenCeiling(g.Signed, impostor, "roger-public", "st-1")
	require.Error(t, err)
}

// A byte-only grant (no token ceiling set, and any grant minted before Option C) reads back
// with zero token ceilings and is NOT an error - 0 means "not token-bounded", so the byte cap
// and audit still govern it.
func TestAByteOnlyEdgeGrantHasNoTokenCeiling(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, core := edgeRegistry(t, now)
	tgt, _ := edgeTarget(t) // token ceilings default to 0

	g, err := r.MintEdge(tgt)
	require.NoError(t, err)

	got, err := ParseEdgeGrant(g.Signed, core, "roger-public", "st-1", []byte("x"), now)
	require.NoError(t, err)
	require.Zero(t, got.MaxTokIn, "absent token ceiling reads as 0 (no ceiling)")
	require.Zero(t, got.MaxTokOut)

	ti, to, err := EdgeGrantTokenCeiling(g.Signed, core, "roger-public", "st-1")
	require.NoError(t, err, "a byte-only grant has no token ceiling, which is not an error")
	require.Zero(t, ti)
	require.Zero(t, to)
}

// The optional-ceiling parser: an ABSENT field (old grant) is 0; a present one must be a valid
// non-negative integer, and a negative or malformed value is refused rather than silently
// treated as "unset" - a malformed money bound must never disable itself.
func TestParseOptionalCeiling(t *testing.T) {
	ok := []struct {
		in   string
		want int64
	}{{"", 0}, {"0", 0}, {"500", 500}}
	for _, tc := range ok {
		got, err := parseOptionalCeiling(tc.in)
		require.NoError(t, err, tc.in)
		require.Equal(t, tc.want, got, tc.in)
	}
	for _, bad := range []string{"-1", "abc", "1.5"} {
		_, err := parseOptionalCeiling(bad)
		require.Error(t, err, bad)
	}
}

// EdgeGrantMeta reads a grant's public routing metadata (attempt/station/deadline) after
// verifying Core's signature, so a Tower can authorize a submit without touching the sealed
// request. It rejects an impostor signer and an expired grant, and skips expiry on a zero time.
func TestEdgeGrantMetaReadsRoutingMetadataAndVerifies(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, core := edgeRegistry(t, now)
	tgt, _ := edgeTarget(t)
	g, err := r.MintEdge(tgt)
	require.NoError(t, err)

	att, station, deadline, err := EdgeGrantMeta(g.Signed, core, "roger-public", "tw-1", now)
	require.NoError(t, err)
	require.Equal(t, g.AttemptID, att)
	require.Equal(t, "st-1", station)
	require.Equal(t, now.Add(time.Minute).Unix(), deadline.Unix())

	// Impostor signer rejected.
	impostor, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, _, _, err = EdgeGrantMeta(g.Signed, impostor, "roger-public", "tw-1", now)
	require.Error(t, err)

	// Expired rejected (now past the deadline).
	_, _, _, err = EdgeGrantMeta(g.Signed, core, "roger-public", "tw-1", now.Add(2*time.Minute))
	require.ErrorIs(t, err, ErrExpired)

	// A grant minted for tw-1 cannot be replayed at another tower.
	_, _, _, err = EdgeGrantMeta(g.Signed, core, "roger-public", "tw-OTHER", now)
	require.ErrorContains(t, err, "for Tower")

	// Empty tower id skips the tower check; zero time skips the expiry check.
	_, _, _, err = EdgeGrantMeta(g.Signed, core, "roger-public", "", time.Time{})
	require.NoError(t, err)
}

// Option C: the consumer's X25519 sealing key rides the grant so the node can seal its result
// back to the consumer through a blind tower. It is signature-covered, optional (absent on a
// byte-path grant), and a malformed one is refused at both mint and parse.
func TestEdgeGrantCarriesTheConsumerEnvelopeKey(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, core := edgeRegistry(t, now)
	tgt, _ := edgeTarget(t)
	envKey := make([]byte, 32)
	for i := range envKey {
		envKey[i] = byte(i + 1)
	}
	tgt.ConsumerEnvKey = envKey

	g, err := r.MintEdge(tgt)
	require.NoError(t, err)
	got, err := ParseEdgeGrant(g.Signed, core, "roger-public", "st-1", []byte("x"), now)
	require.NoError(t, err)
	require.Equal(t, envKey, got.ConsumerEnvKey, "the sealing key survives the round trip")

	// Absent on a byte-path grant: nil, no error.
	plain, _ := edgeTarget(t)
	g2, err := r.MintEdge(plain)
	require.NoError(t, err)
	got2, err := ParseEdgeGrant(g2.Signed, core, "roger-public", "st-1", []byte("x"), now)
	require.NoError(t, err)
	require.Nil(t, got2.ConsumerEnvKey)

	// Malformed at mint: refused rather than signed dead-on-arrival.
	bad, _ := edgeTarget(t)
	bad.ConsumerEnvKey = []byte{1, 2, 3}
	_, err = r.MintEdge(bad)
	require.ErrorContains(t, err, "32 bytes")
}

// The pinned consumer price survives mint -> sign -> parse and is readable at settlement via
// EdgeGrantPricing (signature + Station-bound). This is the round trip that guards the money:
// a grant whose signed body dropped the price would bill zero.
func TestEdgeGrantPinsThePrice(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r, core := edgeRegistry(t, now)
	tgt, _ := edgeTarget(t)
	tgt.PriceInMicros, tgt.PriceOutMicros = 180_000, 300_000 // $0.18 / $0.30 per 1M tokens

	g, err := r.MintEdge(tgt)
	require.NoError(t, err)
	require.Equal(t, int64(180_000), g.PriceInMicros)
	require.Equal(t, int64(300_000), g.PriceOutMicros)

	got, err := ParseEdgeGrant(g.Signed, core, "roger-public", "st-1", []byte("x"), now)
	require.NoError(t, err)
	require.Equal(t, int64(180_000), got.PriceInMicros, "the price survives the signed round trip")
	require.Equal(t, int64(300_000), got.PriceOutMicros)

	pin, pout, err := EdgeGrantPricing(g.Signed, core, "roger-public", "st-1")
	require.NoError(t, err)
	require.Equal(t, int64(180_000), pin)
	require.Equal(t, int64(300_000), pout)

	// Station-bound and signer-bound, like every other money reader.
	_, _, err = EdgeGrantPricing(g.Signed, core, "roger-public", "st-OTHER")
	require.Error(t, err)
	impostor, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, _, err = EdgeGrantPricing(g.Signed, impostor, "roger-public", "st-1")
	require.Error(t, err)

	// An unpriced grant reads 0/0 with no error; a negative price is refused at mint.
	plain, _ := edgeTarget(t)
	g2, err := r.MintEdge(plain)
	require.NoError(t, err)
	pin, pout, err = EdgeGrantPricing(g2.Signed, core, "roger-public", "st-1")
	require.NoError(t, err)
	require.Zero(t, pin)
	require.Zero(t, pout)
	bad, _ := edgeTarget(t)
	bad.PriceOutMicros = -1
	_, err = r.MintEdge(bad)
	require.ErrorContains(t, err, "cannot be negative")
}
