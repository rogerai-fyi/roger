package main

// towerwireattest_test.go covers the P8 wire attestation (spec: "The Tower's wire count
// bounds what a Station can bill"): the Tower's own count of the sealed bytes it relayed is
// an upper bound on the Station's byte claim - clamp-only, so it can lower a bill, never
// raise one.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
)

// A Station claiming more output bytes than the Tower relayed is provably inflating; the
// claim is clamped to the wire count and the settlement is disputed + audited.
func TestWireCountClampsAnInflatedByteClaim(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "2")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	issuedEdgeGrant(t, b, "att-w1", tw.id, "st-1", 1000, 5000) // roomy grant ceiling

	// The Station signs 4000 out (inside the grant), but the Tower only relayed 60 bytes.
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-w1",
		"receipt": signedReceipt(t, stationPriv, "att-w1", "st-1", []byte("answer"),
			dispatch.Usage{In: 10, Out: 4000}),
		"wire_in": 10, "wire_out": 60,
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, float64(60), out["billable_out"], "clamped to the tower's wire count")
	require.Equal(t, true, out["disputed"], "a claim above the wire is a dispute")

	owed, err := b.tower.earnings.OwedTo(owner, time.Time{})
	require.NoError(t, err)
	require.Equal(t, int64(120), owed.Accrued, "priced on the wire count: 60 x 2")

	pending, err := b.tower.auditWanted.Pending(tw.id, time.Now())
	require.NoError(t, err)
	require.Len(t, pending, 1, "force-audited")
}

// An absent (or zero) wire count changes nothing - the attestation is strictly optional and
// strictly downward, so an older tower binary settles exactly as before.
func TestAbsentWireCountChangesNothing(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "1")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	issuedEdgeGrant(t, b, "att-w2", tw.id, "st-1", 1000, 500)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-w2",
		"receipt": signedReceipt(t, stationPriv, "att-w2", "st-1", []byte("answer"),
			dispatch.Usage{In: 10, Out: 40}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, float64(40), out["billable_out"], "untouched without an attestation")
	require.NotEqual(t, true, out["disputed"])
	_ = owner
}

// A wire count ABOVE the claim raises nothing: the attestation is an upper bound only, so a
// tower cannot inflate a bill by overstating what it carried.
func TestWireCountCannotRaiseABill(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "1")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	issuedEdgeGrant(t, b, "att-w3", tw.id, "st-1", 1000, 500)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-w3",
		"receipt": signedReceipt(t, stationPriv, "att-w3", "st-1", []byte("answer"),
			dispatch.Usage{In: 10, Out: 40}),
		"wire_in": 999999, "wire_out": 999999,
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, float64(40), out["billable_out"], "the claim stands; the tower's word never raises it")
	require.NotEqual(t, true, out["disputed"])
	_ = owner
}
