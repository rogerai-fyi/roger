package main

// Moving idle Stations between Towers. Contract: features/tower/station_mobility.feature.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/attach"
	"rogerai.fm/roger/v7/internal/towercore/cert"
	"rogerai.fm/roger/v7/internal/towercore/enroll"
	"rogerai.fm/roger/v7/internal/towercore/head"
	"rogerai.fm/roger/v7/internal/towercore/link"
	"rogerai.fm/roger/v7/internal/towercore/mobility"
	"rogerai.fm/roger/v7/internal/towercore/placement"
)

// movingRig is a Station S live on Tower A, with Tower B coming up after it attached. The
// node declared the nudge and polls for cancels unless told otherwise.
type movingRig struct {
	b         *broker
	srv       *httptest.Server
	a, bTower linkTower
	node      operator
	body      map[string]any
	station   string
	nodeID    string
}

func newMovingRig(t *testing.T, nudge bool) *movingRig {
	t.Helper()
	b, srv := towerTestBroker(t)
	b.netTable = testNetTable(t)
	a := liveEdgeTower(t, b, srv, "op-a", "192.0.2.9:8443")
	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)
	body["nudge"] = nudge
	var out struct {
		StationID string `json:"station_id"`
		TowerID   string `json:"tower_id"`
	}
	code, raw := node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, a.id, out.TowerID)
	bt := liveEdgeTower(t, b, srv, "op-b", "198.51.100.9:8443")
	waitMeasured(t, b, a.id)
	waitMeasured(t, b, bt.id)
	nodeID := body["node_id"].(string)
	b.noteCancelCapable(nodeID)
	// The Station is in B's network: B is strictly better than A (far).
	require.NoError(t, b.tower.mobility.PutFacts(out.StationID, mobility.Facts{
		Locality: placement.Locality{Bucket: "198.51.100.0/24", Continent: "EU"}, Nudge: nudge}))
	return &movingRig{b: b, srv: srv, a: a, bTower: bt, node: node, body: body, station: out.StationID, nodeID: nodeID}
}

func (r *movingRig) attachment(t *testing.T) attach.Attachment {
	t.Helper()
	at, ok, err := r.b.tower.stations.Station(r.station)
	require.NoError(t, err)
	require.True(t, ok)
	return at
}

// observe records one fresh health check of A and runs the mobility pass.
func (r *movingRig) observe(t *testing.T, ok bool, at time.Time) {
	t.Helper()
	r.b.recordHealthResult(r.a.id, ok, at)
	r.b.mobilityPassOnce(at)
}

func (r *movingRig) reattach(t *testing.T) (int, string, string) {
	t.Helper()
	var out struct {
		TowerID string `json:"tower_id"`
	}
	code, raw := r.node.attach(t, r.srv, r.body, &out)
	return code, out.TowerID, raw
}

// passGone runs the mobility pass once to see a closed Tower missing and again once its absence
// has outlasted the link freshness window - the only way a Tower becomes "gone".
func passGone(b *broker, now time.Time) time.Time {
	b.mobilityPassOnce(now)
	later := now.Add(b.tower.link.Freshness() + time.Second)
	b.mobilityPassOnce(later)
	return later
}

func reattachNudges(b *broker, node string) int {
	b.cancels.mu.Lock()
	defer b.cancels.mu.Unlock()
	n := 0
	for _, e := range b.cancels.buf[node] {
		if e.Reattach != "" {
			n++
		}
	}
	return n
}

func TestABetterTowerOnThreeFreshObservationsMovesAnIdleStation(t *testing.T) {
	r := newMovingRig(t, true)
	now := time.Now()
	r.observe(t, true, now)
	r.observe(t, true, now.Add(5*time.Minute))
	require.True(t, r.attachment(t).Live(), "two observations are not enough")
	r.observe(t, true, now.Add(10*time.Minute))

	at := r.attachment(t)
	require.Equal(t, attach.StateDormant, at.State)
	require.Equal(t, r.a.id, at.Origin.TowerID, "the origin is written by the re-attach, not the mover")
	evs := towerEventsOf(t, r.b, r.a.id, evStationMoveStarted)
	require.Len(t, evs, 1)
	d := evs[0].Detail
	require.Equal(t, r.station, d["station"])
	require.Equal(t, r.bTower.id, d["to"])
	require.Equal(t, "better_tower", evs[0].Reason)
	for _, k := range []string{"inflight", "edge_load", "peer_inflight", "peer_edge_load"} {
		require.Contains(t, d, k, "the load the decision saw")
	}
	require.Equal(t, 1, reattachNudges(r.b, r.nodeID))

	// The node re-attaches with its own identity and lands on B at the next epoch.
	code, tower, raw := r.reattach(t)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, r.bTower.id, tower)
	at = r.attachment(t)
	require.True(t, at.Live())
	require.Equal(t, r.bTower.id, at.Origin.TowerID)
	require.Equal(t, int64(2), at.Epoch)
	moved := towerEventsOf(t, r.b, r.bTower.id, evStationMoved)
	require.Len(t, moved, 1)
	require.Equal(t, r.a.id, moved[0].Detail["from"])
	require.EqualValues(t, 2, moved[0].Detail["epoch"])
	require.Len(t, towerEventsOf(t, r.b, r.a.id, evStationMovedAway), 1)
	_, pending, _ := r.b.tower.mobility.Move(r.station)
	require.False(t, pending, "the move is finished")
}

func TestTheStreakCountsObservationsNotPasses(t *testing.T) {
	r := newMovingRig(t, true)
	now := time.Now()
	r.observe(t, true, now)
	for i := 0; i < 5; i++ {
		r.b.mobilityPassOnce(now.Add(time.Duration(i) * time.Minute)) // same sample, many passes
	}
	r.observe(t, true, now.Add(5*time.Minute))
	require.True(t, r.attachment(t).Live(), "two observations however many passes")
}

func TestAStaleStreakIsDiscarded(t *testing.T) {
	r := newMovingRig(t, true)
	now := time.Now()
	r.observe(t, true, now)
	r.observe(t, true, now.Add(5*time.Minute))
	r.observe(t, true, now.Add(5*time.Minute+2*canaryInterval+time.Second))
	require.True(t, r.attachment(t).Live(), "the gap discarded the streak: this is one, not three")
}

func TestAFailingTowerOnTwoHealthChecks(t *testing.T) {
	r := newMovingRig(t, true)
	now := time.Now()
	r.observe(t, false, now)
	require.True(t, r.attachment(t).Live())
	r.observe(t, false, now.Add(5*time.Minute))
	require.Equal(t, attach.StateDormant, r.attachment(t).State)
	require.Equal(t, "tower_failing", towerEventsOf(t, r.b, r.a.id, evStationMoveStarted)[0].Reason)
}

func TestAGoneTowerMovesItsStationsWithoutObservationsAndEvenWithLoad(t *testing.T) {
	r := newMovingRig(t, false) // an old node: a gone Tower still moves it, with no nudge
	r.b.metricsMu.Lock()
	if r.b.inflight == nil {
		r.b.inflight = map[string]int{}
	}
	r.b.inflight[r.nodeID] = 1
	r.b.metricsMu.Unlock()
	r.b.tower.link.Close(mustCurrent(t, r.b, r.a.id), r.a.id)
	r.b.mobilityPassOnce(time.Now())
	require.True(t, r.attachment(t).Live(), "a Tower that just closed is not gone yet")
	passGone(r.b, time.Now())
	require.Equal(t, attach.StateDormant, r.attachment(t).State)
	require.Equal(t, "tower_gone", towerEventsOf(t, r.b, r.a.id, evStationMoveStarted)[0].Reason)
	require.Zero(t, reattachNudges(r.b, r.nodeID), "an old node gets no nudge")
	code, tower, raw := r.reattach(t)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, r.bTower.id, tower)
}

func TestABusyStationIsLeftForLater(t *testing.T) {
	r := newMovingRig(t, true)
	r.b.metricsMu.Lock()
	if r.b.edgeLoad == nil {
		r.b.edgeLoad = map[string]int{}
	}
	r.b.edgeLoad[r.nodeID] = 1
	r.b.metricsMu.Unlock()
	now := time.Now()
	r.observe(t, false, now)
	r.observe(t, false, now.Add(5*time.Minute))
	require.True(t, r.attachment(t).Live(), "Core cannot see it idle")
	r.b.metricsMu.Lock()
	r.b.edgeLoad[r.nodeID] = 0
	r.b.metricsMu.Unlock()
	r.b.mobilityPassOnce(now.Add(6 * time.Minute))
	require.Equal(t, attach.StateDormant, r.attachment(t).State, "still due, moved once idle")
}

func TestAnOldNodeIsNotMovedForABetterTower(t *testing.T) {
	r := newMovingRig(t, false)
	now := time.Now()
	for i := 0; i < 4; i++ {
		r.observe(t, true, now.Add(time.Duration(i)*5*time.Minute))
	}
	require.True(t, r.attachment(t).Live())
	require.Empty(t, towerEventsOf(t, r.b, r.a.id, evStationMoveStarted))
}

func TestALostNudgeIsResentAndAMoveThatNeverCompletesIsUndone(t *testing.T) {
	r := newMovingRig(t, true)
	now := time.Now()
	r.observe(t, false, now)
	r.observe(t, false, now.Add(5*time.Minute))
	require.Equal(t, attach.StateDormant, r.attachment(t).State)
	start := now.Add(5 * time.Minute)
	r.b.mobilityPassOnce(start.Add(10 * time.Second))
	require.Equal(t, 1, reattachNudges(r.b, r.nodeID), "not yet 30 seconds")
	r.b.mobilityPassOnce(start.Add(31 * time.Second))
	require.Equal(t, 2, reattachNudges(r.b, r.nodeID))
	r.b.mobilityPassOnce(start.Add(62 * time.Second))
	require.Equal(t, 3, reattachNudges(r.b, r.nodeID))
	r.b.mobilityPassOnce(start.Add(93 * time.Second))

	at := r.attachment(t)
	require.True(t, at.Live(), "restored rather than left dark")
	require.Equal(t, r.a.id, at.Origin.TowerID)
	require.Equal(t, int64(1), at.Epoch, "the old epoch")
	ab := towerEventsOf(t, r.b, r.a.id, evStationMoveAbandoned)
	require.Len(t, ab, 1)
	require.Equal(t, "no re-attach", ab[0].Reason)
	_, until, _ := r.b.tower.mobility.Skip(r.station)
	require.WithinDuration(t, start.Add(93*time.Second+time.Hour), until, time.Second)

	// While the skip lasts, the still-failing Tower does not start another move.
	r.b.mobilityPassOnce(start.Add(2 * time.Minute))
	require.True(t, r.attachment(t).Live())
	require.Len(t, towerEventsOf(t, r.b, r.a.id, evStationMoveStarted), 1)
}

func TestAnUndoneMoveKeepsTheStationOffItsIntendedTowerForAnHour(t *testing.T) {
	r := newMovingRig(t, true)
	now := time.Now()
	require.True(t, r.b.startMove(r.attachment(t), r.bTower.id, triggerBetter, now))
	for i := 1; i <= 3; i++ {
		r.b.mobilityPending(now.Add(time.Duration(i) * 31 * time.Second))
	}
	require.True(t, r.attachment(t).Live())
	tw, until, _ := r.b.tower.mobility.Skip(r.station)
	require.Equal(t, r.bTower.id, tw)
	require.WithinDuration(t, now.Add(93*time.Second+time.Hour), until, time.Second)
	_, _, excl := r.b.moveOnReattach(r.station, now.Add(2*time.Minute))
	require.True(t, excl[r.bTower.id], "a re-attach in that hour is never placed on the skipped Tower")
	_, _, excl = r.b.moveOnReattach(r.station, now.Add(2*time.Hour))
	require.False(t, excl[r.bTower.id], "after the hour it may be")
}

func TestAMoveOffAGoneTowerIsNeverUndoneBackOntoIt(t *testing.T) {
	r := newMovingRig(t, true)
	r.b.tower.link.Close(mustCurrent(t, r.b, r.a.id), r.a.id)
	now := passGone(r.b, time.Now())
	require.Equal(t, attach.StateDormant, r.attachment(t).State)
	for i := 1; i <= 4; i++ {
		r.b.mobilityPassOnce(now.Add(time.Duration(i) * 31 * time.Second))
	}
	require.Equal(t, attach.StateDormant, r.attachment(t).State, "left dormant, placed afresh when it next attaches")
	_, pending, _ := r.b.tower.mobility.Move(r.station)
	require.False(t, pending)
}

func TestAReattachThatLandsBackIsRecordedAsReturned(t *testing.T) {
	r := newMovingRig(t, true)
	now := time.Now()
	r.observe(t, false, now)
	r.observe(t, false, now.Add(5*time.Minute))
	// B goes away before the node comes back: A is the only Tower.
	r.b.tower.link.Close(mustCurrent(t, r.b, r.bTower.id), r.bTower.id)
	code, tower, raw := r.reattach(t)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, r.a.id, tower)
	require.Equal(t, int64(2), r.attachment(t).Epoch)
	require.Len(t, towerEventsOf(t, r.b, r.a.id, evStationMoveReturned), 1)
	require.Empty(t, towerEventsOf(t, r.b, r.a.id, evStationMoved))
}

func TestAConcurrentChangeWinsAndTheMoveGivesUp(t *testing.T) {
	r := newMovingRig(t, true)
	at := r.attachment(t)
	_, err := r.b.tower.stationStore.SetState(r.station, attach.StateRevoked)
	require.NoError(t, err)
	r.b.startMove(at, r.bTower.id, "better_tower", time.Now())
	require.Empty(t, towerEventsOf(t, r.b, r.a.id, evStationMoveStarted))
	require.Zero(t, reattachNudges(r.b, r.nodeID))
}

func TestMovesArePacedPerTowerAndFleetWide(t *testing.T) {
	b, srv := towerTestBroker(t)
	a := liveEdgeTower(t, b, srv, "op-a", "192.0.2.9:8443")
	for i := 0; i < 8; i++ {
		node := signedInOperator(t, b, "node-"+randHex(4))
		body, _ := selfAttachBodyFor(t, b, node)
		code, raw := node.attach(t, srv, body, nil)
		require.Equal(t, http.StatusOK, code, raw)
	}
	liveEdgeTower(t, b, srv, "op-b", "198.51.100.9:8443")
	b.tower.link.Close(mustCurrent(t, b, a.id), a.id)
	later := passGone(b, time.Now())
	require.Len(t, towerEventsOf(t, b, a.id, evStationMoveStarted), movesPerTowerPerPass)
	b.mobilityPassOnce(later.Add(time.Minute))
	require.Len(t, towerEventsOf(t, b, a.id, evStationMoveStarted), 8)
}

func TestTheNewestStationsAboveADemotedTowersCapAreDue(t *testing.T) {
	b, srv := towerTestBroker(t)
	a := liveEdgeTower(t, b, srv, "op-a", "192.0.2.9:8443")
	var first string
	var nodes []string
	for i := 0; i < 12; i++ {
		node := signedInOperator(t, b, "node-"+randHex(4))
		body, _ := selfAttachBodyFor(t, b, node)
		body["nudge"] = true
		var out struct {
			StationID string `json:"station_id"`
		}
		code, raw := node.attach(t, srv, body, &out)
		require.Equal(t, http.StatusOK, code, raw)
		if i == 0 {
			first = out.StationID
		}
		nodes = append(nodes, body["node_id"].(string))
		time.Sleep(2 * time.Millisecond)
	}
	for _, n := range nodes {
		b.noteCancelCapable(n)
	}
	liveEdgeTower(t, b, srv, "op-b", "198.51.100.9:8443")
	setStage(t, b, a.id, placement.StageNew)
	b.mobilityPassOnce(time.Now())
	started := towerEventsOf(t, b, a.id, evStationMoveStarted)
	require.Len(t, started, 2, "12 on a Tower capped at 10: the two newest")
	for _, ev := range started {
		require.Equal(t, "stage_cap", ev.Reason)
		require.NotEqual(t, first, ev.Detail["station"], "the oldest are kept")
	}
}

func TestOnlyOneInstanceRunsThePendingHalf(t *testing.T) {
	b, _ := towerTestBroker(t)
	require.True(t, b.holdsMobilityLease(), "a single instance always does")
	b.multiInstance = true
	vs, _ := testValkeyShared(t)
	b.shared = vs
	b.instanceID = "i-1"
	require.True(t, b.holdsMobilityLease())
	require.False(t, b.holdsMobilityLease(), "the lease is held until it lapses")
}

func TestMovePacingCapsEachTowerEachTargetAndTheFleet(t *testing.T) {
	p := &movePace{from: map[string]int{}, into: map[string]int{}}
	for i := 0; i < movesIntoTowerPerPass; i++ {
		require.True(t, p.allow("tw-"+string(rune('a'+i)), "tw-x"))
		p.note("tw-"+string(rune('a'+i)), "tw-x")
	}
	require.False(t, p.allow("tw-z", "tw-x"), "no more than %d into one Tower", movesIntoTowerPerPass)
	require.True(t, p.allow("tw-z", ""), "a move with no target is not counted against one")
	for p.total < movesPerPass {
		p.note("tw-q"+randHex(2), "")
	}
	require.False(t, p.allow("tw-new", ""), "no more than %d fleet-wide", movesPerPass)
}

func TestMoveEventsCountTowardThePerTowerBudget(t *testing.T) {
	for _, k := range []string{evStationMoveStarted, evStationMoved, evStationMovedAway, evStationMoveReturned, evStationMoveAbandoned} {
		require.True(t, pacedEvent(admit.Event{Kind: k, Actor: admit.ActorSystem}), k)
	}
}

func TestAReattachThatMissesTheIntendedTowerSaysSo(t *testing.T) {
	r := newMovingRig(t, true)
	c := liveEdgeTower(t, r.b, r.srv, "op-c", "203.0.113.50:8443")
	require.True(t, r.b.startMove(r.attachment(t), c.id, triggerBetter, time.Now()))
	r.b.tower.link.Close(mustCurrent(t, r.b, c.id), c.id)
	code, tower, raw := r.reattach(t)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, r.bTower.id, tower, "re-ranked, excluding A")
	ev := towerEventsOf(t, r.b, r.bTower.id, evStationMoved)[0]
	require.Equal(t, true, ev.Detail["intended_unavailable"])
	require.Equal(t, c.id, ev.Detail["intended"])
}

func TestOldHealthEvidenceIsReapedByTheHousekeepingSweep(t *testing.T) {
	b, _ := towerTestBroker(t)
	old := time.Now().Add(-100 * 24 * time.Hour)
	require.NoError(t, recordCheckForTest(b.tower.health, "tw-1", true, old))
	require.NoError(t, recordCheckForTest(b.tower.health, "tw-1", true, time.Now()))
	b.towerInviteSweepOnce(time.Now())
	sum, _ := b.tower.health.Summary("tw-1", old)
	require.Equal(t, 1, sum.Passed)
}

// REVIEW FINDING: an unreadable mirror, a drain or a late lease looked like "gone".
func TestAnUnreadableLiveSetMovesNothing(t *testing.T) {
	m := link.NewMemMirror()
	b := testBrokerWithDB(store.NewMem())
	ts, err := newTowerSubsystem(b, admit.NewMemStore(), cert.NewMemCustody(), enroll.NewMemStore(),
		cert.Config{TTL: time.Hour}, linkDeps{stations: attach.NewMemStore(), heads: head.NewMemStore(), mirror: m})
	require.NoError(t, err)
	b.tower = ts
	mux := http.NewServeMux()
	b.registerTowerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	a := liveEdgeTower(t, b, srv, "op-a", "192.0.2.9:8443")
	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)
	code, raw := node.attach(t, srv, body, nil)
	require.Equal(t, http.StatusOK, code, raw)
	// A peer instance holds nothing here; the mirror then fails.
	peer := &broker{tower: b.tower, db: b.db, instanceID: "i-2"}
	m.FailForTest(true)
	peer.mobilityPassOnce(time.Now())
	require.Empty(t, towerEventsOf(t, b, a.id, evStationMoveStarted), "a mirror that cannot answer is not a Tower that is gone")
}

func TestADrainingOrSuspendedTowerIsNotGone(t *testing.T) {
	for _, st := range []admit.State{admit.StateDraining, admit.StateSuspended} {
		r := newMovingRig(t, true)
		require.NoError(t, r.b.tower.registry.Transition(r.a.id, st))
		r.b.mobilityPassOnce(time.Now())
		require.True(t, r.attachment(t).Live(), "%s: its Stations stay", st)
	}
}

func TestATowerWhoseLeaseJustLapsedIsNotGoneWhileItsLinkLives(t *testing.T) {
	r := newMovingRig(t, true)
	tw := mustTower(t, r.b, r.a.id)
	require.True(t, r.b.tower.link.Live(tw.ID))
	require.NoError(t, r.b.tower.registry.ExpireLease(tw.ID))
	r.b.mobilityPassOnce(time.Now())
	require.True(t, r.attachment(t).Live())
}

func TestTheGoneHalfRunsOnlyUnderTheLease(t *testing.T) {
	r := newMovingRig(t, false)
	vs, _ := testValkeyShared(t)
	r.b.multiInstance, r.b.shared, r.b.instanceID = true, vs, "i-1"
	require.True(t, r.b.holdsMobilityLease()) // another pass holds it now
	r.b.tower.link.Close(mustCurrent(t, r.b, r.a.id), r.a.id)
	passGone(r.b, time.Now())
	require.True(t, r.attachment(t).Live(), "without the lease this instance starts no gone moves")
}

// REVIEW FINDING: a crash between the two writes could strand a Station dark.
func TestAMoveRowIsClaimedBeforeTheDormantWrite(t *testing.T) {
	r := newMovingRig(t, true)
	at := r.attachment(t)
	// Simulate the crash: the row is claimed, the dormant write never happened.
	ok, err := r.b.tower.mobility.StartMove(mobility.Move{StationID: at.StationID, From: at.Origin.TowerID,
		Trigger: triggerFailing, Epoch: at.Epoch, PriorState: at.State, Started: time.Now()})
	require.NoError(t, err)
	require.True(t, ok)
	r.b.mobilityPending(time.Now().Add(5 * time.Second))
	_, pending, _ := r.b.tower.mobility.Move(at.StationID)
	require.True(t, pending, "a fresh row is left for its dormant write")
	r.b.mobilityPending(time.Now().Add(time.Minute))
	_, pending, _ = r.b.tower.mobility.Move(at.StationID)
	require.False(t, pending, "an old row whose Station still serves is cleared")
	require.True(t, r.attachment(t).Live())
}

// REVIEW FINDING: equally good Towers reset each other's streaks through the random draw.
func TestEquallyGoodTowersDoNotResetAStreak(t *testing.T) {
	r := newMovingRig(t, true)
	c := liveEdgeTower(t, r.b, r.srv, "op-c", "198.51.100.77:8443") // same bucket as B
	waitMeasured(t, r.b, c.id)
	now := time.Now()
	r.observe(t, true, now)
	r.observe(t, true, now.Add(5*time.Minute))
	r.observe(t, true, now.Add(10*time.Minute))
	require.Equal(t, attach.StateDormant, r.attachment(t).State, "three observations, one deterministic target")
}

func TestHouseFailoverAboveTheOwnersCapIsMovedOff(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "nora")
	var ids []string
	for i := 0; i < 2; i++ {
		lt := enrolledTower(t, b, op.login)
		require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
		code, raw := lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{Network: link.PublicNetwork,
			Versions: []int{1}, TowerID: lt.id, Capabilities: mandatoryCaps(),
			RelayEndpoint: "198.51.100." + string(rune('1'+i)) + ":8443"}), nil)
		require.Equal(t, http.StatusOK, code, raw)
		setStage(t, b, lt.id, placement.StageEstablished)
		ids = append(ids, lt.id)
	}
	for i := 0; i < 8; i++ {
		node := signedInOperator(t, b, "house-"+randHex(4))
		makeHouse(t, b, node.login)
		body, _ := selfAttachBodyFor(t, b, node)
		body["nudge"] = true
		code, raw := node.attach(t, srv, body, nil)
		require.Equal(t, http.StatusOK, code, raw)
		b.noteCancelCapable(body["node_id"].(string))
	}
	// Both of the owner's Towers drop to proving: the owner may hold 5 house Stations, not 8.
	for _, id := range ids {
		setStage(t, b, id, placement.StageProving)
	}
	houseTower(t, b, srv, "roger-house", "203.0.113.9:8443")
	b.mobilityPassOnce(time.Now())
	n := 0
	for _, id := range ids {
		n += len(towerEventsOf(t, b, id, evStationMoveStarted))
	}
	require.Equal(t, 3, n, "8 house Stations, owner cap 5: the 3 newest move")
}

// AUDIT FINDING: an orderly restart deletes the session at once, and the Tower was treated as
// gone on the next pass.
func TestATowerThatRestartsWithinTheWindowIsNotGone(t *testing.T) {
	r := newMovingRig(t, true)
	now := time.Now()
	r.b.tower.link.Close(mustCurrent(t, r.b, r.a.id), r.a.id)
	r.b.mobilityPassOnce(now)
	r.b.mobilityPassOnce(now.Add(r.b.tower.link.Freshness() / 2))
	require.True(t, r.attachment(t).Live())
	// It reconnects inside the window: the absence clock is forgotten.
	code, raw := r.a.call(t, r.srv, "/tower/session", jsonOf(t, link.Hello{Network: link.PublicNetwork,
		Versions: []int{1}, TowerID: r.a.id, Capabilities: mandatoryCaps(), RelayEndpoint: "192.0.2.9:8443"}), nil)
	require.Equal(t, http.StatusOK, code, raw)
	r.b.mobilityPassOnce(now.Add(2 * r.b.tower.link.Freshness()))
	require.True(t, r.attachment(t).Live(), "a Tower back inside the window was never gone")
	require.Empty(t, towerEventsOf(t, r.b, r.a.id, evStationMoveStarted))
}

// AUDIT FINDING: a move that landed back on its own Tower cleared the pause, so a Tower that
// stayed failing cycled the Station dark and back every pass.
func TestAReturnedMovePausesTheStation(t *testing.T) {
	r := newMovingRig(t, true)
	now := time.Now()
	require.True(t, r.b.startMove(r.attachment(t), "", triggerFailing, now))
	r.b.tower.link.Close(mustCurrent(t, r.b, r.bTower.id), r.bTower.id)
	code, tower, raw := r.reattach(t)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, r.a.id, tower)
	_, until, _ := r.b.tower.mobility.Skip(r.station)
	require.WithinDuration(t, time.Now().Add(moveSkipFor), until, 5*time.Second)
}

// AUDIT FINDING: a failing or over-cap move with nowhere to go only took the Station dark.
func TestAFailingTowerWithNoAlternativeKeepsItsStation(t *testing.T) {
	r := newMovingRig(t, true)
	r.b.tower.link.Close(mustCurrent(t, r.b, r.bTower.id), r.bTower.id) // B gone: nowhere to go
	now := time.Now()
	r.observe(t, false, now)
	r.observe(t, false, now.Add(5*time.Minute))
	require.True(t, r.attachment(t).Live())
	require.Empty(t, towerEventsOf(t, r.b, r.a.id, evStationMoveStarted))
}

// AUDIT FINDING: a failing move could go to another failing Tower, so Stations bounced.
func TestAFailingMoveNeedsAHealthyDestination(t *testing.T) {
	r := newMovingRig(t, true)
	now := time.Now()
	// B fails too.
	require.NoError(t, recordCheckForTest(r.b.tower.health, r.bTower.id, false, now))
	require.NoError(t, recordCheckForTest(r.b.tower.health, r.bTower.id, false, now.Add(time.Second)))
	r.observe(t, false, now)
	r.observe(t, false, now.Add(5*time.Minute))
	require.True(t, r.attachment(t).Live(), "no healthy Tower to go to: it stays")
	// B recovers: now it moves.
	require.NoError(t, recordCheckForTest(r.b.tower.health, r.bTower.id, true, now.Add(6*time.Minute)))
	r.observe(t, false, now.Add(7*time.Minute))
	require.Equal(t, attach.StateDormant, r.attachment(t).State)
}

// AUDIT FINDING: a silent Tower counted as gone the instant it left the live set.
func TestASilentTowerIsGoneOnlyAWindowAfterLeavingTheLiveSet(t *testing.T) {
	r := newMovingRig(t, false)
	w := r.b.tower.link.Freshness()
	last, ok := r.b.tower.link.LastSeen(r.a.id)
	require.True(t, ok)
	// Pretend no heartbeat since: just past one window it has left the live set...
	r.b.tower.link.Close(mustCurrent(t, r.b, r.a.id), r.a.id)
	r.b.moveMu.Lock()
	r.b.absentSince = map[string]time.Time{}
	r.b.moveMu.Unlock()
	r.b.mobilityPassOnce(last.Add(w + time.Second))
	require.True(t, r.attachment(t).Live(), "left the live set a moment ago: not gone yet")
	r.b.mobilityPassOnce(last.Add(2*w + 2*time.Second))
	require.Equal(t, attach.StateDormant, r.attachment(t).State)
}

// AUDIT FINDING: a fresh process after an outage declared every not-yet-relinked Tower gone on
// its first pass, from an old record of when it was last heard.
func TestAFreshInstanceGivesEveryTowerAFullWindow(t *testing.T) {
	r := newMovingRig(t, false)
	w := r.b.tower.link.Freshness()
	last, _ := r.b.tower.link.LastSeen(r.a.id)
	r.b.tower.link.Close(mustCurrent(t, r.b, r.a.id), r.a.id)
	// This process starts long after A was last heard.
	boot := last.Add(3 * w)
	r.b.moveMu.Lock()
	r.b.absentSince, r.b.mobilitySince = nil, time.Time{}
	r.b.moveMu.Unlock()
	r.b.mobilityPassOnce(boot)
	r.b.mobilityPassOnce(boot.Add(w / 2))
	require.True(t, r.attachment(t).Live(), "a new instance waits a full window before calling a Tower gone")
	r.b.mobilityPassOnce(boot.Add(w + time.Second))
	require.Equal(t, attach.StateDormant, r.attachment(t).State)
}

// AUDIT FINDING: the skip could leave a Station with no Tower at all.
func TestASkippedTowerIsUsedWhenItIsTheOnlyOne(t *testing.T) {
	r := newMovingRig(t, true)
	require.NoError(t, r.b.tower.mobility.FinishMove(r.station, r.bTower.id, time.Now().Add(time.Hour)))
	require.True(t, r.b.startMove(r.attachment(t), "", triggerGone, time.Now()))
	r.b.tower.link.Close(mustCurrent(t, r.b, r.a.id), r.a.id) // A is gone: B, skipped, is all there is
	code, tower, raw := r.reattach(t)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, r.bTower.id, tower)
}
