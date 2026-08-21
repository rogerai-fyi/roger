package main

// toweredge_billing_test.go covers the founder-approved Tower revenue share end to end: when
// edge traffic is PRICED, settling a relayed attempt charges the consumer and pays BOTH the
// serving Station's owner and the relaying Tower's operator through the shared wallet.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/store"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
)

func TestEdgeBillingPaysStationOwnerAndTowerOperator(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "1000000") // 1 credit per output byte (credits per 1M bytes)
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	b, srv := towerTestBroker(t)
	b.feeRate = 0.30 // 30% platform fee; the Tower's 10% comes out of that margin

	// The Tower operator (one account) and the Station owner (a DIFFERENT account), so the split
	// is observably to two wallets.
	op := signedInOperator(t, b, "tower-op")
	towerAcct := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)

	stPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	stationOwner := hexOf(stPub)
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: stationOwner, Login: "station-op", Email: "s@x.test", EmailVerifiedAt: time.Now().Unix(),
	}))
	stationPriv := attachStation(t, b, "st-1", tw.id, stationOwner)

	// A real grant with a ceiling of 1000 out bytes, and a funded consumer whose authorize-time
	// hold reserved the ceiling price (1000 * 1 = 1000 credits).
	cpub := issuedEdgeGrant(t, b, "att-1", tw.id, "st-1", 1000, 1000)
	consumerWallet := bindEdgeConsumer(t, b, cpub)
	_, err = b.db.AddCredits(consumerWallet, 100000)
	require.NoError(t, err)
	ok, err := b.db.HoldFor(consumerWallet, "att-1", 1000)
	require.NoError(t, err)
	require.True(t, ok)

	// Settle with 50 billable output bytes -> cost 50 credits.
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1", []byte("answer"),
			dispatch.Usage{In: 0, Out: 50}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)

	now := time.Now()
	// Station owner earns cost*(1-fee) = 50*0.7 = 35.
	sSt, _ := b.db.EarningSplitOf(stationOwner, now)
	require.InDelta(t, 35, sSt.Payable, 0.001, "station owner earns its 70%")
	// Tower operator earns 10% of GROSS = 50*0.10 = 5 (platform absorbs it; its 30% fee -> 20%).
	sTw, _ := b.db.EarningSplitOf(towerAcct, now)
	require.InDelta(t, 5, sTw.Payable, 0.001, "tower operator earns 10% of gross")
	// Consumer charged exactly the 50 (held 1000, refunded 950).
	bal, _ := b.db.PeekBalance(consumerWallet)
	require.InDelta(t, 100000-50, bal, 0.001, "consumer billed only the actual cost")

	// A refund of the request claws BOTH the station and tower lots.
	_, _, err = b.db.RefundLineage("rf-1", []string{"att-1"}, consumerWallet, "att-1", 50, now)
	require.NoError(t, err)
	sSt, _ = b.db.EarningSplitOf(stationOwner, now)
	sTw, _ = b.db.EarningSplitOf(towerAcct, now)
	require.InDelta(t, 0, sSt.Payable, 0.001, "station lot clawed on refund")
	require.InDelta(t, 0, sTw.Payable, 0.001, "tower lot clawed on refund")
}

// Billing is ON by default; an operator can turn it OFF by zeroing both rates. When off,
// settling a relayed attempt bills nothing and mints no wallet lots - edge traffic is free again.
func TestEdgeBillingIsDormantWhenUnpriced(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "0")
	b, srv := towerTestBroker(t)
	b.feeRate = 0.30
	op := signedInOperator(t, b, "tower-op")
	towerAcct := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, towerAcct)
	issuedEdgeGrant(t, b, "att-1", tw.id, "st-1", 1000, 1000)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1", []byte("answer"),
			dispatch.Usage{In: 0, Out: 50}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)

	s, _ := b.db.EarningSplitOf(towerAcct, time.Now())
	require.Equal(t, float64(0), s.Payable, "unpriced edge traffic mints no wallet earnings")
}

// Review finding 1: the dispatch one-use Settle and the wallet capture are separate commits. If
// the first attempt faulted AFTER the dispatch settle but BEFORE billing, a retry must COMPLETE
// the billing, not 409 and leave the consumer's hold to be swept (free work) with the operators
// unpaid. Simulate the crash by pre-settling the dispatch attempt, then settling through the
// endpoint: it must pay both parties.
func TestSettleCompletesBillingAfterAStrandedDispatchSettle(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "1000000")
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	b, srv := towerTestBroker(t)
	b.feeRate = 0.30
	op := signedInOperator(t, b, "tower-op")
	towerAcct := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	stationOwner := hexOf(stPub)
	require.NoError(t, b.db.BindOwner(store.Owner{Pubkey: stationOwner, Login: "station-op", Email: "s@x.test", EmailVerifiedAt: time.Now().Unix()}))
	stationPriv := attachStation(t, b, "st-1", tw.id, stationOwner)
	cpub := issuedEdgeGrant(t, b, "att-1", tw.id, "st-1", 1000, 1000)
	consumerWallet := bindEdgeConsumer(t, b, cpub)
	_, _ = b.db.AddCredits(consumerWallet, 100000)
	ok, err := b.db.HoldFor(consumerWallet, "att-1", 1000)
	require.NoError(t, err)
	require.True(t, ok)

	// Simulate a crash AFTER the dispatch settle but BEFORE billing: pre-claim + settle the
	// dispatch attempt so the endpoint sees ErrAlreadySettled.
	now := time.Now()
	_, err = b.tower.dispatch.Store().ClaimByID("att-1", tw.id, now)
	require.NoError(t, err)
	_, err = b.tower.dispatch.Store().Settle("att-1", now)
	require.NoError(t, err)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1", []byte("answer"), dispatch.Usage{In: 0, Out: 50}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusConflict, code, "an already-settled attempt still 409s, but completes billing first")

	sSt, _ := b.db.EarningSplitOf(stationOwner, now)
	require.InDelta(t, 35, sSt.Payable, 0.001, "station paid on the completion")
	sTw, _ := b.db.EarningSplitOf(towerAcct, now)
	require.InDelta(t, 5, sTw.Payable, 0.001, "tower paid 10% of gross on the completion")
	bal, _ := b.db.PeekBalance(consumerWallet)
	require.InDelta(t, 100000-50, bal, 0.001, "consumer charged the actual cost")

	// And a SECOND retry is a clean idempotent no-op (no double-charge / double-pay).
	code, _ = tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusConflict, code)
	sSt, _ = b.db.EarningSplitOf(stationOwner, now)
	require.InDelta(t, 35, sSt.Payable, 0.001, "no double-pay on a second retry")
	bal, _ = b.db.PeekBalance(consumerWallet)
	require.InDelta(t, 100000-50, bal, 0.001, "no double-charge on a second retry")
}

// Review finding 2: the already-settled completion path must NOT re-run audit selection - the
// audit's Resolve deletes the wanted row when the transcript arrives, so re-selecting would
// re-open a resolved audit and make the Tower re-serve a transcript it already proved.
func TestAReplayDoesNotReopenAResolvedAudit(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))
	issuedEdgeGrant(t, b, "att-1", tw.id, "st-1", 1000, 50) // low out ceiling forces a dispute+audit

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1", []byte("answer"),
			dispatch.Usage{In: 0, Out: 5000}), // over the 50 ceiling -> disputed -> force-audited
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, true, out["disputed"])

	// The attempt is wanted for audit.
	pending, err := b.tower.auditWanted.Pending(tw.id, time.Now())
	require.NoError(t, err)
	require.Len(t, pending, 1)

	// The transcript arrives and RESOLVES the audit (delete the wanted row).
	require.NoError(t, b.tower.auditWanted.Resolve("att-1"))
	pending, _ = b.tower.auditWanted.Pending(tw.id, time.Now())
	require.Len(t, pending, 0)

	// A REPLAY of the settle (the dispatch attempt is already settled) must complete idempotently
	// and 409 - WITHOUT re-opening the resolved audit.
	code, _ = tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusConflict, code, "a replay is a conflict")
	pending, _ = b.tower.auditWanted.Pending(tw.id, time.Now())
	require.Len(t, pending, 0, "the resolved audit was NOT re-opened by the replay")
}

// The consumer's pre-auth hold is reclaimed by the sweep after holdTTL, and the edge settlement
// window is grantLifetime + edgeSettleGrace(). If the window ever outran holdTTL, a late-but-valid
// receipt would find its hold already swept and settle for FREE with the operator unpaid. That
// coupling is arithmetic (edgeSettleGrace derives from holdTTL) and easy to break with a future
// change to the grant lifetime, the grace, or the margin - so it is asserted here at every
// holdTTL a deployment can actually produce, default included.
//
// THE SHORT VALUES ARE THE POINT, and they were missing. This test used to run from six minutes
// upward and called that "the realistic range", which made the invariant read as unconditional
// when it was only true above a threshold nobody had written down. edgeSettleGrace clamps at
// minEdgeSettleGrace, and once it has clamped it no longer tracks holdTTL at all: at
// ROGERAI_HOLD_TTL=2m the window was 2m against a 2m hold, so a receipt arriving in the last
// seconds of its own window found the hold swept - free work, operator unpaid, reachable by
// setting one environment variable. holdTTL is floored at minHoldTTL now, which is why every
// row below passes; before the floor the first three failed.
func TestEdgeSettleWindowStaysInsideTheHoldTTL(t *testing.T) {
	check := func(t *testing.T) {
		window := towerAttemptLifetime + edgeSettleGrace()
		require.Less(t, window, holdTTL(),
			"edge settle window %v must stay strictly under holdTTL %v so a hold always outlives its attempt's deadline",
			window, holdTTL())
	}
	t.Run("default", check) // no ROGERAI_HOLD_TTL set
	for _, ttl := range []string{"30s", "1m", "2m", "3m", "4m", "6m", "10m", "15m", "30m", "1h"} {
		t.Run(ttl, func(t *testing.T) { t.Setenv("ROGERAI_HOLD_TTL", ttl); check(t) })
	}
}

// Tower inference requires a SIGNED-IN account (which is where the terms of service are accepted).
// A validly-signed request from a key that is NOT bound to an account is refused before any
// Station is chosen or any hold placed - the consent gate for charging real money.
func TestEdgeAuthorizeRequiresASignedInAccount(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	tw := enrolledTower(t, b, op.login)
	attachStation(t, b, "st-1", tw.id, ownerPubkeyOf(t, b, op.login))
	routableEdge(t, b, tw.id, "st-1", "m", "203.0.113.7:8443")

	// A fresh key that signs correctly but belongs to no account.
	_, strangerPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	code, _ := consumerCall(t, srv, strangerPriv, "/tower/edge/authorize", map[string]any{"model": "m", "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusForbidden, code, "a key not bound to an account is refused")

	// The same request from a signed-in, funded account is authorized.
	member := signedInConsumer(t, b)
	code, _ = consumerCall(t, srv, member, "/tower/edge/authorize", map[string]any{"model": "m", "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusOK, code)
}

// A banned account is signed in but must not be served or charged - refused with the same 403 as
// an unknown account, so a ban cannot be distinguished from "not signed in".
func TestEdgeAuthorizeRefusesABannedAccount(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	tw := enrolledTower(t, b, op.login)
	attachStation(t, b, "st-1", tw.id, ownerPubkeyOf(t, b, op.login))
	routableEdge(t, b, tw.id, "st-1", "m", "203.0.113.7:8443")

	member := signedInConsumer(t, b)
	memberPub := hexOf(member.Public().(ed25519.PublicKey))
	// Ban that account.
	b.metricsMu.Lock()
	if b.bannedOwners == nil {
		b.bannedOwners = map[string]bool{}
	}
	b.bannedOwners[memberPub] = true
	b.metricsMu.Unlock()

	code, _ := consumerCall(t, srv, member, "/tower/edge/authorize", map[string]any{"model": "m", "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusForbidden, code, "a banned account is refused")
}

// The Tower operator's earning is tagged with a "tower:" node prefix so the earnings dashboard can
// show the RELAY share apart from a node-SERVING share. It changes no money (payout/clawback key on
// the account + request), only provenance.
func TestTowerRelayEarningsAreTaggedForTheDashboard(t *testing.T) {
	require.True(t, IsTowerNode("tower:tw-ams"))
	require.False(t, IsTowerNode("gpu-01"))

	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "1000000")
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	b, srv := towerTestBroker(t)
	b.feeRate = 0.30
	op := signedInOperator(t, b, "tower-op")
	towerAcct := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	stationOwner := hexOf(stPub)
	require.NoError(t, b.db.BindOwner(store.Owner{Pubkey: stationOwner, Login: "station-op2", Email: "s2@x.test", EmailVerifiedAt: time.Now().Unix()}))
	stationPriv := attachStation(t, b, "st-1", tw.id, stationOwner)
	cpub := issuedEdgeGrant(t, b, "att-1", tw.id, "st-1", 1000, 1000)
	consumerWallet := bindEdgeConsumer(t, b, cpub)
	_, _ = b.db.AddCredits(consumerWallet, 100000)
	ok, err := b.db.HoldFor(consumerWallet, "att-1", 1000)
	require.NoError(t, err)
	require.True(t, ok)
	body, _ := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1", []byte("answer"), dispatch.Usage{In: 0, Out: 50}),
	})
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)

	// The tower operator's only earning is the relay lot, tagged "tower:".
	_, byNode, err := b.db.EarningRollups(towerAcct)
	require.NoError(t, err)
	require.Len(t, byNode, 1)
	require.True(t, IsTowerNode(byNode[0].Key), "the tower operator's earning is tagged as a relay share")
	// The station owner's is an ordinary serving lot (not tower-tagged).
	_, stNode, _ := b.db.EarningRollups(stationOwner)
	require.Len(t, stNode, 1)
	require.False(t, IsTowerNode(stNode[0].Key), "the station owner's earning is a serving share")
}

// issuedEdgeGrantPriced mints an edge grant carrying token ceilings AND a pinned per-token
// price (micro-USD per 1M tokens), the Option C money shape.
func issuedEdgeGrantPriced(t *testing.T, b *broker, attemptID, towerID, stationID string,
	priceInMicros, priceOutMicros int64) ed25519.PublicKey {
	t.Helper()
	cpub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	apub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g, err := b.tower.dispatch.MintEdge(dispatch.EdgeTarget{
		TowerID: towerID, StationID: stationID, StationEpoch: 1, Model: "m", Modality: "text",
		RelayName: stationID + ".relay.example", MaxIn: 8 << 20, MaxOut: 8 << 20,
		MaxTokIn: 1 << 20, MaxTokOut: 1 << 20,
		PriceInMicros: priceInMicros, PriceOutMicros: priceOutMicros,
		AssertionKey: apub, ConsumerKey: cpub,
	})
	require.NoError(t, err)
	require.NoError(t, b.tower.dispatch.Store().Put(dispatch.Record{
		AttemptID: attemptID, JobID: g.JobID, TowerID: towerID, StationID: stationID,
		StationEpoch: 1, Model: "m", Modality: "text", Nonce: g.Nonce,
		Deadline: time.Now().Add(time.Hour), Grant: g.Signed, ConsumerKey: cpub,
		State: dispatch.StateIssued,
	}))
	return cpub
}

// THE OPTION C MONEY TEST: a token-priced attempt settles at tokens x the price PINNED IN THE
// GRANT, splitting 70% to the serving node's owner, 10% of gross to the tower operator, 20%
// to the platform - through the same wallet as everything else. The byte tariff is OFF, so a
// charge can only have come from the pinned token price.
func TestTokenPricedSettlementPaysAtThePinnedPrice(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "0") // byte tariff OFF: only the pinned price can bill
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	b, srv := towerTestBroker(t)
	b.feeRate = 0.30
	op := signedInOperator(t, b, "tower-op")
	towerAcct := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)

	stPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	stationOwner := hexOf(stPub)
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: stationOwner, Login: "station-op", Email: "sp@x.test", EmailVerifiedAt: time.Now().Unix(),
	}))
	stationPriv := attachStation(t, b, "st-1", tw.id, stationOwner)

	// Price: $5,000 per 1M output tokens = 5e9 micros (inside the mint sanity cap; bands gate
	// leaf admission). 200 billable output tokens -> cost = 200 x 5e9 / 1e12 = 1.0 credit.
	cpub := issuedEdgeGrantPriced(t, b, "att-1", tw.id, "st-1", 0, 5_000_000_000)
	consumerWallet := bindEdgeConsumer(t, b, cpub)
	_, err = b.db.AddCredits(consumerWallet, 1000)
	require.NoError(t, err)
	held, err := b.db.HoldFor(consumerWallet, "att-1", 10)
	require.NoError(t, err)
	require.True(t, held)

	// The node's receipt: byte out 300 (within ceiling, and >= tokens), token out 200.
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceiptTok(t, stationPriv, "att-1", "st-1", make([]byte, 300),
			dispatch.Usage{In: 10, Out: 300}, dispatch.Usage{In: 0, Out: 200}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)

	now := time.Now()
	// cost = 1.0 credit: node owner 70% = 0.70, tower 10% of gross = 0.10.
	sSt, _ := b.db.EarningSplitOf(stationOwner, now)
	require.InDelta(t, 0.70, sSt.Payable, 1e-9, "the serving owner earns 70% of the token-priced cost")
	sTw, _ := b.db.EarningSplitOf(towerAcct, now)
	require.InDelta(t, 0.10, sTw.Payable, 1e-9, "the tower operator earns 10% of gross")
	bal, _ := b.db.PeekBalance(consumerWallet)
	require.InDelta(t, 1000-1.0, bal, 1e-9, "the consumer paid exactly tokens x the pinned price (hold remainder refunded)")
}

// A token-priced grant whose node signed NO token claim falls back to the byte tariff - but
// CAPPED at what the token ceilings would have cost at the pinned price, so a low-priced node
// cannot zero its claim to be paid the higher platform byte rate. And the anomaly is disputed.
func TestTokenPricedGrantWithNoTokenClaimFallsBackToBytes(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "1000000") // byte tariff: 1 credit per byte out
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	b, srv := towerTestBroker(t)
	b.feeRate = 0.30
	op := signedInOperator(t, b, "tower-op")
	tw := enrolledTower(t, b, op.login)
	stPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	stationOwner := hexOf(stPub)
	require.NoError(t, b.db.BindOwner(store.Owner{
		Pubkey: stationOwner, Login: "station-op2", Email: "sp2@x.test", EmailVerifiedAt: time.Now().Unix(),
	}))
	stationPriv := attachStation(t, b, "st-1", tw.id, stationOwner)

	cpub := issuedEdgeGrantPriced(t, b, "att-1", tw.id, "st-1", 0, 2_000_000)
	consumerWallet := bindEdgeConsumer(t, b, cpub)
	_, _ = b.db.AddCredits(consumerWallet, 1000)
	held, err := b.db.HoldFor(consumerWallet, "att-1", 100)
	require.NoError(t, err)
	require.True(t, held)

	// ZERO token claim: the byte tariff says 40 bytes x 1 credit/byte = 40 credits, but the
	// token-ceiling cap says (1<<20 tokens) x $2/1M = ~2.097152 credits - the arbitrage cap
	// bills the LOWER figure, and flags the anomaly for audit.
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceiptTok(t, stationPriv, "att-1", "st-1", []byte("answer"),
			dispatch.Usage{In: 10, Out: 40}, dispatch.Usage{}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, true, out["disputed"], "a zero token claim on a priced grant with real bytes is audited")
	capCost := 2.097152 // tokenCostCredits(1<<20, 1<<20, 0, 2_000_000)
	bal, _ := b.db.PeekBalance(consumerWallet)
	require.InDelta(t, 1000-capCost, bal, 1e-6, "the byte fallback is capped at the token-ceiling cost")
}

// tokenCostCredits: micro-USD per 1M tokens -> credits, zero on any negative input.
func TestTokenCostCredits(t *testing.T) {
	require.InDelta(t, 1.0, tokenCostCredits(0, 500_000, 0, 2_000_000), 1e-12) // $2/1M x 0.5M
	require.InDelta(t, 0.0003, tokenCostCredits(0, 1000, 0, 300_000), 1e-12)   // $0.30/1M x 1k
	require.Zero(t, tokenCostCredits(-1, 10, 5, 5))
	require.Zero(t, tokenCostCredits(10, 10, -5, 5))
	require.Zero(t, tokenCostCredits(0, 0, 1_000_000, 1_000_000))
}
