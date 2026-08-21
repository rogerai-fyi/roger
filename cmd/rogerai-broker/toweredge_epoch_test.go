package main

// toweredge_epoch_test.go is about ONE integer that was written down four times and read zero.
//
// dispatch.Record.StationEpoch has been minted from the attachment, signed into the grant,
// stored on the dispatch row and carried across instances since the dispatch store existed,
// under a comment saying it "fences a rehome: work granted under the old origin cannot be
// completed after the move". Nothing compared it to anything. These tests are the comparison,
// and they are written against the placement model the founder settled on: a Station keeps a
// sticky binding to one relay, Core may move it only while the Station is idle or its relay is
// bad, and work caught by a move is a FAILED DELIVERY - not a payment to be re-attributed.
//
// The two directions get different answers and that is the whole point of the suite:
//   - the attachment MOVED PAST the grant: permanent (410), because the epoch only ever goes up
//     and no retry can un-supersede a placement;
//   - the attachment is BEHIND the grant: transient (503), because that is the shape of a read
//     that has not caught up, and a 4xx there would let towerjoin drop an honest receipt from
//     its durable spool forever.
//
// Contract: features/tower/edge_dispatch.feature, features/tower/operator_revenue_share.feature.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/attach"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

// stationEpochOf reads the epoch Core currently records for a Station - the value the fence
// compares a grant against.
func stationEpochOf(t *testing.T, b *broker, stationID string) int64 {
	t.Helper()
	at, ok, err := b.tower.stations.Station(stationID)
	require.NoError(t, err)
	require.True(t, ok, "the Station must be attached for its epoch to mean anything")
	return at.Epoch
}

// reviveStation advances a Station's epoch THROUGH THE ONLY WRITER THAT HAS EVER SET ONE.
//
// It puts the attachment to sleep and lets the same machine - same assertion key, same session
// key, same owner, same origin kind - come back, which is the revival branch of
// attach.Registry.Admit and the one place in the tree that assigns revived.Epoch+1. Poking a
// number into the store instead would test the fence against a state Core cannot produce, and
// the whole reason this fence is being built is that a value nobody produced is a value nobody
// checked.
func reviveStation(t *testing.T, b *broker, stationID, towerID, owner string) int64 {
	t.Helper()
	before, ok, err := b.tower.stations.Station(stationID)
	require.NoError(t, err)
	require.True(t, ok)

	// Dormant is what DetachIdle writes, and it is the one precondition a revival needs.
	moved, err := b.tower.stationStore.SetState(stationID, attach.StateDormant)
	require.NoError(t, err)
	require.True(t, moved)

	// UNIQUE PER REVIVAL, and the reason is worse than the one first written here.
	//
	// The first explanation was that a reused id would take Admit's `replay` branch and hand
	// back the previous revival's attachment. It does not: PutAuthorization is an overwrite, so
	// the second call finds the invitation UNCONSUMED and never reaches replay. What fires is
	// checkBindings' racer short-circuit - `existing.AuthID == authID`, written for a caller
	// retrying after a lost reply - which returns an EMPTY revived attachment. Admit then
	// carries nothing forward and writes `Epoch: 1` over a Station sitting at 2: an epoch
	// REGRESSION, which no writer in the tree is supposed to be able to produce and which the
	// settlement fence's permanent 410 is licensed by the absence of.
	//
	// Both stores now refuse a write that fails to raise the epoch
	// (TestParityAnAttachmentsEpochNeverGoesBackwards), so a reused id here fails loudly instead
	// of silently testing a Station that moved once. Keeping the ids unique is still the right
	// fixture: this helper is supposed to be a revival, not a probe of the guard.
	// (Found by TestTheMovedFenceLogsFieldsAnAggregationCanCount, which needs a gap above 1.)
	authID := "auth-revive-" + stationID + "-" + strconv.FormatInt(before.Epoch, 10)
	auth, secret, err := attach.NewInvite(attach.Authorization{
		ID: authID, Network: link.PublicNetwork, StationID: stationID, Owner: owner,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: before.AssertionKey, SessionKey: before.SessionKey,
	}, time.Hour, time.Now())
	require.NoError(t, err)
	require.NoError(t, b.tower.stationStore.PutAuthorization(auth))
	woke, err := b.tower.stations.Admit(attach.Proof{
		AuthID: authID, Secret: secret,
		Network: link.PublicNetwork, StationID: stationID, Owner: owner,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: before.AssertionKey, SessionKey: before.SessionKey,
	})
	require.NoError(t, err)
	_, err = b.tower.stations.Promote(stationID)
	require.NoError(t, err)
	require.Equal(t, before.Epoch+1, woke.Epoch, "a revival is the epoch's only increment")
	return woke.Epoch
}

// issuedAttemptAtEpoch is issuedAttempt with the placement epoch spelled out, so a test can
// write down the exact disagreement it wants. The grant and the row carry the SAME value,
// because openEdgeAttempt copies one onto the other and a fixture where they differ is a
// fixture testing a state production cannot reach.
func issuedAttemptAtEpoch(t *testing.T, b *broker, attemptID, towerID, stationID string,
	epoch int64) ed25519.PrivateKey {
	t.Helper()
	return issuedAttemptUntil(t, b, attemptID, towerID, stationID, epoch, time.Now().Add(time.Hour))
}

// issuedAttemptUntil is the same fixture with the attempt's EXECUTION deadline spelled out.
//
// THE RECORD'S DEADLINE IS DERIVED FROM IT, not written beside it, because that is the one
// relationship production maintains and the fixture used to bypass: openEdgeAttempt stores
// `g.Deadline.Add(edgeSettleGrace())`, so the row's deadline is the EVIDENCE ceiling and the
// execution window closed a settle-grace earlier. A fixture that sets the row's deadline
// directly cannot tell the two apart, which is exactly why a field reading the wrong one of
// them went unnoticed under a test that asserted the field was present.
func issuedAttemptUntil(t *testing.T, b *broker, attemptID, towerID, stationID string,
	epoch int64, exec time.Time) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	apub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	g, err := b.tower.dispatch.MintEdge(dispatch.EdgeTarget{
		TowerID: towerID, StationID: stationID, StationEpoch: epoch, Model: "m",
		Modality: "text", RelayName: stationID + ".relay.example",
		MaxIn: 8 << 20, MaxOut: 8 << 20, AssertionKey: apub, ConsumerKey: pub,
	})
	require.NoError(t, err)
	require.NoError(t, b.tower.dispatch.Store().Put(dispatch.Record{
		AttemptID: attemptID, JobID: "job-" + attemptID, TowerID: towerID,
		StationID: stationID, StationEpoch: epoch, Model: "m", Modality: "text",
		Nonce: "n-" + attemptID, Deadline: exec.Add(edgeSettleGrace()), Grant: g.Signed,
		ConsumerKey: pub, State: dispatch.StateIssued,
	}))
	bindEdgeConsumer(t, b, pub)
	return priv
}

// settleBody is the tower-signed settlement a courier forwards.
func settleBody(t *testing.T, towerID, stationID, attemptID string,
	stationPriv ed25519.PrivateKey, out int64) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"tower_id": towerID, "station_id": stationID, "attempt_id": attemptID,
		"receipt": signedReceipt(t, stationPriv, attemptID, stationID, []byte("a"),
			dispatch.Usage{In: 0, Out: out}),
	})
	require.NoError(t, err)
	return body
}

// attemptState reports what the dispatch store says an attempt IS - not what it is not. The
// refusals below are only worth anything if the attempt is still sitting in `issued`; asserting
// "not settled" would pass just as happily against `claimed`, which is the half-consumed state
// a refusal must never leave behind.
func attemptState(t *testing.T, b *broker, attemptID string) string {
	t.Helper()
	rec, ok, err := b.tower.dispatch.Store().Get(attemptID)
	require.NoError(t, err)
	require.True(t, ok)
	return rec.State
}

// A PLACEMENT THAT MOVED VOIDS THE WORK IT MOVED OUT FROM UNDER, PERMANENTLY AND LOUDLY.
//
// The Station is real, its receipt is real and correctly signed by the key Core recorded, and
// the settling Tower is the one the grant named. The only thing wrong is that the attachment
// has been re-placed since the grant was minted. Before this fence existed every assertion here
// answered 200 and the operator was paid for work served under a placement that no longer
// existed - which is the exact behaviour the milestone that makes placement mobile cannot ship
// on top of.
func TestASettlementUnderASupersededPlacementIsVoidAndPaysNobody(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "5")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	require.Equal(t, int64(1), stationEpochOf(t, b, "st-1"))

	// Authorized against the placement as it stood, then the placement moves.
	issuedAttemptAtEpoch(t, b, "att-stale", tw.id, "st-1", 1)
	require.Equal(t, int64(2), reviveStation(t, b, "st-1", tw.id, owner))

	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle",
		settleBody(t, tw.id, "st-1", "att-stale", stationPriv, 4), &out)
	// 410 and not 503: the epoch only ever goes up, so no retry can un-supersede this grant,
	// and towerjoin.SettleEdgeReceipt turning this into ErrSettlePermanent is what gets the
	// operator a named abandonment on their own console instead of a silent expiry. It is only
	// safe because the consumer's hold is released by the orphan sweep, which reads no receipt.
	require.Equal(t, http.StatusGone, code, out)

	// NOTHING WAS CONSUMED. Still `issued`, so a repair - if one were ever possible - is not
	// blocked by a half-taken one-use claim.
	require.Equal(t, dispatch.StateIssued, attemptState(t, b, "att-stale"))
	owed, err := b.tower.earnings.OwedTo(owner, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 0, owed.Attempts, "a superseded placement pays nobody")
	require.Equal(t, int64(0), owed.Accrued)

	// AND THE STATION IS NOT BLACKLISTED - the refusal is about the grant's placement, not
	// about the machine. Work authorized against the placement it is at NOW settles normally.
	issuedAttemptAtEpoch(t, b, "att-fresh", tw.id, "st-1", 2)
	code, _ = tw.call(t, srv, "/tower/edge/settle",
		settleBody(t, tw.id, "st-1", "att-fresh", stationPriv, 4), &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, dispatch.StateSettled, attemptState(t, b, "att-fresh"))
	owed, err = b.tower.earnings.OwedTo(owner, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, owed.Attempts)
	require.Equal(t, int64(20), owed.Accrued)
}

// AN ATTACHMENT BEHIND THE GRANT IS A READ THAT HAS NOT CAUGHT UP, AND MUST NEVER BE PERMANENT.
//
// No writer in the tree lowers an epoch, so "the grant is ahead of the attachment" is not a
// state anybody arranged - it is Core's own view lagging or restored. The distinction is worth
// a whole branch because getting it wrong costs an honest operator their pay: any 4xx but 409
// makes towerjoin drop the receipt from a spool that survives restarts, so answering the moved
// case's 410 here would delete the money for a replication delay. The retry has to heal it, and
// this test heals it and watches the money land.
func TestAnAttachmentBehindItsGrantRefusesTransientlyAndTheRetryPays(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "5")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)

	// The attempt was authorized against epoch 2; the attachment in front of us still reads 1.
	issuedAttemptAtEpoch(t, b, "att-lag", tw.id, "st-1", 2)
	require.Equal(t, int64(1), stationEpochOf(t, b, "st-1"))

	body := settleBody(t, tw.id, "st-1", "att-lag", stationPriv, 4)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusServiceUnavailable, code, out)
	require.Equal(t, dispatch.StateIssued, attemptState(t, b, "att-lag"))
	owed, err := b.tower.earnings.OwedTo(owner, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 0, owed.Attempts)

	// The lagging read catches up, the courier re-forwards THE SAME RECEIPT, and the operator
	// is paid. This is the property the status code buys and the only way to prove the refusal
	// consumed nothing: a claim taken on the first pass would refuse the second.
	require.Equal(t, int64(2), reviveStation(t, b, "st-1", tw.id, owner))
	code, _ = tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, dispatch.StateSettled, attemptState(t, b, "att-lag"))
	owed, err = b.tower.earnings.OwedTo(owner, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, owed.Attempts)
	require.Equal(t, int64(20), owed.Accrued)
}

// THE ROLLOUT CASE: A GRANT THAT STATES NO EPOCH IS NOT A GRANT THAT DISAGREES.
//
// StationEpoch is an int64 and its zero value is "nobody wrote one here", never "epoch zero".
// A fence that read 0 as a number would have refused every attempt in flight at deploy time and
// every attempt minted by an older instance during a rolling restart - the entire fleet, on the
// release that added a check nothing had been failing. This test is the guard on that, and it
// is the one test here that also passed before the fence existed: its job is to fail the day
// somebody tightens the comparison without noticing what zero means.
func TestAGrantThatStatesNoPlacementEpochStillSettles(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "5")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)

	issuedAttemptAtEpoch(t, b, "att-old", tw.id, "st-1", 0)
	require.Equal(t, int64(1), stationEpochOf(t, b, "st-1"), "the attachment does state one")

	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle",
		settleBody(t, tw.id, "st-1", "att-old", stationPriv, 4), &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, dispatch.StateSettled, attemptState(t, b, "att-old"))
	owed, err := b.tower.earnings.OwedTo(owner, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, owed.Attempts)
}

// The verdict itself, apart from the eight-hundred-line path that acts on it. Three of these
// four answers are refusals or exemptions that production cannot currently reach, so the table
// is where they are pinned down; the handler tests above prove the wiring for the two that can
// be staged end to end.
func TestStationEpochFenceNamesWhichWayThePlacementMoved(t *testing.T) {
	for _, tc := range []struct {
		name        string
		grant, live int64
		want        epochFenceVerdict
	}{
		{"the same placement", 3, 3, epochFenceAgrees},
		{"a fresh attachment's first placement", 1, 1, epochFenceAgrees},
		{"the grant states none", 0, 1, epochFenceUnstated},
		{"the attachment states none", 1, 0, epochFenceUnstated},
		{"neither states one", 0, 0, epochFenceUnstated},
		{"the placement moved on", 1, 2, epochFenceMoved},
		{"the placement moved on twice", 1, 3, epochFenceMoved},
		{"the attachment is behind the grant", 3, 1, epochFenceRegressed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, stationEpochFence(tc.grant, tc.live))
		})
	}
}

// THE FENCE GATES ENTRY INTO A SETTLEMENT, NOT THE COMPLETION OF ONE CORE ALREADY BEGAN.
//
// This handler is built to finish a half-committed settlement: the claim, the settle and the
// wallet capture are separate non-atomic steps, and a fault between them leaves an attempt the
// courier's next forward re-drives. If the placement then moved, refusing that re-drive would
// punish an operator for OUR interruption on the one path where Core has already judged the
// attempt payable under the placement it had - and would leave the money half-committed with
// the consumer's hold swept for work that really happened. So a record that is no longer
// `issued` is not re-judged, which is safe because this handler is the only thing in the tree
// that ever moves an edge attempt out of `issued`.
//
// The assertion is that the second forward answers exactly what it answers with no placement
// change at all (409, the already-settled refusal), and NOT the fence's 410.
func TestAPlacementChangeDoesNotReJudgeASettlementAlreadyBegun(t *testing.T) {
	t.Setenv("ROGERAI_TOWER_ACCRUAL_MICROS_OUT", "5")
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-1", tw.id, owner)
	issuedAttemptAtEpoch(t, b, "att-redrive", tw.id, "st-1", 1)

	body := settleBody(t, tw.id, "st-1", "att-redrive", stationPriv, 4)
	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, dispatch.StateSettled, attemptState(t, b, "att-redrive"))

	// The placement moves AFTER the settle committed - the window a fault between the settle and
	// the capture would leave open.
	require.Equal(t, int64(2), reviveStation(t, b, "st-1", tw.id, owner))

	code, _ = tw.call(t, srv, "/tower/edge/settle", body, &out)
	require.Equal(t, http.StatusConflict, code,
		"a re-drive is answered by the one-use claim, not re-judged by the placement fence")
	owed, err := b.tower.earnings.OwedTo(owner, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, owed.Attempts, "and it does not accrue twice either")
	require.Equal(t, int64(20), owed.Accrued)
}

// THE FENCE IS ALSO THE ONLY INSTRUMENT WE HAVE FOR THE COST OF PLACEMENT MOBILITY, AND AN
// INSTRUMENT NOBODY CAN AGGREGATE IS A PARAGRAPH.
//
// §6.3b accepts a race between "Core observed this Station idle" and "the move landed", on the
// argument that the fence makes the loser safe and that losing will be rare. Rare is a
// prediction, and the only way it stops being one is if every occurrence is countable across a
// fleet and over months. Every firing of the MOVED branch is exactly one request destroyed by a
// placement change, so this line is the count - and it is written in the key=value shape the
// rest of the broker's operational logging uses (probe.go, report.go, strikes.go) rather than in
// prose, so an aggregation slices it instead of regexing English.
//
// The assertions here are on the FIELDS, not on the wording. A future edit may say it better;
// what it may not do is drop a field an operator's query is grouping by, or stop naming which
// relay was superseded, and those are the failures this test exists to catch.
func TestTheMovedFenceLogsFieldsAnAggregationCanCount(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	owner := ownerPubkeyOf(t, b, op.login)
	tw := enrolledTower(t, b, op.login)
	stationPriv := attachStation(t, b, "st-log", tw.id, owner)

	issuedAttemptAtEpoch(t, b, "att-logged", tw.id, "st-log", 1)
	// TWO revivals, not one. epochs_skipped is the field that distinguishes an ordinary single
	// move catching an attempt from a Station being churned between relays while one attempt is
	// still alive, and the second is what §6.3c's signal hysteresis exists to prevent - so the test
	// has to produce a value the arithmetic could get wrong.
	require.Equal(t, int64(2), reviveStation(t, b, "st-log", tw.id, owner))
	require.Equal(t, int64(3), reviveStation(t, b, "st-log", tw.id, owner))

	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })

	var out map[string]any
	code, _ := tw.call(t, srv, "/tower/edge/settle",
		settleBody(t, tw.id, "st-log", "att-logged", stationPriv, 4), &out)
	require.Equal(t, http.StatusGone, code, out)

	line := logs.String()
	// The verdict token leads, so the three fence outcomes are separable without parsing the
	// sentence - MOVED is harm, REGRESSED is a lagging read, UNSTATED is the rollout arm's own
	// retirement notice, and summing them together would be meaningless.
	require.Contains(t, line, "edge fence MOVED ",
		"the moved fence must name its verdict in a token an aggregation can filter on")
	for _, field := range []string{
		"attempt=att-logged",
		"station=st-log",
		"tower=" + tw.id, // WHICH relay was superseded: the slice that makes a bad mover findable
		"grant_epoch=1",
		"attach_epoch=3",
		"epochs_skipped=2",   // one attempt, two moves - the churn signal
		"deadline_open=true", // this attempt's execution window IS open; see the test below
	} {
		require.Contains(t, line, field,
			"the moved fence dropped a field an aggregation groups by: %s", field)
	}
	// The node id joins the destroyed request back to the machine that served it, which is the
	// only identity probes, trust and the classic fabric are keyed by. Asserted as PRESENT
	// rather than as non-empty on purpose: a classic operator-invited Station carries no node id
	// by construction (it is reached through the Tower's signed inventory and never registers
	// with a broker - see DetachIdle's scope argument), so an empty value here is a true
	// statement about that population and not a dropped field. What must never happen is the key
	// disappearing, because then the two populations become indistinguishable in the aggregate.
	require.Contains(t, line, "node=", "the moved fence must state the node behind the Station")
}

// THE FIELD THAT WAS BUILT TO DRAW ONE DISTINCTION HAS TO ACTUALLY DRAW IT.
//
// `deadline_open` exists to separate the two harms hiding inside one 410: a consumer still
// waiting for an answer that is now never coming, and a courier whose spool caught up after the
// work had already finished - an operator unpaid, but nobody left hanging. Those want different
// responses from whoever reads the aggregation, which is the whole reason the field is there.
//
// IT COULD NOT DRAW IT. The line read `rec.Deadline`, and a dispatch record's deadline is the
// EVIDENCE ceiling: openEdgeAttempt writes the grant's deadline PLUS edgeSettleGrace(), under a
// comment that says so. The courier re-forwards every fifteen seconds inside a settlement
// window measured in minutes, so essentially every firing found that ceiling in the future and
// logged `deadline_open=true` - a constant, on the line whose entire thesis is that it is the
// instrument. The test that shipped with it asserted only that the KEY was present, and its
// fixture set the row's deadline directly, so the grant-plus-grace relationship the field
// depends on was not represented in the test at all.
//
// Both directions are asserted here, because a field that is always false is exactly as useless
// as one that is always true.
func TestTheMovedFenceSaysWhetherTheExecutionWindowWasOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		exec time.Duration // relative to now: the grant's own deadline
		want string
	}{
		{"a consumer is still waiting", time.Hour, "deadline_open=true"},
		// PAST THE GRANT'S DEADLINE BUT INSIDE THE SETTLE GRACE, which is not a corner case: it
		// is the ordinary shape of a late receipt, and the only reason the record outlives the
		// grant at all. The row's own deadline is still minutes in the future here, so a field
		// reading it says "true" and the distinction is lost.
		{"the spool caught up late", -30 * time.Second, "deadline_open=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, srv := towerTestBroker(t)
			op := signedInOperator(t, b, "octocat")
			owner := ownerPubkeyOf(t, b, op.login)
			tw := enrolledTower(t, b, op.login)
			stationPriv := attachStation(t, b, "st-w", tw.id, owner)

			issuedAttemptUntil(t, b, "att-window", tw.id, "st-w", 1, time.Now().Add(tc.exec))
			require.Equal(t, int64(2), reviveStation(t, b, "st-w", tw.id, owner))

			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(old) })

			var out map[string]any
			code, _ := tw.call(t, srv, "/tower/edge/settle",
				settleBody(t, tw.id, "st-w", "att-window", stationPriv, 4), &out)
			require.Equal(t, http.StatusGone, code, out)

			line := logs.String()
			require.Contains(t, line, "edge fence MOVED ")
			require.Contains(t, line, tc.want,
				"deadline_open must answer for the EXECUTION window (the grant's deadline), not "+
					"for the record's, which is that plus edgeSettleGrace() and is open for "+
					"almost every firing: %s", line)
		})
	}
}
