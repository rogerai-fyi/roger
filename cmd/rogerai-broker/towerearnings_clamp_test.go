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

// The station-substitution attack a review found: a Tower running two attached Stations must
// not settle an attempt granted for Station Z with a receipt its own Station Y signed. That
// would close the attempt against Y, accrue Y's owner, and slip the grant ceiling (which names
// Z). The settlement is bound to the granted Station and refused.
func TestASettlementForTheWrongStationIsRefused(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "2")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	// Two Stations, same owner, same Tower - the ordinary multi-GPU case.
	attachStation(t, b, "st-aa", tw.id, owner)
	yPriv := attachStation(t, b, "st-bb", tw.id, owner)
	// The attempt was granted for Z, with a low ceiling.
	issuedEdgeGrant(t, b, "att-1", tw.id, "st-aa", 1000, 50)

	// The operator settles it with Y's receipt, over the ceiling.
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-bb", "attempt_id": "att-1",
		"receipt": signedReceipt(t, yPriv, "att-1", "st-bb", []byte("answer"),
			dispatch.Usage{In: 10, Out: 5000}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusNotFound, code, "a receipt for the wrong Station cannot settle")

	// Nothing accrued to anyone, and the attempt is still open (not consumed by the bad settle).
	owed, err := b.tower.earnings.OwedTo(owner, time.Time{})
	require.NoError(t, err)
	require.Equal(t, int64(0), owed.Accrued)
}

// A stored attempt whose grant will not yield its ceiling (our own bug, or a tampered record)
// must not settle UNFLAGGED on an unclamped figure: it still settles - we do not trap an honest
// operator's pay behind our fault - but it is marked disputed and force-audited so a human sees
// it. A money bound that cannot be checked gets a second look, never a silent pass.
func TestAnUnreadableGrantSettlesButIsFlaggedForAudit(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "1")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-aa", tw.id, owner)
	// A record with a GARBAGE grant - EdgeGrantCeiling cannot verify it.
	require.NoError(t, b.tower.dispatch.Store().Put(dispatch.Record{
		AttemptID: "att-1", JobID: "job-1", TowerID: tw.id, StationID: "st-aa",
		Model: "m", Modality: "text", Nonce: "n-1", Deadline: time.Now().Add(time.Hour),
		Grant: []byte("not a signed grant"), State: dispatch.StateIssued,
	}))
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-aa", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-aa", []byte("answer"),
			dispatch.Usage{In: 1, Out: 7}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, true, out["disputed"], "an uncheckable bound is flagged, not passed")

	pending, err := b.tower.auditWanted.Pending(tw.id, time.Now())
	require.NoError(t, err)
	require.Len(t, pending, 1, "force-audited")
}
