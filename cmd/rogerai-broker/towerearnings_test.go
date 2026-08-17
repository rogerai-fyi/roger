package main

// towerearnings_test.go covers the funding ledger's two ends on the real routes: an attempt
// that settles ACCRUES to the Station's owner, and the operator can READ what they are owed.
//
// Contract: features/tower/edge_dispatch.feature (the "what the operator is paid for" scenario).

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
)

// A settled attempt is money owed to the operator whose Station did the work. The amount is
// the reconciled BILLABLE usage priced at the configured rate - never the Tower's own count.
func TestSettlementAccruesToTheStationOwner(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_IN", "1")
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "2")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)

	consumerPriv := issuedAttempt(t, b, "att-1", tw.id, "st-1")
	response := []byte(`{"choices":[{"text":"hi"}]}`)
	code, out := consumerCall(t, srv, consumerPriv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack":        signedAck(t, consumerPriv, "att-1", response, dispatch.Usage{In: 10, Out: 90}),
	})
	require.Equal(t, http.StatusOK, code, out)

	// The Station claims MORE than the consumer saw; billable is held to the consumer's figure,
	// and the accrual must be priced on the billable figure, not the claim.
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1", response,
			dispatch.Usage{In: 10, Out: 100}),
	})
	require.NoError(t, err)
	var settled map[string]any
	code, _ = tw.call(t, srv, "/tower/edge/settle", body, &settled)
	require.Equal(t, http.StatusOK, code, settled)

	owed, err := b.tower.earnings.OwedTo(owner, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, owed.Attempts)
	// 10*1 (in) + 90*2 (billable out, NOT the claimed 100) = 190.
	require.Equal(t, int64(190), owed.Accrued, "priced on billable usage, not the Station's claim")
	require.Equal(t, int64(190), owed.Owed())
}

// A retried settlement is refused by the one-use claim, so it can never accrue twice. The
// ledger's own idempotency (parity_test) is the backstop; this is the handler-level guard.
func TestARetriedSettlementDoesNotAccrueTwice(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "5")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	issuedAttempt(t, b, "att-1", tw.id, "st-1")

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1", []byte("a"),
			dispatch.Usage{In: 0, Out: 4}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	code, _ = tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusConflict, code, "a second settlement is refused")

	owed, err := b.tower.earnings.OwedTo(owner, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, owed.Attempts)
	require.Equal(t, int64(20), owed.Accrued)
}

// The operator reads THE MONEY on the signed route - the same credits, held/payable/paid and
// relay-vs-serving split the website's Payouts page serves, scoped to the pubkey that signed.
// It must NOT answer from the policy-priced accrual trail: that surface once did, and the CLI
// and the dashboard disagreed about the same money.
func TestAnOperatorReadsWhatTheyAreOwed(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "3") // the trail's policy rate - NOT the money
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_IN", "0")
	t.Setenv("ROGERAI_TOWER_EDGE_PRICE_OUT", "0")
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	b, srv := towerTestBroker(t)
	b.feeRate = 0.30
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)

	// A priced attempt so there is real money: 200 tokens at $5,000/1M = 1.0 credit gross.
	// This operator owns BOTH the tower and the station, so they earn 70% + 10% = 0.80.
	cpub := issuedEdgeGrantPriced(t, b, "att-owed", tw.id, "st-1", 0, 5_000_000_000)
	consumerWallet := bindEdgeConsumer(t, b, cpub)
	_, err := b.db.AddCredits(consumerWallet, 100)
	require.NoError(t, err)
	held, err := b.db.HoldFor(consumerWallet, "att-owed", 10)
	require.NoError(t, err)
	require.True(t, held)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-owed",
		"receipt": signedReceiptTok(t, stationPriv, "att-owed", "st-1", make([]byte, 300),
			dispatch.Usage{In: 0, Out: 300}, dispatch.Usage{In: 0, Out: 200}),
	})
	require.NoError(t, err)
	var settled map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &settled)
	require.Equal(t, http.StatusOK, code, settled)

	var owed map[string]any
	code, raw := op.call(t, srv, http.MethodPost, "/tower/earnings/owed", map[string]any{}, &owed)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, "credits", owed["unit"], "the money is quoted in the website's unit, not the trail's micros")
	require.InDelta(t, 0.80, owed["payable"], 1e-6, "70%% serving + 10%% relaying of a 1.0-credit request")
	require.InDelta(t, 0.10, owed["from_relaying"], 1e-6)
	require.InDelta(t, 0.70, owed["from_serving"], 1e-6)
	require.Equal(t, float64(1), owed["attempts"], "the trail contributes COUNTS, not a balance")
	require.NotContains(t, owed, "accrued", "the policy-priced accrual is never quoted as a balance")

	// THE INVARIANT: this endpoint and the website's /payouts/earnings agree, because both
	// read the ledger the payout rail pays from.
	split, err := b.db.EarningSplitOf(owner, time.Now())
	require.NoError(t, err)
	require.InDelta(t, split.Payable, owed["payable"], 1e-9)
	require.InDelta(t, split.Paid, owed["paid"], 1e-9)
}

// A stranger cannot read the ledger, and nobody's balance leaks to an unauthenticated caller.
func TestReadingEarningsRequiresBeingSignedIn(t *testing.T) {
	_, srv := towerTestBroker(t)
	resp, err := http.Post(srv.URL+"/tower/earnings/owed", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// A bad rate string prices that side at zero rather than refusing settlement: pricing is an
// operations knob, and a typo must never wedge the settlement path.
func TestABadAccrualRateIsTreatedAsZero(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "not-a-number")
	require.Equal(t, int64(0), edgeAccrualMicros(0, 100))
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "-5")
	require.Equal(t, int64(0), edgeAccrualMicros(0, 100))
}
