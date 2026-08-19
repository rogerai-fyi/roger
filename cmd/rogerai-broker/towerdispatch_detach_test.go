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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/attach"
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
	code, raw := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
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

	require.Equal(t, attach.StateDetached, stateOf(t, b, stationID),
		"a station whose machine has not been seen since before the horizon is still live")

	rows, err := b.tower.routable.ByTower(towerID, time.Now())
	require.NoError(t, err)
	require.Empty(t, rows,
		"the projection still carries a row for a retired attachment - which is the bloat, not just the symptom")
}

// AND A MACHINE THAT IS STILL THERE IS NEVER RETIRED, however many horizons pass. This is the
// leg that matters: the failure direction of a retirement sweep is an operator's node silently
// falling off the network, and a stamp that does not actually stamp would look exactly like a
// working sweep until somebody's Station disappeared.
func TestALiveMachineIsNeverRetiredHoweverLongTheSweepRuns(t *testing.T) {
	shortenIdleHorizon(t, 20*time.Millisecond)
	b, srv := towerTestBroker(t)
	towerID, stationID, nodeID := attachedStation(t, b, srv, "keepalive-op")

	for i := 0; i < 6; i++ {
		time.Sleep(30 * time.Millisecond) // longer than the whole horizon, every time
		b.mu.Lock()
		b.lastSeen[nodeID] = time.Now() // the node is heartbeating, as a live node does
		b.mu.Unlock()
		b.publishRoutable(towerID)
		require.Equal(t, attach.StateActive, stateOf(t, b, stationID),
			"sweep %d retired a station whose machine is right there", i)
	}

	rows, err := b.tower.routable.ByTower(towerID, time.Now())
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

	require.Equal(t, attach.StateDetached, stateOf(t, b, quietStation))
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

// stateOf reads an attachment's lifecycle state straight from the registry.
func stateOf(t *testing.T, b *broker, stationID string) string {
	t.Helper()
	at, found, err := b.tower.stations.Station(stationID)
	require.NoError(t, err)
	require.True(t, found)
	return at.State
}
