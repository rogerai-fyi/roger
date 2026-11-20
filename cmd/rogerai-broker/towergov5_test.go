package main

// towergov5_test.go: the third pre-push audit's findings, pinned as regressions
// (features/tower/governance_record.feature).

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/reputation"
)

// Log pacing must never become a record. A burst of refused attaches, then a flush, writes
// nothing - in particular no event for a Tower called "attach".
func TestRefusedAttachPacingNeverFlushesIntoTheRecord(t *testing.T) {
	b, srv := towerTestBroker(t)
	for i := 0; i < 5; i++ {
		resp, err := http.Post(srv.URL+"/tower/edge/attach", "application/json", strings.NewReader(`{}`))
		require.NoError(t, err)
		resp.Body.Close()
	}
	b.towerRefusals.shift(2 * time.Hour)
	b.towerGovSweepOnce(time.Now())
	for _, id := range []string{"attach", "unsigned", "unknown_tower", "budget"} {
		require.Empty(t, towerEventsOf(t, b, id), id)
	}
}

// The budget caps what a Tower or an operator drives. An operator's refused requests must
// not use up the room that a system or administrator refusal needs.
func TestAnOperatorCannotCrowdOutSystemOrAdminRecords(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	for i := 0; i < towerEventBudget+3; i++ {
		_, _ = op.call(t, srv, http.MethodPost, "/tower/self/lifecycle", map[string]string{"tower_id": lt.id, "state": "active"}, nil)
	}
	// An administrator's refused change still lands.
	code, _ := adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"quarantine"}`)
	require.Equal(t, http.StatusConflict, code)
	var admin int
	for _, ev := range towerEventsOf(t, b, lt.id, "lifecycle_refused") {
		if ev.Actor == "admin:broker-key" {
			admin++
		}
	}
	require.Equal(t, 1, admin)
	require.True(t, pacedEvent(admit.Event{Kind: evLifecycleRefused, Actor: "operator:octocat"}))
	require.False(t, pacedEvent(admit.Event{Kind: evLifecycleRefused, Actor: admit.ActorSystem}))
	require.False(t, pacedEvent(admit.Event{Kind: evLifecycleRefused, Actor: "admin:gh:luis"}))
	require.True(t, pacedEvent(admit.Event{Kind: evSessionOpened, Actor: admit.ActorTower}))
}

// A closed session is not "gone quiet", even when its close was over the budget and not
// recorded: the sweep asks the link whether a session is there at all.
func TestAClosedSessionIsNeverCalledStale(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	acc := openSessionOf(t, srv.URL, lt, call)
	// The close happens but is not recorded (as when the budget is spent).
	require.True(t, b.tower.link.Close(acc.SessionID, lt.id))
	b.towerGovSweepOnce(time.Now().Add(towerFreshnessWindow + time.Minute))
	require.Empty(t, towerEventsOf(t, b, lt.id, "session_stale"))
}

// An admitted Tower's unsigned refusals are counted for that Tower, not lost in one global
// stream with everyone else's noise.
func TestUnsignedRefusalsOfAnAdmittedTowerAreCountedForIt(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	ghost := lt
	ghost.id = "tw-noise"
	b.towerGovSweepOnce(time.Now()) // learns which Towers are admitted
	post := func(id string) {
		h := helloFor(lt)
		h.TowerID = id
		resp, err := http.Post(srv.URL+"/tower/session", "application/json", bytes.NewReader(jsonOf(t, h)))
		require.NoError(t, err)
		resp.Body.Close()
	}
	post("tw-noise") // noise first, using up any shared slot
	for i := 0; i < 4; i++ {
		post(lt.id)
	}
	evs := towerEventsOf(t, b, lt.id, "request_refused")
	require.Len(t, evs, 1, "the admitted Tower's own refusal is recorded despite the noise")
	require.Equal(t, "unsigned", evs[0].Reason)
	b.towerRefusals.shift(time.Hour + time.Minute)
	b.towerGovSweepOnce(time.Now())
	evs = towerEventsOf(t, b, lt.id, "request_refused")
	require.Len(t, evs, 2)
	require.EqualValues(t, 3, evs[0].Detail["count"], "every one is counted")
	require.Empty(t, towerEventsOf(t, b, "tw-noise"))
}

func TestAPromotionWhoseStationCannotBeReadIsRefused(t *testing.T) {
	b, srv := towerTestBrokerWithStations(t, &promoteFailingStations{})
	b.tower.stationStore.(*promoteFailingStations).failReads = true
	b.adminKey = "admin-secret"
	code, _ := adminTowerPost(t, srv, "/tower/station/promote", `{"station_id":"st-x"}`)
	require.Equal(t, http.StatusServiceUnavailable, code, "no promotion without its record")
}

func TestACanaryFailureCarriesItsCause(t *testing.T) {
	b, _ := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")
	var buf bytes.Buffer
	restore := captureLog(&buf)
	b.noteCanaryCause(lt.id, "st-1", reputation.CanaryFail, "receipt_mismatch")
	restore()
	evs := towerEventsOf(t, b, lt.id, "canary")
	require.Len(t, evs, 1)
	require.Equal(t, "receipt_mismatch", evs[0].Detail["cause"])
	require.Contains(t, buf.String(), "cause=receipt_mismatch")
}

func TestTheRefusalLimiterForgetsQuietKeys(t *testing.T) {
	var l refusalLimiter
	for i := 0; i < 100; i++ {
		l.note("k" + strings.Repeat("x", i))
	}
	l.shift(2 * time.Hour)
	_ = l.due()
	l.prune()
	l.mu.Lock()
	defer l.mu.Unlock()
	require.Empty(t, l.logged)
	require.Empty(t, l.recorded)
	require.Empty(t, l.counts)
}
