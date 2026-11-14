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
	require.NoError(t, b.tower.registry.TransitionAs(lt.id, admit.StateSuspended, admit.Event{Kind: evLifecycle, Actor: admit.ActorSystem, Reason: "reputation"}))
	require.Equal(t, placement.StageNew, b.towerStage(lt.id), "the reader demotes before any sweep")

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
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: towerID, Kind: evTrustStage,
		Actor: admit.ActorSystem, To: admit.State(st), At: at}))
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
