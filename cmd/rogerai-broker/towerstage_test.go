package main

// The trust ramp, as the broker runs it. Contract: features/tower/tower_trust_ramp.feature.

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/fleet"
	"rogerai.fm/roger/v7/internal/towercore/placement"
)

// approvedTower is a community Tower approved (quarantine -> active) at approvedAt.
func approvedTower(t *testing.T, b *broker, srv *httptest.Server, login string, approvedAt time.Time) linkTower {
	t.Helper()
	op := signedInOperator(t, b, login)
	lt := enrolledTower(t, b, op.login)
	require.NoError(t, b.tower.registry.TransitionAs(lt.id, admit.StateActive, admit.Event{Kind: evLifecycle,
		Actor: "admin:broker-key", At: approvedAt}))
	return lt
}

func stageEvents(t *testing.T, b *broker, id string) []admit.Event {
	return towerEventsOf(t, b, id, evTrustStage)
}

func passChecks(t *testing.T, b *broker, id string, n int, from time.Time) {
	t.Helper()
	for i := 0; i < n; i++ {
		require.NoError(t, b.tower.health.Record(id, true, from.Add(time.Duration(i)*time.Minute)))
	}
}

func TestANewlyApprovedTowerStartsAtNew(t *testing.T) {
	b, srv := towerTestBroker(t)
	approved := time.Now().Add(-time.Hour)
	lt := approvedTower(t, b, srv, "nora", approved)
	b.towerGovSweepOnce(time.Now())
	evs := stageEvents(t, b, lt.id)
	require.Len(t, evs, 1)
	require.Equal(t, admit.State("new"), evs[0].To)
	require.Equal(t, admit.State(""), evs[0].From)
	require.Equal(t, admit.ActorSystem, evs[0].Actor)
	require.Equal(t, placement.StageNew, b.towerStage(lt.id))

	b.towerGovSweepOnce(time.Now())
	require.Len(t, stageEvents(t, b, lt.id), 1, "an unchanged stage writes nothing")
}

func TestAQuarantinedOrHouseTowerHasNoStage(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "q")
	q := enrolledTower(t, b, op.login)
	house := approvedTower(t, b, srv, "roger-house", time.Now().Add(-time.Hour))
	makeHouse(t, b, "roger-house")
	b.towerGovSweepOnce(time.Now())
	require.Empty(t, stageEvents(t, b, q.id), "never approved")
	require.Empty(t, stageEvents(t, b, house.id), "house Towers have no stage")
}

func TestPromotionIsComputedFromTheRecord(t *testing.T) {
	b, srv := towerTestBroker(t)
	now := time.Now()
	lt := approvedTower(t, b, srv, "nora", now.Add(-8*24*time.Hour))
	b.towerGovSweepOnce(now.Add(-8*24*time.Hour + time.Minute)) // "new" entered at approval
	passChecks(t, b, lt.id, 60, now.Add(-7*24*time.Hour))
	b.towerGovSweepOnce(now)
	evs := stageEvents(t, b, lt.id)
	require.Equal(t, admit.State("proving"), evs[0].To)
	require.Equal(t, admit.State("new"), evs[0].From)
	require.EqualValues(t, 60, evs[0].Detail["passed"])
	require.NotNil(t, evs[0].Detail["days"])
	require.NotNil(t, evs[0].Detail["pass_rate"])
}

func TestASuspensionDemotesAtOnce(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-time.Hour))
	setStage(t, b, lt.id, placement.StageEstablished)
	_, err := b.tower.registry.TransitionRecorded(lt.id, admit.StateSuspended, admit.Event{Kind: evLifecycle, Actor: admit.ActorSystem, Reason: "reputation"})
	require.NoError(t, err)
	b.logTowerEvent(admit.Event{TowerID: lt.id, Kind: evLifecycle, To: admit.StateSuspended}) // the broker logs every write it makes
	require.Equal(t, placement.StageNew, b.towerStage(lt.id), "the reader demotes before any sweep")

	// A PEER's write is seen within the cache's lifetime, not later.
	require.Less(t, stageCacheTTL, time.Minute)

	b.towerGovSweepOnce(time.Now())
	evs := stageEvents(t, b, lt.id)
	require.Equal(t, admit.State("new"), evs[0].To)
	require.Equal(t, admit.State("established"), evs[0].From)
	require.Equal(t, "suspended", evs[0].Reason)
}

func TestHealthChecksFailingTwiceDemote(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-time.Hour))
	setStage(t, b, lt.id, placement.StageProving)
	b.recordHealthResult(lt.id, false, time.Now())
	require.Equal(t, placement.StageProving, b.towerStage(lt.id), "one failure is a blip")
	b.recordHealthResult(lt.id, false, time.Now().Add(time.Second))
	require.Len(t, towerEventsOf(t, b, lt.id, evHealthFailing), 1)
	require.Equal(t, placement.StageNew, b.towerStage(lt.id))
	b.recordHealthResult(lt.id, false, time.Now().Add(2*time.Second))
	require.Len(t, towerEventsOf(t, b, lt.id, evHealthFailing), 1, "one event per streak, not per failure")
}

func TestALeaseLapseLongerThanAnHourDemotes(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-3*time.Hour))
	setStageAt(t, b, lt.id, placement.StageProving, time.Now().Add(-3*time.Hour))
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evLeaseLapsed, Actor: admit.ActorSystem,
		At: time.Now().Add(-2 * time.Hour)}))
	b.towerGovSweepOnce(time.Now())
	require.Equal(t, placement.StageNew, b.towerStage(lt.id))
	require.Equal(t, "lease_lapsed", stageEvents(t, b, lt.id)[0].Reason)
}

func TestALeaseLapseRecoveredWithinAnHourDoesNotDemote(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-3*time.Hour))
	setStageAt(t, b, lt.id, placement.StageProving, time.Now().Add(-3*time.Hour))
	lapse := time.Now().Add(-2 * time.Hour)
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evLeaseLapsed, Actor: admit.ActorSystem, At: lapse}))
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evCertRenewed, Actor: admit.ActorTower, At: lapse.Add(20 * time.Minute)}))
	b.towerGovSweepOnce(time.Now())
	require.Equal(t, placement.StageProving, b.towerStage(lt.id))
}

func TestAnAdminExcusalRestoresAStageLostToCoresFault(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-10*24*time.Hour))
	setStageAt(t, b, lt.id, placement.StageProving, time.Now().Add(-5*24*time.Hour))
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evLeaseLapsed, Actor: admit.ActorSystem,
		At: time.Now().Add(-26 * time.Hour)}))
	b.towerGovSweepOnce(time.Now())
	require.Equal(t, placement.StageNew, b.towerStage(lt.id))
	lapse := towerEventsOf(t, b, lt.id, evLeaseLapsed)[0]

	// A blank or general excusal is refused.
	code, raw := adminTowerPost(t, srv, "/tower/excuse", `{"tower_id":"`+lt.id+`","reason":"our bug"}`)
	require.Equal(t, http.StatusBadRequest, code, raw)
	code, raw = adminTowerPost(t, srv, "/tower/excuse", `{"tower_id":"`+lt.id+`","event_id":`+strconv.FormatInt(lapse.ID, 10)+`}`)
	require.Equal(t, http.StatusBadRequest, code, raw)
	code, raw = adminTowerPost(t, srv, "/tower/excuse", `{"tower_id":"`+lt.id+`","event_id":999999,"reason":"x"}`)
	require.Equal(t, http.StatusNotFound, code, raw)

	code, raw = adminTowerPost(t, srv, "/tower/excuse", `{"tower_id":"`+lt.id+`","event_id":`+
		strconv.FormatInt(lapse.ID, 10)+`,"reason":"renewal signed with the wrong key (Core defect, fixed in v7.0.1)"}`)
	require.Equal(t, http.StatusOK, code, raw)
	ex := towerEventsOf(t, b, lt.id, evExcusal)
	require.Len(t, ex, 1)
	require.Equal(t, "admin:broker-key", ex[0].Actor)

	b.towerGovSweepOnce(time.Now())
	require.Equal(t, placement.StageProving, b.towerStage(lt.id), "the excused lapse no longer demotes")
	got := stageEvents(t, b, lt.id)[0]
	require.Equal(t, "excused", got.Reason)
}

func TestAnAdminIncidentDemotes(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-time.Hour))
	setStage(t, b, lt.id, placement.StageEstablished)
	code, raw := adminTowerPost(t, srv, "/tower/incident", `{"tower_id":"`+lt.id+`"}`)
	require.Equal(t, http.StatusBadRequest, code, "an incident needs a reason: %s", raw)
	code, raw = adminTowerPost(t, srv, "/tower/incident", `{"tower_id":"`+lt.id+`","reason":"served stale weights"}`)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, placement.StageNew, b.towerStage(lt.id))
}

func TestAnAdminCeilingHoldsAStageButNeverRaisesIt(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-time.Hour))
	setStage(t, b, lt.id, placement.StageEstablished)
	code, raw := adminTowerPost(t, srv, "/tower/stage-ceiling", `{"tower_id":"`+lt.id+`","stage":"proving","reason":"hold while we look"}`)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, placement.StageProving, b.towerStage(lt.id))
	code, _ = adminTowerPost(t, srv, "/tower/stage-ceiling", `{"tower_id":"`+lt.id+`","stage":"gold","reason":"x"}`)
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = adminTowerPost(t, srv, "/tower/stage-ceiling", `{"tower_id":"`+lt.id+`","stage":"established"}`)
	require.Equal(t, http.StatusBadRequest, code, "a reason is required")
	// Lifting the ceiling to the top never raises past the record.
	code, _ = adminTowerPost(t, srv, "/tower/stage-ceiling", `{"tower_id":"`+lt.id+`","stage":"established","reason":"done"}`)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, placement.StageEstablished, b.towerStage(lt.id))
}

func TestAdminStageRoutesRequireTheAdmin(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "real"
	for _, p := range []string{"/tower/excuse", "/tower/incident", "/tower/stage-ceiling"} {
		code, _ := adminTowerPost(t, srv, p, `{"tower_id":"x","reason":"y","stage":"new","event_id":1}`)
		require.Equal(t, http.StatusForbidden, code, p)
	}
}

func setStageAt(t *testing.T, b *broker, towerID string, st placement.Stage, at time.Time) {
	t.Helper()
	ev := admit.Event{TowerID: towerID, Kind: evTrustStage, Actor: admit.ActorSystem, To: admit.State(st), At: at}
	require.NoError(t, b.tower.registry.Record(ev))
	b.dropStageCache(ev)
}

func TestTheEdgeWeightScalesTheTowerSideOfTheCoin(t *testing.T) {
	// Equal capacity on both sides: weight w makes the Tower side win w/(1+w) of the time.
	for _, w := range []float64{0.25, 0.5, 1.0} {
		wins := 0
		for i := 0; i < 20000; i++ {
			if edgeCoinWeighted("req-"+strconv.Itoa(i), 4, 4, w) {
				wins++
			}
		}
		want := w / (1 + w)
		require.InDelta(t, want, float64(wins)/20000, 0.015, "weight %v", w)
	}
	// Weight 1 is exactly the unweighted coin, request for request.
	for i := 0; i < 500; i++ {
		id := "r" + strconv.Itoa(i)
		require.Equal(t, edgeCoin(id, 3, 5), edgeCoinWeighted(id, 3, 5, 1.0))
	}
}

func TestEachTowerCarriesItsStagesWeight(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-time.Hour))
	setStage(t, b, lt.id, placement.StageNew)
	require.Equal(t, 0.25, b.towerEdgeWeight(lt.id))
	setStage(t, b, lt.id, placement.StageProving)
	require.Equal(t, 0.5, b.towerEdgeWeight(lt.id))
	setStage(t, b, lt.id, placement.StageEstablished)
	require.Equal(t, 1.0, b.towerEdgeWeight(lt.id))
	house := approvedTower(t, b, srv, "roger-house", time.Now().Add(-time.Hour))
	makeHouse(t, b, "roger-house")
	require.Equal(t, 1.0, b.towerEdgeWeight(house.id))
}

func TestTheTowerSideWeightIsCapacityWeighted(t *testing.T) {
	rows := []attemptCand{
		{edge: &edgeCand{row: fleetRow("tw-a", "n1")}},
		{edge: &edgeCand{row: fleetRow("tw-b", "n2")}},
	}
	caps := map[string]int{"n1": 3, "n2": 1}
	weights := map[string]float64{"tw-a": 0.25, "tw-b": 1.0}
	got := edgeSideWeight(rows, func(n string) int { return caps[n] }, func(tw string) float64 { return weights[tw] })
	require.InDelta(t, (3*0.25+1*1.0)/4, got, 1e-9)
	require.Equal(t, 1.0, edgeSideWeight(nil, nil, nil), "no rows: no weighting")
}

func fleetRow(towerID, nodeID string) fleet.Station {
	return fleet.Station{TowerID: towerID, NodeID: nodeID}
}

func TestTheOperatorSeesTheirStageAndWhatIsLeft(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "nora")
	lt := enrolledTower(t, b, op.login)
	require.NoError(t, b.tower.registry.TransitionAs(lt.id, admit.StateActive, admit.Event{Kind: evLifecycle,
		Actor: "admin:broker-key", At: time.Now().Add(-2 * 24 * time.Hour)}))
	b.towerGovSweepOnce(time.Now())
	passChecks(t, b, lt.id, 10, time.Now().Add(-time.Hour))

	var status struct {
		Towers []map[string]any `json:"towers"`
	}
	code, raw := op.call(t, srv, http.MethodGet, "/tower/status", nil, &status)
	require.Equal(t, http.StatusOK, code, raw)
	require.Len(t, status.Towers, 1)
	trust := status.Towers[0]["trust"].(map[string]any)
	require.Equal(t, "new", trust["stage"])
	require.EqualValues(t, 10, trust["community_stations"])
	require.EqualValues(t, 0, trust["house_stations"])
	require.Equal(t, "5 more days, 40 more passed health checks", trust["next"])
	require.NotContains(t, raw, "excusal", "the record itself stays admin only")
}

func TestTheAdminSeesTheStageAndItsEvidence(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-2*24*time.Hour))
	b.towerGovSweepOnce(time.Now())
	passChecks(t, b, lt.id, 9, time.Now().Add(-time.Hour))
	require.NoError(t, b.tower.health.Record(lt.id, false, time.Now()))
	d := adminTowerDetailOf(t, srv, lt.id)
	st := d["trust"].(map[string]any)
	require.Equal(t, "new", st["stage"])
	require.EqualValues(t, 9, st["passed"])
	require.EqualValues(t, 1, st["failed"])
	require.Equal(t, "0.900", st["pass_rate"])
	require.NotEmpty(t, st["entered_at"])
	require.Equal(t, "5 more days, 41 more passed health checks", st["next"])
}

// REVIEW FINDING: the stage was read from the newest 500 events, so a busy Tower lost it.
func TestABusyRecordNeverPushesTheStageOutOfView(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-40*24*time.Hour))
	setStageAt(t, b, lt.id, placement.StageProving, time.Now().Add(-10*24*time.Hour))
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evStageCeiling,
		Actor: "admin:broker-key", To: "proving", Reason: "hold", At: time.Now().Add(-9 * 24 * time.Hour)}))
	for i := 0; i < 700; i++ {
		require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evStationPlaced,
			Actor: admit.ActorSystem, At: time.Now().Add(-time.Duration(700-i) * time.Minute)}))
	}
	b.dropStageCache(admit.Event{TowerID: lt.id, Kind: evTrustStage})
	require.Equal(t, placement.StageProving, b.towerStage(lt.id))
	rep := b.stageReport(mustTower(t, b, lt.id), time.Now())
	require.Equal(t, "proving", rep["ceiling"], "the ceiling is still seen")
}

// REVIEW FINDING: excusing one of two disqualifiers erased the other.
func TestExcusingOneOfTwoDisqualifiersDoesNotUndoTheDemotion(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-20*24*time.Hour))
	setStageAt(t, b, lt.id, placement.StageProving, time.Now().Add(-10*24*time.Hour))
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evIncident, Actor: "admin:broker-key",
		Reason: "first", At: time.Now().Add(-2 * time.Hour)}))
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evIncident, Actor: "admin:broker-key",
		Reason: "second", At: time.Now().Add(-time.Hour)}))
	b.towerGovSweepOnce(time.Now())
	demo := stageEvents(t, b, lt.id)[0]
	require.Len(t, eventIDs(demo.Detail["cause_events"]), 2, "both causes are named")
	entered, _ := timeOf(demo.Detail["entered_at"])
	require.WithinDuration(t, time.Now().Add(-time.Hour), entered, time.Minute, "the new clock starts at the latest cause")

	first := towerEventsOf(t, b, lt.id, evIncident)[1] // newest first
	require.Equal(t, "first", first.Reason)
	code, raw := adminTowerPost(t, srv, "/tower/excuse", `{"tower_id":"`+lt.id+`","event_id":`+strconv.FormatInt(first.ID, 10)+`,"reason":"ours"}`)
	require.Equal(t, http.StatusOK, code, raw)
	b.towerGovSweepOnce(time.Now())
	require.Equal(t, placement.StageNew, b.towerStage(lt.id), "the second cause still stands")

	second := towerEventsOf(t, b, lt.id, evIncident)[0]
	code, raw = adminTowerPost(t, srv, "/tower/excuse", `{"tower_id":"`+lt.id+`","event_id":`+strconv.FormatInt(second.ID, 10)+`,"reason":"ours too"}`)
	require.Equal(t, http.StatusOK, code, raw)
	b.towerGovSweepOnce(time.Now())
	require.Equal(t, placement.StageProving, b.towerStage(lt.id), "only when every cause is excused")
}

// REVIEW FINDING: every instance's sweep wrote its own stage events.
func TestTwoInstancesSweepingTogetherWriteOneStageChange(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-time.Hour))
	other := &broker{tower: b.tower, db: b.db, instanceID: "i-2", stationLimitExempt: b.stationLimitExempt}
	tw := mustTower(t, b, lt.id)
	v, err := b.readStage(lt.id, time.Now()) // both read the same record...
	require.NoError(t, err)
	require.False(t, v.hasLast)
	b.sweepStage(tw, time.Now())
	other.sweepStage(tw, time.Now()) // ...and the second write loses
	require.Len(t, stageEvents(t, b, lt.id), 1)
}

func mustTower(t *testing.T, b *broker, id string) admit.Tower {
	t.Helper()
	tw, ok := b.tower.registry.Get(id)
	require.True(t, ok)
	return tw
}

func TestARevokedTowerHasNoStage(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-time.Hour))
	b.towerGovSweepOnce(time.Now())
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateRevoked))
	require.Nil(t, b.stageReport(mustTower(t, b, lt.id), time.Now()))
}

// REVIEW FINDING: the coin asked for a Tower's weight once per row, each a record scan.
func TestTheCoinAsksEachTowerForItsWeightOnce(t *testing.T) {
	rows := []attemptCand{
		{edge: &edgeCand{row: fleetRow("tw-a", "n1")}}, {edge: &edgeCand{row: fleetRow("tw-a", "n2")}},
		{edge: &edgeCand{row: fleetRow("tw-a", "n3")}}, {edge: &edgeCand{row: fleetRow("tw-b", "n4")}},
	}
	asked := map[string]int{}
	edgeSideWeight(rows, func(string) int { return 1 }, func(tw string) float64 { asked[tw]++; return 1 })
	require.Equal(t, map[string]int{"tw-a": 1, "tw-b": 1}, asked)
}

func TestAStageIsCachedOnTheRequestPath(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-time.Hour))
	setStage(t, b, lt.id, placement.StageProving)
	// A peer's write (straight to the record, no local drop) is not seen until the entry ages out.
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evTrustStage, Actor: admit.ActorSystem, To: "established"}))
	require.Equal(t, placement.StageProving, b.towerStage(lt.id))
	b.stageMu.Lock()
	c := b.stageCache[lt.id]
	c.at = c.at.Add(-stageCacheTTL)
	b.stageCache[lt.id] = c
	b.stageMu.Unlock()
	require.Equal(t, placement.StageEstablished, b.towerStage(lt.id))
}

// REVIEW FINDING: health_failing fired only at exactly two, so a count that skipped (two
// instances recording at once) lost it; and its pacing could drop a demotion cause.
func TestHealthFailingIsWrittenOncePerRunEvenIfTheCountSkips(t *testing.T) {
	b, _ := towerTestBroker(t)
	now := time.Now()
	for i := 0; i < 3; i++ {
		require.NoError(t, b.tower.health.Record("tw-1", false, now.Add(time.Duration(i)*time.Second)))
	}
	b.noteHealthStreak("tw-1") // first seen at three
	b.noteHealthStreak("tw-1")
	require.Len(t, towerEventsOf(t, b, "tw-1", evHealthFailing), 1)
	require.False(t, pacedEvent(admit.Event{Kind: evHealthFailing, Actor: admit.ActorSystem}), "a demotion cause is never dropped by the budget")

	// A pass ends the run; the next run is a new event.
	require.NoError(t, b.tower.health.Record("tw-1", true, now.Add(10*time.Second)))
	require.NoError(t, b.tower.health.Record("tw-1", false, now.Add(20*time.Second)))
	require.NoError(t, b.tower.health.Record("tw-1", false, now.Add(30*time.Second)))
	b.noteHealthStreak("tw-1")
	require.Len(t, towerEventsOf(t, b, "tw-1", evHealthFailing), 2)
}

// AUDIT FINDING: an operator's own drain/resume events could fill the bounded read and hide a
// later suspension.
func TestDrainResumeTogglingCannotHideASuspension(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-100*24*time.Hour))
	setStageAt(t, b, lt.id, placement.StageEstablished, time.Now().Add(-90*24*time.Hour))
	base := time.Now().Add(-80 * 24 * time.Hour)
	for i := 0; i < stageScanLimit+50; i++ {
		to := admit.StateDraining
		if i%2 == 1 {
			to = admit.StateActive
		}
		require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evLifecycle,
			Actor: "operator:nora", To: to, At: base.Add(time.Duration(i) * time.Minute)}))
	}
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evLifecycle,
		Actor: admit.ActorSystem, To: admit.StateSuspended, Reason: "reputation"}))
	b.dropStageCache(admit.Event{TowerID: lt.id, Kind: evLifecycle})
	require.Equal(t, placement.StageNew, b.towerStage(lt.id))
}

// AUDIT FINDING: a disqualifier landing between the sweep's read and its demotion write was
// left out of the causes, so excusing the listed ones restored the stage.
func TestADisqualifierAfterTheLatestListedCauseStillCounts(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-20*24*time.Hour))
	setStageAt(t, b, lt.id, placement.StageProving, time.Now().Add(-10*24*time.Hour))
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evIncident, Actor: "admin:broker-key",
		Reason: "listed", At: time.Now().Add(-2 * time.Hour)}))
	b.towerGovSweepOnce(time.Now())
	listed := towerEventsOf(t, b, lt.id, evIncident)[0]
	// The racing one: after the listed cause, before the demotion's write time.
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evIncident, Actor: "admin:broker-key",
		Reason: "racing", At: time.Now().Add(-time.Hour)}))
	code, raw := adminTowerPost(t, srv, "/tower/excuse", `{"tower_id":"`+lt.id+`","event_id":`+strconv.FormatInt(listed.ID, 10)+`,"reason":"ours"}`)
	require.Equal(t, http.StatusOK, code, raw)
	b.towerGovSweepOnce(time.Now())
	require.Equal(t, placement.StageNew, b.towerStage(lt.id), "the unlisted cause still stands")
}

type stageReadFailing struct {
	admit.Store
	calls int
}

func (s *stageReadFailing) FindEvent(string, []string, bool) (admit.Event, bool, error) {
	s.calls++
	return admit.Event{}, false, admit.ErrUnavailable
}

// AUDIT FINDING: a failed read was not cached, so every request repeated it during an outage.
func TestAnUnreadableStageIsRememberedBriefly(t *testing.T) {
	fs := &stageReadFailing{Store: admit.NewMemStore()}
	b, _ := towerTestBrokerWithAdmitStore(t, fs)
	for i := 0; i < 10; i++ {
		require.Equal(t, placement.StageNew, b.towerStage("tw-x"))
	}
	require.Equal(t, 1, fs.calls, "asked once, not per request")
	b.stageMu.Lock()
	c := b.stageCache["tw-x"]
	b.stageMu.Unlock()
	require.WithinDuration(t, time.Now().Add(stageFailCacheTTL-stageCacheTTL), c.at, time.Second, "remembered for the short failure window only")
}

// AUDIT FINDING: a lapse inside its grace hour was skipped forever if a stage was entered in
// that hour.
func TestALapseJustBeforeAPromotionStillCountsIfNeverRenewed(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := approvedTower(t, b, srv, "nora", time.Now().Add(-20*24*time.Hour))
	lapse := time.Now().Add(-90 * time.Minute)
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evLeaseLapsed, Actor: admit.ActorSystem, At: lapse}))
	setStageAt(t, b, lt.id, placement.StageProving, lapse.Add(20*time.Minute)) // promoted inside the grace
	b.towerGovSweepOnce(time.Now())
	require.Equal(t, placement.StageNew, b.towerStage(lt.id), "the unrenewed lapse counts")
}
