package main

// towerearnings_clamp_test.go covers the money-integrity fixes from the security review:
// billable is bounded to the grant's authorized ceiling before it is accrued (so a Station
// cannot be paid for more than it was authorized to do), and the pricing arithmetic saturates
// rather than wraps.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/link"
	"rogerai.fm/roger/v5/internal/towercore/audit"
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

// Review finding 2: a consumer that acks the CORRECT response digest but a lower usage (e.g. 0)
// must not silently zero an honest operator's pay marked "corroborated/clean". Usage is
// byte-exact, so matching digests force matching usage; the contradiction is flagged and audited.
func TestAConsumerCannotSilentlyZeroThePayWithAMatchingDigest(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "1")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-aa", tw.id, owner)
	consumerPriv := issuedAttempt(t, b, "att-1", tw.id, "st-aa")
	response := []byte("a real forty-ish byte answer from the model")

	// The consumer signs the TRUE response digest but claims usage 0 - a provable lie.
	code, _ := consumerCall(t, srv, consumerPriv, "/tower/edge/ack", map[string]any{
		"attempt_id": "att-1",
		"ack":        signedAck(t, consumerPriv, "att-1", response, dispatch.Usage{In: 0, Out: 0}),
	})
	require.Equal(t, http.StatusOK, code)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-aa", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-aa", response,
			dispatch.Usage{In: 5, Out: int64(len(response))}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ = tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, true, out["disputed"], "a usage-vs-digest contradiction is flagged, not clean")

	// And it was force-audited (the audit has the bytes and can attribute the lie).
	pending, err := b.tower.auditWanted.Pending(tw.id, time.Now())
	require.NoError(t, err)
	require.Len(t, pending, 1)
}

// Review finding 3: a settlement whose one-use claim landed but whose commit was interrupted
// (transient store fault or crash, leaving the attempt stranded "claimed") must be recoverable
// by a retry, not permanently 409'd with the operator's pay lost.
func TestAStrandedClaimCanBeReDriven(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "1")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-aa", tw.id, owner)
	issuedAttempt(t, b, "att-1", tw.id, "st-aa")

	// Simulate an interrupted prior settle: the attempt is claimed but never settled.
	_, cerr := b.tower.dispatch.Store().ClaimByID("att-1", tw.id, time.Now())
	require.NoError(t, cerr)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-aa", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-aa", []byte("answer"),
			dispatch.Usage{In: 1, Out: 6}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, "a stranded claim re-drives to a real settlement")
	require.Equal(t, float64(6), out["billable_out"])

	owed, err := b.tower.earnings.OwedTo(owner, time.Time{})
	require.NoError(t, err)
	require.Equal(t, int64(6), owed.Accrued, "the recovered settlement accrues exactly once")

	// A further retry is now a genuine double-settle and is refused.
	code, _ = tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusConflict, code)
}

// Review finding 1 backstop: on the unacknowledged path the billable usage is the Station's own
// signed byte count, bounded only by the grant ceiling. The sampled audit re-derives the true
// length from the transcript bytes (which must hash to the signed digest) and holds the claim to
// it: a Station that billed more bytes than it signed for is caught as a usage misreport and
// quarantined - attributable, because it signed both the receipt's usage and the transcript.
func TestAuditCatchesAUsageMisreport(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := enrolledTower(t, b, "owner-1")
	stationPriv := attachStation(t, b, "st-1", tw.id, "owner-1")
	require.NoError(t, b.tower.registry.Transition(tw.id, admit.StateActive))

	req, resp := []byte("the prompt"), []byte("a short real answer")
	// Settlement recorded the digests of the REAL bytes but a receipt that CLAIMED far more
	// output than those bytes are long - the inflation an unacknowledged attempt could hide.
	require.NoError(t, b.tower.auditWanted.Want(audit.Wanted{
		TowerID: tw.id, AttemptID: "att-1", StationID: "st-1",
		RequestDigest: digestLike(req), ResponseDigest: digestLike(resp),
		UsageIn: int64(len(req)), UsageOut: 999999,
		Deadline: time.Now().Add(time.Hour),
	}))
	// The transcript carries the real bytes (they hash to the signed digest) - so the digests
	// match, but their length contradicts the 999999 the receipt billed.
	obj, reqB64, respB64 := signedTranscript(t, stationPriv, "att-1", req, resp)
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "attempt_id": "att-1", "available": true,
		"transcript": obj, "request": reqB64, "response": respB64,
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/audit/transcript", body, &out)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, false, out["matched"], "a usage misreport fails the audit")

	got, _ := b.tower.registry.Get(tw.id)
	require.Equal(t, admit.StateSuspended, got.State, "an audit mismatch takes the Tower off (suspended)")
}

// Self-dealing defence, end to end: an operator who routes their OWN traffic through their OWN
// Station (consumer account == Station owner) earns nothing. The attempt still settles and is
// recorded - the usage is evidence - but it is excluded from what is owed and surfaced as
// self-dealt. This is the account-level first line against wash-trading a revenue share.
func TestSelfDealingEarnsNothing(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "10")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	opPub := op.priv.Public().(ed25519.PublicKey) // the operator's own account key
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-aa", tw.id, owner)

	// An attempt whose CONSUMER is the operator's own account, served by the operator's Station.
	g, err := b.tower.dispatch.MintEdge(dispatch.EdgeTarget{
		TowerID: tw.id, StationID: "st-aa", StationEpoch: 1, Model: "m", Modality: "text",
		RelayName: "st-aa.relay.example", MaxIn: 1000, MaxOut: 1000,
		AssertionKey: opPub, ConsumerKey: opPub,
	})
	require.NoError(t, err)
	require.NoError(t, b.tower.dispatch.Store().Put(dispatch.Record{
		AttemptID: "att-1", JobID: g.JobID, TowerID: tw.id, StationID: "st-aa",
		StationEpoch: 1, Model: "m", Modality: "text", Nonce: g.Nonce,
		Deadline: time.Now().Add(time.Hour), Grant: g.Signed, ConsumerKey: opPub,
		State: dispatch.StateIssued,
	}))

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-aa", "attempt_id": "att-1",
		"receipt": signedReceipt(t, stationPriv, "att-1", "st-aa", []byte("answer"),
			dispatch.Usage{In: 5, Out: 50}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)

	owed, err := b.tower.earnings.OwedTo(owner, time.Time{})
	require.NoError(t, err)
	require.Equal(t, int64(0), owed.Owed(), "self-dealing earns nothing")
	require.Equal(t, int64(500), owed.SelfDealt, "but it is recorded and surfaced (50*10)")
	require.Equal(t, 1, owed.Attempts)
}

// issuedEdgeGrantTok mints an edge grant carrying TOKEN ceilings alongside the byte ceilings,
// for the Option C per-token clamp tests.
func issuedEdgeGrantTok(t *testing.T, b *broker, attemptID, towerID, stationID string, maxIn, maxOut, maxTokIn, maxTokOut int64) ed25519.PublicKey {
	t.Helper()
	cpub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	apub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g, err := b.tower.dispatch.MintEdge(dispatch.EdgeTarget{
		TowerID: towerID, StationID: stationID, StationEpoch: 1, Model: "m", Modality: "text",
		RelayName: stationID + ".relay.example", MaxIn: maxIn, MaxOut: maxOut,
		MaxTokIn: maxTokIn, MaxTokOut: maxTokOut, AssertionKey: apub, ConsumerKey: cpub,
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

// signedReceiptTok signs a receipt carrying a TOKEN claim alongside the byte usage.
func signedReceiptTok(t *testing.T, priv ed25519.PrivateKey, attemptID, stationID string,
	response []byte, u, tok dispatch.Usage) string {
	t.Helper()
	rec, err := dispatch.SignReceipt(priv, link.PublicNetwork,
		dispatch.Grant{AttemptID: attemptID, StationID: stationID}, []byte("req-"+attemptID), response, u, tok)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(rec.Signed)
}

// Option C: the TOKEN claim a node is paid on is clamped to the grant's token ceiling at
// settle, exactly as the byte claim is. The byte usage is kept within its own ceiling here so
// the dispute is attributable to the token over-claim alone.
func TestEdgeSettleClampsTheTokenClaimToTheGrantCeiling(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	issuedEdgeGrantTok(t, b, "att-1", tw.id, "st-1", 8<<20, 5000, 1<<20, 50) // token out ceiling 50

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		// byte out=40 (within its 5000 ceiling, no byte dispute); token out=5000 (over the 50 ceiling).
		"receipt": signedReceiptTok(t, stationPriv, "att-1", "st-1", []byte("answer"),
			dispatch.Usage{In: 10, Out: 40}, dispatch.Usage{In: 5, Out: 5000}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, float64(40), out["billable_out"], "the byte figure within its ceiling is untouched")
	require.Equal(t, true, out["disputed"], "a token over-claim is a dispute")
	pending, err := b.tower.auditWanted.Pending(tw.id, time.Now())
	require.NoError(t, err)
	require.Len(t, pending, 1, "and force-audited regardless of the sample")
}

// A token claim within the ceiling is not clamped and not disputed.
func TestEdgeSettleLeavesAnInBoundsTokenClaimAlone(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	issuedEdgeGrantTok(t, b, "att-1", tw.id, "st-1", 8<<20, 5000, 1<<20, 500)

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		// token out 400 <= byte out 500 (tokens<=bytes) AND <= the 500 token ceiling: in bounds.
		"receipt": signedReceiptTok(t, stationPriv, "att-1", "st-1", []byte("answer"),
			dispatch.Usage{In: 10, Out: 500}, dispatch.Usage{In: 5, Out: 400}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.NotEqual(t, true, out["disputed"], "an in-bounds token claim is not disputed")
}

// A grant with NO token ceiling (0/0) must not clamp or dispute a token claim at settle - 0
// means "not token-bounded", so the byte cap + audit govern, exactly as an old byte-only grant.
func TestEdgeSettleLeavesTokensAloneWhenGrantHasNoTokenCeiling(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	issuedEdgeGrantTok(t, b, "att-1", tw.id, "st-1", 8<<20, 5000, 0, 0) // NO token ceiling

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		// No token ceiling, and the token claim stays within the byte claim (tokens<=bytes still
		// governs): the CEILING clamp is skipped, so this is not disputed.
		"receipt": signedReceiptTok(t, stationPriv, "att-1", "st-1", []byte("answer"),
			dispatch.Usage{In: 5000, Out: 5000}, dispatch.Usage{In: 4000, Out: 5000}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.NotEqual(t, true, out["disputed"], "a 0 token ceiling does not clamp or dispute an in-bounds token claim")
}

// The token INPUT ceiling clamps symmetrically with output: an over-claim on tok_in alone is a
// dispute (byte usage kept within its ceiling to isolate the token-input clamp).
func TestEdgeSettleClampsTheInputTokenClaimToTheGrantCeiling(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	issuedEdgeGrantTok(t, b, "att-1", tw.id, "st-1", 8<<20, 5000, 50, 1<<20) // token IN ceiling 50

	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		"receipt": signedReceiptTok(t, stationPriv, "att-1", "st-1", []byte("answer"),
			dispatch.Usage{In: 10, Out: 40}, dispatch.Usage{In: 5000, Out: 9}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, true, out["disputed"], "an input token over-claim is a dispute")
}

// tokens <= bytes is enforced with data Core already holds: a token claim exceeding the bytes
// actually served is provably inflated (a token is >= 1 byte), so it is clamped and disputed
// even with a generous token ceiling and no Tower attestation - the attestation-free bound that
// lets token pricing land without waiting on the full byte-attestation.
func TestEdgeSettleClampsTokensToTheByteFigure(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	issuedEdgeGrantTok(t, b, "att-1", tw.id, "st-1", 8<<20, 5000, 1<<20, 1<<20) // roomy ceilings
	body, err := json.Marshal(map[string]any{
		"tower_id": tw.id, "station_id": "st-1", "attempt_id": "att-1",
		// byte out=40 (within its ceiling); token out=100 > 40 bytes: provably inflated.
		"receipt": signedReceiptTok(t, stationPriv, "att-1", "st-1", []byte("answer"),
			dispatch.Usage{In: 10, Out: 40}, dispatch.Usage{In: 5, Out: 100}),
	})
	require.NoError(t, err)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, true, out["disputed"], "a token claim above the bytes served is a dispute")
}
