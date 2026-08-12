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

// The operator reads their own balance on the signed route, scoped to the pubkey that signed.
func TestAnOperatorReadsWhatTheyAreOwed(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "3")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	issuedAttempt(t, b, "att-1", tw.id, "st-1")

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1", []byte("a"),
			dispatch.Usage{In: 0, Out: 8}),
	})
	require.NoError(t, err)
	var settled map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &settled)
	require.Equal(t, http.StatusOK, code, settled)

	var owed map[string]any
	code, raw := op.call(t, srv, http.MethodPost, "/tower/earnings/owed", map[string]any{}, &owed)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, float64(24), owed["owed"], raw)
	require.Equal(t, float64(24), owed["accrued"], raw)
	require.Equal(t, float64(1), owed["attempts"], raw)
	require.Equal(t, "micros", owed["unit"])
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
