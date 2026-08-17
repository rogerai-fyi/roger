package main

// towerwireattest_test.go covers the P8 wire attestation (spec: "The Tower's wire count is
// settlement evidence the audit arbitrates"): the Tower's count of the sealed bytes it
// relayed FLAGS a mismatching settlement (disputed + forced audit) but never moves money -
// a security review killed the clamp version, which let a consumer running its own tower
// attest tiny counts and buy near-free inference at an honest node's expense.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/audit"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
)

// A Station byte claim above the Tower's wire count means SOMEBODY is lying - the station
// inflating, or the tower attesting low. Core cannot tell which from here, so the settlement
// is disputed + force-audited (the transcript proves who lied) - and the MONEY is untouched:
// the receipt's figure stands, because the tower's word moves no money in either direction.
func TestWireMismatchFlagsButNeverMovesMoney(t *testing.T) {
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
	require.Equal(t, float64(4000), out["billable_out"], "the receipt's figure stands - the tower's word moves no money")
	require.Equal(t, true, out["disputed"], "a claim above the wire is a dispute for the audit to arbitrate")

	owed, err := b.tower.earnings.OwedTo(owner, time.Time{})
	require.NoError(t, err)
	require.Equal(t, int64(8000), owed.Accrued, "priced on the receipt: 4000 x 2 - a lying-low tower cannot underpay the node")

	// Force-audited, and the wanted row carries the wire counts so the audit CAN arbitrate.
	pending, err := b.tower.auditWanted.Pending(tw.id, time.Now())
	require.NoError(t, err)
	require.Len(t, pending, 1, "force-audited")
	require.Equal(t, int64(60), pending[0].WireOut, "the attested count rides to the audit")
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

// A wire count ABOVE the claim raises nothing and disputes nothing: sealed bytes exceed
// plaintext, so wire > claim is the ORDINARY honest shape.
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

// THE ARBITRATION: at audit the transcript proves the true plaintext lengths, and sealed
// bytes can never be smaller - so a Tower-attested wire count below the proven length is a
// physical impossibility. The Station's transcript passes; the TOWER eats the finding.
func TestAuditAttributesAnImpossibleWireCountToTheTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-w")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-w")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	req, resp := []byte("the prompt"), []byte("a fairly long real answer with real bytes in it")
	// The tower attested it relayed only 5 result bytes - impossible: the transcript below
	// proves the response was much longer, and the sealed form is longer still.
	require.NoError(t, b.tower.auditWanted.Want(audit.Wanted{
		TowerID: tw.id, AttemptID: "att-wire", StationID: "st-1",
		RequestDigest: digestLike(req), ResponseDigest: digestLike(resp),
		UsageIn: int64(len(req)), UsageOut: int64(len(resp)),
		WireIn: int64(len(req)) + 100, WireOut: 5,
		Deadline: time.Now().Add(time.Hour),
	}))
	obj, reqB64, respB64 := signedTranscript(t, stationPriv, "att-wire", req, resp)
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": "att-wire", "available": true,
		"transcript": obj, "request": reqB64, "response": respB64,
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, out["matched"], "the STATION's transcript passes - it told the truth")

	// The tower carries the finding: a strong-evidence outcome lands on ITS ledger.
	tally, err := b.tower.outcomes.Tally(tw.id, time.Time{})
	require.NoError(t, err)
	require.Equal(t, 1, tally.CanaryFail, "the impossible attestation is recorded against the tower")
}

// An off-sample (adaptive/forced) selection carries no retention promise: a Station that did
// not keep it produces a soft miss, not a quarantine-grade mismatch.
func TestOffSampleAuditMissIsSoft(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-s")
	attachStation(t, b, "st-1", tw.id, "owner-s")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	// Find an attempt id OUTSIDE the deterministic sample.
	offSample := ""
	for i := 0; i < 64; i++ {
		id := fmt.Sprintf("att-off-%d", i)
		if !auditSampled(id) {
			offSample = id
			break
		}
	}
	require.NotEmpty(t, offSample)
	require.NoError(t, b.tower.auditWanted.Want(audit.Wanted{
		TowerID: tw.id, AttemptID: offSample, StationID: "st-1",
		RequestDigest: "rq", ResponseDigest: "rs",
		Deadline: time.Now().Add(time.Hour),
	}))
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": offSample, "available": false,
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusOK, code)

	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateActive, got.State, "an off-sample miss never suspends an honest tower")
	tally, err := b.tower.outcomes.Tally(tw.id, time.Time{})
	require.NoError(t, err)
	require.Zero(t, tally.AuditMismatch, "no mismatch outcome for an off-sample miss")
}
