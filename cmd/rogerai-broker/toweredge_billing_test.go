package main

// toweredge_billing_test.go covers the founder-approved Tower revenue share end to end: when
// edge traffic is PRICED, settling a relayed attempt charges the consumer and pays BOTH the
// serving Station's owner and the relaying Tower's operator through the shared wallet.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/store"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
)

func TestEdgeBillingPaysStationOwnerAndTowerOperator(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "1") // 1 credit per output byte
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
	consumerWallet := protocol.UserIDFromPubkey(hex.EncodeToString(cpub))
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
	// Tower operator earns cost*fee*towerRate = 50*0.3*0.10 = 1.5.
	sTw, _ := b.db.EarningSplitOf(towerAcct, now)
	require.InDelta(t, 1.5, sTw.Payable, 0.001, "tower operator earns 10% of the platform margin")
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

// With no edge price configured (the default), settling a relayed attempt bills nothing and
// mints no wallet lots - edge traffic stays free until billing is explicitly turned on.
func TestEdgeBillingIsDormantWhenUnpriced(t *testing.T) {
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
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "1")
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
	consumerWallet := protocol.UserIDFromPubkey(hex.EncodeToString(cpub))
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
	require.InDelta(t, 1.5, sTw.Payable, 0.001, "tower paid on the completion")
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
