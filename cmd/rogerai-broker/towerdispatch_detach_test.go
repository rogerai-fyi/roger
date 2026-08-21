package main

// towerdispatch_detach_test.go is the spec for how an attachment ENDS.
//
// It had no way to. attach.StateDetached was declared and read and never assigned outside
// terminal reaping, so the only exit from the live set was an owner explicitly revoking - and
// publishRoutable republished every live attachment on every sweep. A machine that ran
// `roger share` once and pressed Ctrl-C therefore stayed a live attachment, and a routable row,
// for as long as the database existed. The eligibility gate added in M1's second correction
// stopped such a row from taking traffic; nothing stopped the table from growing.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/attach"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

// attachedStation brings one self-attached station on air behind a live tower and returns both
// ids, plus the broker node id the attachment is joined to.
func attachedStation(t *testing.T, b *broker, srv *httptest.Server, towerLogin string) (towerID, stationID, nodeID string) {
	t.Helper()
	tw := liveEdgeTower(t, b, srv, towerLogin, "203.0.113.9:8443")
	node := signedInOperator(t, b, towerLogin+"-node")
	body, _ := selfAttachBodyFor(t, b, node)
	var out struct {
		StationID string `json:"station_id"`
	}
	code, raw := node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusOK, code, raw)

	at, found, err := b.tower.stations.Station(out.StationID)
	require.NoError(t, err)
	require.True(t, found)
	require.NotEmpty(t, at.NodeID, "M0's join is what makes liveness knowable from an attachment")
	return tw.id, out.StationID, at.NodeID
}

// shortenIdleHorizon runs the retirement sweep on a scale a test can wait for.
func shortenIdleHorizon(t *testing.T, d time.Duration) {
	t.Helper()
	old := attachmentIdleHorizon
	attachmentIdleHorizon = d
	t.Cleanup(func() { attachmentIdleHorizon = old })
}

// A MACHINE THAT WENT HOME IS EVENTUALLY RETIRED, and its row leaves the projection with it.
func TestAnAttachmentWhoseMachineWentHomeIsRetired(t *testing.T) {
	shortenIdleHorizon(t, 40*time.Millisecond)
	b, srv := towerTestBroker(t)
	towerID, stationID, nodeID := attachedStation(t, b, srv, "detach-op")

	// The attach itself published, and the node is heartbeating, so the sweep stamped it.
	require.Equal(t, attach.StateActive, stateOf(t, b, stationID))

	// The operator presses Ctrl-C. The registration stops being refreshed; the attachment knows
	// nothing about that, which is exactly the gap this closes.
	b.mu.Lock()
	b.lastSeen[nodeID] = time.Now().Add(-time.Hour)
	b.mu.Unlock()

	time.Sleep(60 * time.Millisecond)
	b.publishRoutable(towerID)

	require.Equal(t, attach.StateDormant, stateOf(t, b, stationID),
		"a station whose machine has not been seen since before the horizon is still live")
	require.False(t, mustAttachment(t, b, stationID).Live(),
		"a dormant station must not carry work")

	rows, err := b.tower.routable.ByTower(towerID, time.Now())
	require.NoError(t, err)
	require.Empty(t, rows,
		"the projection still carries a row for a retired attachment - which is the bloat, not just the symptom")
}

// AND A MACHINE THAT IS STILL THERE IS NEVER RETIRED, however many horizons pass. This is the
// leg that matters: the failure direction of a retirement sweep is an operator's node silently
// falling off the network, and a stamp that does not actually stamp would look exactly like a
// working sweep until somebody's Station disappeared.
//
// IT DRIVES THE CLOCK RATHER THAN RACING IT, and that is not a style preference - the previous
// shape could not state the property it was testing. It shortened the horizon to 20ms and slept
// 30ms between sweeps, so the only thing keeping the live Station alive was that publishRoutable
// gets from TouchRoutable(alive, now) to DetachIdle(tower, now-20ms) in under twenty
// milliseconds. Those are two adjacent statements and usually microseconds apart; they are also
// separated by whatever the runtime feels like doing in between, and a GC pause or a busy CI box
// is worth more than 20ms often enough to matter. The test would then retire a live machine and
// report the retirement bug it exists to catch, on code that has no bug. It was never observed
// failing on its own here - it is a margin rather than an active flake - but a 25ms sleep
// injected between those two statements, which is all a loaded box is, failed it three runs out
// of three. Margins are only ever paid in.
//
// Widening the margin (a bigger horizon, bigger sleeps) buys probability and costs seconds, and
// still leaves a number that a slow enough machine beats. So instead the pass is handed its
// instant: publishRoutableAt takes the `now` that the stamp and the cutoff are both measured
// from, so "the row was stamped this pass, therefore it is one horizon inside the cutoff" is
// arithmetic on one variable rather than a bet on the scheduler. Nothing can be stalled
// through, because no wall-clock duration appears in the property at all.
//
// The clock is advanced by THIRTY PRODUCTION HORIZONS between sweeps - 210 days at the real
// seven-day setting, which is why this no longer shortens the horizon either. That keeps the
// test's teeth: every sweep after the first judges a row whose previous stamp is 209 days past
// the cutoff, so if the stamping loop stops stamping (the whole point of the leg) the very next
// require fails. And it runs in microseconds instead of 180ms.
func TestALiveMachineIsNeverRetiredHoweverLongTheSweepRuns(t *testing.T) {
	b, srv := towerTestBroker(t)
	towerID, stationID, nodeID := attachedStation(t, b, srv, "keepalive-op")

	now := time.Now()
	for i := 0; i < 6; i++ {
		now = now.Add(30 * attachmentIdleHorizon) // longer than the whole horizon, every time
		b.mu.Lock()
		b.lastSeen[nodeID] = now // the node is heartbeating, as a live node does
		b.mu.Unlock()
		b.publishRoutableAt(towerID, now)
		require.Equal(t, attach.StateActive, stateOf(t, b, stationID),
			"sweep %d retired a station whose machine is right there", i)
	}

	rows, err := b.tower.routable.ByTower(towerID, now)
	require.NoError(t, err)
	require.Len(t, rows, 1, "a live station must still be routable")
}

// The retirement is scoped to ONE Tower - the sweep runs per Tower, and a Tower's housekeeping
// must not reach across to somebody else's fleet.
//
// IT ALSO SWEEPS A CLASSIC STATION ON THE TOWER IT IS SWEEPING, and that leg is here because
// this test used to be the reason nobody noticed the defect below. It created exactly the
// Station that was being wrongly retired and then asserted about it behind the OTHER Tower -
// so it passed on the tower-scoping rule while concealing that the Station would have been
// retired had the sweep reached it. A test that constructs the failing case and then looks
// somewhere else is worse than no test, because it reads like coverage.
func TestRetirementDoesNotReachAcrossTowers(t *testing.T) {
	shortenIdleHorizon(t, 40*time.Millisecond)
	b, srv := towerTestBroker(t)
	quietTower, quietStation, quietNode := attachedStation(t, b, srv, "quiet-op")

	// A classic Station on the tower that IS swept, and one on a tower that is not.
	attachStation(t, b, "st-classichere", quietTower, "quiet-op")
	other := enrolledTower(t, b, "other-owner")
	require.NoError(t, b.tower.registry.Transition(other.id, admit.StateActive))
	attachStation(t, b, "st-elsewhere", other.id, "other-owner")

	b.mu.Lock()
	b.lastSeen[quietNode] = time.Now().Add(-time.Hour)
	b.mu.Unlock()

	time.Sleep(60 * time.Millisecond)
	b.publishRoutable(quietTower) // only this one sweeps

	require.Equal(t, attach.StateDormant, stateOf(t, b, quietStation))
	require.Equal(t, attach.StateActive, stateOf(t, b, "st-classichere"),
		"the swept Tower retired its own classic Station - see TestAClassicStationOutlivesTheHorizon")
	require.Equal(t, attach.StateActive, stateOf(t, b, "st-elsewhere"),
		"sweeping one Tower retired an attachment behind another")
}

// EVERY CLASSIC STATION WAS RETIRED SEVEN DAYS AFTER IT ATTACHED, PERMANENTLY, BY ITS OWN
// TOWER. This is the whole defect, end to end through the real publish path.
//
// Two halves, both of which had to be true. publishRoutable `continue`s past a classic
// attachment before it can enter `alive`, so TouchRoutable never stamps one - correctly, since
// there is no node id on it to join to a live registration, and no roger-share half to
// heartbeat. And DetachIdle's WHERE clause was scoped by origin tower and live state only, with
// nothing about whether a stamp was ever POSSIBLE. So COALESCE(last_routable, attached_at) sat
// at attached_at forever, the row crossed the horizon on schedule, and publishRoutable - which
// runs on every inventory push and for every live tower on the housekeeping tick - retired it.
//
// What makes it unrecoverable rather than annoying: StateDetached is terminal. checkBindings
// answers "this Station ID has been retired and cannot be reattached", and the row is not even
// freed for a fresh attach under the same id until terminalAttachmentHorizon, a month later.
//
// The horizon here is 40ms and the sweep runs six times, which is a hundred and fifty horizons.
// A classic Station is not "retired slowly"; it is never retired by this sweep at all.
func TestAClassicStationOutlivesTheHorizon(t *testing.T) {
	shortenIdleHorizon(t, 40*time.Millisecond)
	b, srv := towerTestBroker(t)
	towerID, _, _ := attachedStation(t, b, srv, "classic-op")
	attachStation(t, b, "st-classic", towerID, "classic-op")

	for i := 0; i < 6; i++ {
		time.Sleep(50 * time.Millisecond)
		b.publishRoutable(towerID)
		require.Equal(t, attach.StateActive, stateOf(t, b, "st-classic"),
			"sweep %d retired a Station whose liveness this broker has no way to observe", i)
	}

	// And it is still attachable-to: the terminal state is what would have made this permanent.
	at, found, err := b.tower.stations.Station("st-classic")
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, at.Live(), "a retired Station ID can never be reattached (checkBindings)")
}

// mustAttachment reads one attachment straight from the registry.
func mustAttachment(t *testing.T, b *broker, stationID string) attach.Attachment {
	t.Helper()
	at, found, err := b.tower.stations.Station(stationID)
	require.NoError(t, err)
	require.True(t, found)
	return at
}

// stateOf reads an attachment's lifecycle state straight from the registry.
func stateOf(t *testing.T, b *broker, stationID string) string {
	t.Helper()
	at, found, err := b.tower.stations.Station(stationID)
	require.NoError(t, err)
	require.True(t, found)
	return at.State
}

// THE STAMP PREDICATE AND THE RETIRE PREDICATE HAVE TO BE ONE PREDICATE.
//
// The fix above scoped DetachIdle to rows carrying a node id, and the argument given for it -
// "a sweep may only judge a row it could have found evidence FOR" - is exactly right. It is
// also only true while the STAMPING loop and the SWEEP agree on which rows those are, and they
// did not. publishRoutable stamped rows that were self-attached AND carried a model; DetachIdle
// judged every live row where node_id <> ”. The gap between the two is a row with a node id and
// no model: skipped by the stamp, judged by the sweep, retired on the horizon, terminally.
//
// It is not reachable today - self-attach refuses an attach with no model - so this is a latent
// defect rather than a live one. It is pinned anyway, because "unreachable" is a property of a
// validator two packages away and the scope argument is a property of THESE TWO LOOPS. The
// previous version of this defect was also unreachable right up until the classic-invite flow
// made it reachable.
//
// THIS TEST DID NOT PIN ANY OF THAT UNTIL NOW, and the way it failed to is worth keeping in
// view. It asserted `stateOf(...) != attach.StateDetached` - and the idle sweep does not write
// StateDetached and has not since the soft/terminal split: DetachIdle writes StateDormant, in
// both stores, deliberately, because retiring a Station terminally is what made a fortnight
// away cost an operator their identity. So the assertion was true of a row the sweep had just
// retired, and disabling the stamping loop outright - the exact defect above, in its strongest
// form - left this test passing. It now asserts that five sweeps leave the row in the state
// admission put it in, and that mutation fails it on the first sweep.
//
// The state is READ rather than named, and it is StateQuarantine rather than StateActive
// today, because this row is written straight through the registry: Admit always admits into
// quarantine and it is the self-attach handler, deliberately skipped here, that promotes. What
// the sweep's WHERE clause judges is `state IN ('quarantine','active')` - both halves of
// Live() - so a quarantined row is squarely in scope, and naming the literal would turn a test
// about the sweep into a test about admission's opening state.
//
// The clock is driven for the same reason as the live-machine test above: with the horizon
// shortened to 40ms, a stall between TouchRoutable(alive, now) and DetachIdle(tower, now-40ms)
// retires a row that was stamped microseconds earlier, and the test would then report this
// defect against code that does not have it. publishRoutableAt hands both halves one instant,
// so the margin is a horizon of driven time and nothing can be stalled through it.
func TestAnyRowTheSweepCanJudgeIsARowTheStampCanReach(t *testing.T) {
	b, srv := towerTestBroker(t)
	towerID, _, _ := attachedStation(t, b, srv, "parity-op")

	// A row DetachIdle would judge (it carries a node id) that the stamping loop used to skip
	// (it carries no model). Written straight through the registry, because the attach handler
	// is the validator that makes it unreachable and the point is that the sweep must not
	// depend on that validator.
	const stationID, nodeID = "st-nomodel", "n-nomodel"
	apub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sessionRaw := make([]byte, 32)
	copy(sessionRaw, stationID)
	auth, secret, err := attach.NewInvite(attach.Authorization{
		ID: "auth-" + stationID, Network: link.PublicNetwork, StationID: stationID,
		Owner:        ownerPubkeyOf(t, b, "parity-op"),
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: hex.EncodeToString(apub), SessionKey: hex.EncodeToString(sessionRaw),
		NodeID: nodeID, // the join is there; the offer is not
	}, time.Hour, time.Now())
	require.NoError(t, err)
	require.NoError(t, b.tower.stationStore.PutAuthorization(auth))
	_, err = b.tower.stations.Admit(attach.Proof{
		AuthID: auth.ID, Secret: secret, Network: link.PublicNetwork, StationID: stationID,
		Owner:        ownerPubkeyOf(t, b, "parity-op"),
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: hex.EncodeToString(apub), SessionKey: hex.EncodeToString(sessionRaw),
	})
	require.NoError(t, err)

	// Its machine is right there, heartbeating, on every sweep. That is the evidence the sweep
	// exists to look for, and the stamp is the only thing that records it.
	b.mu.Lock()
	b.nodes[nodeID] = protocol.NodeRegistration{NodeID: nodeID}
	b.mu.Unlock()
	admitted := stateOf(t, b, stationID)
	require.NotEqual(t, attach.StateDormant, admitted,
		"the row has to start in a state the sweep would judge, or this proves nothing")
	now := time.Now()
	for i := 0; i < 5; i++ {
		now = now.Add(30 * attachmentIdleHorizon) // longer than the whole horizon, every time
		b.mu.Lock()
		b.lastSeen[nodeID] = now
		b.mu.Unlock()
		b.publishRoutableAt(towerID, now)
		require.Equal(t, admitted, stateOf(t, b, stationID),
			"sweep %d retired a live machine the stamping loop had no way to vouch for", i)
	}
}

// A FORTNIGHT AWAY MUST NOT COST AN OPERATOR THEIR STATION, and this is that end to end,
// through the real self-attach handler with the real registry underneath.
//
// The idle sweep used to write StateDetached, which is terminal: the very next `roger share`
// re-attaches with the SAME persistent on-disk identity - same station id, same assertion key,
// same session key, because that identity is deliberately persistent and Core verifies every
// receipt against it - and got 409 "this Station ID has been retired and cannot be reattached".
// The row was not even freed for a fresh Station under that id for another month.
//
// The dependency is the part that made it unacceptable rather than merely harsh. The stamp the
// sweep measures has exactly ONE writer, publishRoutable joining a node id to a live
// registration, so a week of that mirror being broken on the instance holding a Tower's link
// retired every self-attached Station behind it, permanently, with nobody deciding anything.
func TestAStationThatWentQuietForAFortnightCanComeBack(t *testing.T) {
	shortenIdleHorizon(t, 40*time.Millisecond)
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "holiday-op", "203.0.113.9:8443")
	op := signedInOperator(t, b, "holiday-op-node")
	body, apub := selfAttachBodyFor(t, b, op)
	// The node names its own persistent Station id, exactly as agent.AttachTower does from the
	// identity on disk. Without that this test would be about minting a NEW Station, which is
	// not what a returning machine does and not what was broken. The id is DERIVED from the
	// assertion key (protocol.DeriveStationID) rather than chosen, which is what station.Init
	// stamps on disk and the only id Core will accept beside this key.
	stationID := protocol.DeriveStationID(apub)
	body["station_id"] = stationID

	var first struct {
		StationID string `json:"station_id"`
	}
	code, raw := op.attach(t, srv, body, &first)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, stationID, first.StationID)
	nodeID := body["node_id"].(string)

	// Two weeks off: the machine stops heartbeating and the sweep runs.
	b.mu.Lock()
	b.lastSeen[nodeID] = time.Now().Add(-14 * 24 * time.Hour)
	b.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	b.publishRoutable(tw.id)

	require.Equal(t, attach.StateDormant, stateOf(t, b, stationID),
		"the idle sweep still ends a Station's identity outright")
	rows, err := b.tower.routable.ByTower(tw.id, time.Now())
	require.NoError(t, err)
	require.Empty(t, rows, "a dormant Station must carry no work and appear in no projection")

	// THE OPERATOR COMES HOME. Same identity, same keys, same node - one `roger share`.
	b.mu.Lock()
	b.lastSeen[nodeID] = time.Now()
	b.mu.Unlock()
	var second struct {
		StationID string `json:"station_id"`
		State     string `json:"state"`
	}
	code, raw = op.attach(t, srv, body, &second)
	require.Equal(t, http.StatusOK, code,
		"a machine that was away for a fortnight was refused its own Station: %s", raw)
	require.Equal(t, stationID, second.StationID, "coming back must not mint a second identity")
	require.Equal(t, attach.StateActive, stateOf(t, b, stationID))

	// And it is routable again on the very next publish, rather than waiting out a sweep.
	b.publishRoutable(tw.id)
	rows, err = b.tower.routable.ByTower(tw.id, time.Now())
	require.NoError(t, err)
	require.Len(t, rows, 1, "the returning Station is not back in the projection")
}

// AND A STRANGER CANNOT WAKE SOMEBODY ELSE'S SLEEPING STATION. The assertion key is public and
// on the wire; what the revival branch requires is the whole identity - owner, origin kind and
// BOTH keys - and a mismatch is refused with the sentence for a mismatch rather than with the
// terminal one.
func TestADormantStationIsOnlyWokenByTheMachineThatHoldsIt(t *testing.T) {
	shortenIdleHorizon(t, 40*time.Millisecond)
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "sleep-op", "203.0.113.9:8443")
	owner := signedInOperator(t, b, "sleep-op-node")
	body, apub := selfAttachBodyFor(t, b, owner)
	stationID := protocol.DeriveStationID(apub)
	body["station_id"] = stationID
	code, raw := op0(t, owner, srv, body)
	require.Equal(t, http.StatusOK, code, raw)

	b.mu.Lock()
	b.lastSeen[body["node_id"].(string)] = time.Now().Add(-14 * 24 * time.Hour)
	b.mu.Unlock()
	time.Sleep(60 * time.Millisecond)
	b.publishRoutable(tw.id)
	require.Equal(t, attach.StateDormant, stateOf(t, b, stationID))

	// A different account, a different machine, the SAME station id and the same public
	// assertion key it read off the plaintext hub link. It must be the same assertion key, and
	// not merely the same id: the id is derived from that key, so a thief naming this id beside
	// a key of their own is turned away by the derivation check without ever reaching the
	// dormant-revival branch this test is about.
	thief := signedInOperator(t, b, "thief-op")
	stolen, _ := selfAttachBodyFor(t, b, thief)
	stolen["station_id"] = stationID
	stolen["assertion_key"] = body["assertion_key"]
	code, raw = op0(t, thief, srv, stolen)
	require.NotEqual(t, http.StatusOK, code,
		"a stranger woke somebody else's dormant Station: %s", raw)
	require.Equal(t, attach.StateDormant, stateOf(t, b, stationID),
		"the refused attempt still moved the sleeping Station's state")
}

// op0 posts a self-attach body and returns the status and raw answer.
func op0(t *testing.T, o operator, srv *httptest.Server, body map[string]any) (int, string) {
	t.Helper()
	var out map[string]any
	return o.attach(t, srv, body, &out)
}
