package station

// edge_sealed_test.go proves the Topology-2 serve with REAL crypto end to end: the consumer's
// sealed request opens only at the Station, the edge grant governs it, the receipt carries the
// model's token usage, and the result seals back to the consumer - unreadable to the tower.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/envelope"
)

// sealedRig mints a Core registry, a station, a consumer envelope keypair, and an edge grant
// bound to them. mutate tweaks the target before minting.
func sealedRig(t *testing.T, now time.Time, mutate func(*dispatch.EdgeTarget)) (
	e EdgeExecutor, s *Station, grant dispatch.EdgeGrant, consumerPriv []byte, up *stubUpstream) {

	t.Helper()
	s = initStation(t)
	corePub, corePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	reg := dispatch.NewWithStore(dispatch.Config{
		Network: "roger-public", Signer: corePriv, Lifetime: time.Minute,
		Now: func() time.Time { return now },
	}, nil)

	envPub, envPriv, err := envelope.NewKey()
	require.NoError(t, err)
	tgt := dispatch.EdgeTarget{
		TowerID: "tw-1", StationID: s.StationID, Model: "m", Modality: "text",
		RelayName: "st.relay.example", MaxIn: 4096, MaxOut: 4096,
		AssertionKey: s.AssertionPub(), ConsumerKey: edgeConsumerKey(),
		ConsumerEnvKey: envPub,
	}
	if mutate != nil {
		mutate(&tgt)
	}
	g, err := reg.MintEdge(tgt)
	require.NoError(t, err)

	up = &stubUpstream{body: []byte(`{"choices":[{"text":"hi"}],"usage":{"prompt_tokens":6,"completion_tokens":2}}`)}
	e = EdgeExecutor{Station: s, CoreKey: corePub, Network: "roger-public", Upstream: up,
		Now: func() time.Time { return now }}
	return e, s, g, envPriv, up
}

// The whole blind path, real crypto: sealed in, grant-verified, token receipt, sealed out.
func TestServeSealedRoundTripsWithRealCrypto(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, s, g, consumerPriv, up := sealedRig(t, now, nil)

	request := []byte(`{"prompt":"hello"}`)
	sealedReq := sealFor(t, s, g.AttemptID, request)

	resultEnv, receipt, failure := e.ServeSealed(context.Background(), g.Signed, sealedReq)
	require.Empty(t, failure)
	require.NotEmpty(t, receipt)
	require.Equal(t, request, up.saw, "the model was given exactly the consumer's plaintext")

	// The result is NOT the plaintext - the tower carries it unreadable.
	require.NotContains(t, string(resultEnv), `"choices"`, "the result must not cross the tower in the clear")

	// Only the CONSUMER can open it, bound to this attempt.
	sealed, err := envelope.Parse(resultEnv)
	require.NoError(t, err)
	body, err := envelope.OpenWith(consumerPriv, sealed, g.AttemptID)
	require.NoError(t, err)
	require.Equal(t, up.body, body, "the consumer gets the model's own bytes")

	// The receipt verifies against the Station's key and carries the model's token usage.
	rec, err := dispatch.ParseReceipt(receipt, s.AssertionPub(), "roger-public", g.AttemptID, s.StationID)
	require.NoError(t, err)
	require.Equal(t, dispatch.Usage{In: 6, Out: 2}, rec.TokUsage)
	require.Equal(t, dispatch.Usage{In: int64(len(request)), Out: int64(len(up.body))}, rec.Usage)
}

// A stranger cannot open the result: it is sealed to the grant's consumer key alone.
func TestServeSealedResultOpensOnlyForTheConsumer(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, s, g, _, _ := sealedRig(t, now, nil)
	resultEnv, _, failure := e.ServeSealed(context.Background(), g.Signed, sealFor(t, s, g.AttemptID, []byte(`{"p":1}`)))
	require.Empty(t, failure)

	_, strangerPriv, err := envelope.NewKey()
	require.NoError(t, err)
	sealed, err := envelope.Parse(resultEnv)
	require.NoError(t, err)
	_, err = envelope.OpenWith(strangerPriv, sealed, g.AttemptID)
	require.Error(t, err, "a tower or stranger holding the bytes cannot open the result")
}

// A grant with NO consumer sealing key is refused: the sealed path has nothing readable to
// return without one, and degrading to plaintext would hand the payload to the relay.
func TestServeSealedRefusesAGrantWithoutASealingKey(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, s, g, _, up := sealedRig(t, now, func(tgt *dispatch.EdgeTarget) { tgt.ConsumerEnvKey = nil })
	env, receipt, failure := e.ServeSealed(context.Background(), g.Signed, sealFor(t, s, g.AttemptID, []byte(`{"p":1}`)))
	require.Contains(t, failure, "no consumer sealing key")
	require.Empty(t, receipt, "a refusal must never carry a settleable receipt")
	require.Empty(t, env)
	require.Nil(t, up.saw, "the model is never reached on a refusal")
}

// A request sealed to somebody else's key never opens and never reaches the model.
func TestServeSealedRefusesARequestSealedToAnotherKey(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, _, g, _, up := sealedRig(t, now, nil)
	otherPub, _, err := envelope.NewKey()
	require.NoError(t, err)
	sealed, err := envelope.SealTo(otherPub, []byte(`{"p":1}`), g.AttemptID)
	require.NoError(t, err)
	raw, err := sealed.Marshal()
	require.NoError(t, err)

	_, receipt, failure := e.ServeSealed(context.Background(), g.Signed, raw)
	require.Contains(t, failure, "not sealed to this Station")
	require.Empty(t, receipt)
	require.Nil(t, up.saw)
}

// An expired grant is refused before the model runs.
func TestServeSealedRefusesAnExpiredGrant(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, s, g, _, up := sealedRig(t, now, nil)
	e.Now = func() time.Time { return now.Add(2 * time.Minute) } // past the deadline
	_, receipt, failure := e.ServeSealed(context.Background(), g.Signed, sealFor(t, s, g.AttemptID, []byte(`{"p":1}`)))
	require.Contains(t, failure, "expired")
	require.Empty(t, receipt)
	require.Nil(t, up.saw)
}

// The receipt lands in the outbox (the copy that reaches settlement) before the result leaves.
func TestServeSealedQueuesTheReceiptForTheCourier(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, s, g, _, _ := sealedRig(t, now, nil)
	e.Outbox = NewOutbox(8)
	_, receipt, failure := e.ServeSealed(context.Background(), g.Signed, sealFor(t, s, g.AttemptID, []byte(`{"p":1}`)))
	require.Empty(t, failure)
	pending := e.Outbox.Collect(8)
	require.Len(t, pending, 1)
	require.Equal(t, g.AttemptID, pending[0].AttemptID)
	require.Equal(t, receipt, pending[0].Receipt, "the courier carries the same signed receipt the consumer got")
}

// A replayed job for an already-served attempt is refused without a receipt: a hostile tower
// holding the grant + envelope verbatim cannot burn this node's compute by re-injecting it.
func TestServeSealedRefusesAReplayedAttempt(t *testing.T) {
	// A REAL clock here: the AttemptCache expires entries at the grant deadline against wall
	// time, so a fixed past rig-clock would make every entry born expired.
	now := time.Now()
	e, s, g, _, up := sealedRig(t, now, nil)
	e.Seen = NewAttemptCache()
	sealedReq := sealFor(t, s, g.AttemptID, []byte(`{"p":1}`))

	_, receipt, failure := e.ServeSealed(context.Background(), g.Signed, sealedReq)
	require.Empty(t, failure)
	require.NotEmpty(t, receipt)
	served := append([]byte(nil), up.saw...)

	_, receipt2, failure2 := e.ServeSealed(context.Background(), g.Signed, sealedReq)
	require.Contains(t, failure2, "already served")
	require.Empty(t, receipt2, "a replay must not mint a second receipt")
	require.Equal(t, served, up.saw, "the model is not run a second time")
}

// An upstream failure crosses the blind tower as a GENERIC class only - the model's own error
// body can echo request fragments, and this failure string is the one thing that travels in
// the clear. And it carries no receipt.
func TestServeSealedUpstreamFailureIsGenericAndUnreceipted(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, s, g, _, up := sealedRig(t, now, nil)
	up.err = errors.New(`upstream 422: invalid prompt "the secret launch codes"`)

	env, receipt, failure := e.ServeSealed(context.Background(), g.Signed, sealFor(t, s, g.AttemptID, []byte(`{"p":"the secret launch codes"}`)))
	require.Equal(t, "the model did not answer", failure, "the wire gets only the class")
	require.NotContains(t, failure, "launch codes", "no upstream echo crosses the tower")
	require.Empty(t, receipt)
	require.Empty(t, env)
}

// The byte ceilings govern the sealed path exactly as the TLS edge: an over-ceiling request
// never reaches the model, an over-ceiling answer is never signed for.
func TestServeSealedEnforcesTheByteCeilings(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	// Input over MaxIn: refused before the model.
	e, s, g, _, up := sealedRig(t, now, func(tgt *dispatch.EdgeTarget) { tgt.MaxIn = 8 })
	_, receipt, failure := e.ServeSealed(context.Background(), g.Signed, sealFor(t, s, g.AttemptID, make([]byte, 9)))
	require.Contains(t, failure, "the grant allows 8")
	require.Empty(t, receipt)
	require.Nil(t, up.saw)

	// Output over MaxOut: served but never signed for.
	e2, s2, g2, _, up2 := sealedRig(t, now, func(tgt *dispatch.EdgeTarget) { tgt.MaxOut = 4 })
	up2.body = []byte("far more than four bytes")
	_, receipt2, failure2 := e2.ServeSealed(context.Background(), g2.Signed, sealFor(t, s2, g2.AttemptID, []byte(`{}`)))
	require.Contains(t, failure2, "more than this grant allows")
	require.Empty(t, receipt2)
}

// Grant A paired with an envelope sealed for attempt B does not open - the AAD binding, on the
// sealed path itself.
func TestServeSealedRefusesAnEnvelopeForAnotherAttempt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, s, g, _, up := sealedRig(t, now, nil)
	sealedForOther := sealFor(t, s, "att-somebody-else", []byte(`{"p":1}`))
	_, receipt, failure := e.ServeSealed(context.Background(), g.Signed, sealedForOther)
	require.Contains(t, failure, "not sealed to this Station")
	require.Empty(t, receipt)
	require.Nil(t, up.saw)
}

// The RESULT is bound to its attempt too: the sealed answer does not open under another
// attempt id, so a tower cannot replay one attempt's answer into another.
func TestServeSealedResultIsBoundToItsAttempt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	e, s, g, consumerPriv, _ := sealedRig(t, now, nil)
	resultEnv, _, failure := e.ServeSealed(context.Background(), g.Signed, sealFor(t, s, g.AttemptID, []byte(`{"p":1}`)))
	require.Empty(t, failure)
	sealed, err := envelope.Parse(resultEnv)
	require.NoError(t, err)
	_, err = envelope.OpenWith(consumerPriv, sealed, "att-a-different-attempt")
	require.Error(t, err, "the result must not open under another attempt id")
}
