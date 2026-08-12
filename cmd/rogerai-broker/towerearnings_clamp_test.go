package main

// towerearnings_clamp_test.go covers the money-integrity fixes from the security review:
// billable is bounded to the grant's authorized ceiling before it is accrued (so a Station
// cannot be paid for more than it was authorized to do), and the pricing arithmetic saturates
// rather than wraps.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
)

// issuedEdgeGrant mints a REAL Core-signed edge grant (so its ceiling is readable at
// settlement) with the given bounds, and records it. Returns the consumer key it is bound to.
func issuedEdgeGrant(t *testing.T, b *broker, attemptID, towerID, stationID string, maxIn, maxOut int64) ed25519.PublicKey {
	t.Helper()
	cpub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	apub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g, err := b.tower.dispatch.MintEdge(dispatch.EdgeTarget{
		TowerID: towerID, StationID: stationID, StationEpoch: 1, Model: "m", Modality: "text",
		RelayName: stationID + ".relay.example", MaxIn: maxIn, MaxOut: maxOut,
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

// A Station claiming more output than its grant authorized is paid only for the ceiling, and
// the over-claim is treated as a dispute and audited. This is the review's HIGH finding 1: on
// the no-ack path billable is the payee's own number, so the grant ceiling is what bounds it.
func TestBillableIsClampedToTheGrantCeilingAndDisputed(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "2")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	issuedEdgeGrant(t, b, "att-1", tw.id, "st-1", 1000, 50) // ceiling out = 50

	// The Station signs a receipt claiming 5000 out - a hundred times its authorization.
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1", []byte("answer"),
			dispatch.Usage{In: 10, Out: 5000}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, float64(50), out["billable_out"], "clamped to the grant ceiling")
	require.Equal(t, true, out["disputed"], "an over-claim is a dispute")

	// Accrued on the CLAMPED figure: 50 * 2 = 100, not 5000 * 2.
	owed, err := b.tower.earnings.OwedTo(owner, time.Time{})
	require.NoError(t, err)
	require.Equal(t, int64(100), owed.Accrued, "priced on the ceiling, not the claim")

	// And force-audited regardless of the sample.
	pending, err := b.tower.auditWanted.Pending(tw.id, time.Now())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "att-1", pending[0].AttemptID)
}

// A claim within the ceiling is untouched: the clamp only ever caps an over-claim.
func TestBillableWithinTheCeilingIsNotClamped(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "1")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	issuedEdgeGrant(t, b, "att-1", tw.id, "st-1", 1000, 500)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-1", []byte("answer"),
			dispatch.Usage{In: 10, Out: 40}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, float64(40), out["billable_out"])
	require.NotEqual(t, true, out["disputed"])
}

// The pricing arithmetic saturates instead of wrapping to a small wrong number an adversary
// could steer (review finding 3).
func TestAccrualPricingSaturates(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "9223372036854775807") // MaxInt64
	require.Equal(t, int64(math.MaxInt64), edgeAccrualMicros(0, 1000))
	require.Equal(t, int64(math.MaxInt64), edgeAccrualMicros(1000, 1000))
	require.Equal(t, int64(0), edgeAccrualMicros(0, 0))
}
