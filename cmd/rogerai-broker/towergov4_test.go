package main

// towergov4_test.go: the second pre-push audit's findings, pinned as regressions
// (features/tower/governance_record.feature).

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
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
)

// failingMirror is a shared session view that cannot be read.
type failingMirror struct{}

func (failingMirror) Put(string, link.Record) error { return errors.New("down") }
func (failingMirror) Get(string) (link.Record, bool, error) {
	return link.Record{}, false, errors.New("down")
}
func (failingMirror) Del(string, string) error             { return errors.New("down") }
func (failingMirror) All() (map[string]link.Record, error) { return nil, errors.New("down") }

// An instance that cannot read the shared session view must not declare a live Tower gone:
// the event is permanent, and the Tower may be heartbeating to the other instance.
func TestTheStaleSweepSkipsATowerItCannotSee(t *testing.T) {
	shared := admit.NewMemStore()
	b := testBrokerWithDB(store.NewMem())
	ts, err := newTowerSubsystem(b, shared, cert.NewMemCustody(), enroll.NewMemStore(), cert.Config{TTL: time.Hour},
		linkDeps{stations: attach.NewMemStore(), heads: head.NewMemStore(), mirror: failingMirror{}})
	require.NoError(t, err)
	b.tower = ts
	lt := enrolledTower(t, b, "owner-1")
	// Another instance recorded the open.
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evSessionOpened, Actor: admit.ActorTower,
		At: time.Now().Add(-time.Hour)}))

	b.towerGovSweepOnce(time.Now().Add(time.Hour))
	require.Empty(t, towerEventsOf(t, b, lt.id, "session_stale"), "unknown is not gone")
}

// A heartbeat newer than the open, seen through the shared view, keeps the Tower fresh: the
// sweep judges the newest sign of life, not the time the session opened.
func TestTheStaleSweepJudgesTheNewestSignOfLife(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	_ = openSessionOf(t, srv.URL, lt, func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) })
	b.towerGovSweepOnce(time.Now().Add(towerFreshnessWindow / 2))
	require.Empty(t, towerEventsOf(t, b, lt.id, "session_stale"))
}

// AN OPERATOR CANNOT LOOP THEIR OWN TOWER'S STATE to grow the record: past the hourly budget
// the change itself is refused, so the record stays transactional and bounded.
func TestAnOperatorLoopingLifecycleChangesIsRefused(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"active"}`)

	var refused bool
	for i := 0; i < towerEventBudget+5; i++ {
		to := "draining"
		if i%2 == 1 {
			to = "active"
		}
		code, _ := op.call(t, srv, http.MethodPost, "/tower/self/lifecycle", map[string]string{"tower_id": lt.id, "state": to}, nil)
		if code == http.StatusTooManyRequests {
			refused = true
			break
		}
		require.Equal(t, http.StatusOK, code)
	}
	require.True(t, refused, "the loop is stopped")
	var mine int
	for _, ev := range towerEventsOf(t, b, lt.id, "lifecycle") {
		if ev.Actor == "operator:octocat" {
			mine++
		}
	}
	require.LessOrEqual(t, mine, towerEventBudget)
}

func TestRefusedUnsignedAttachesArePacedInTheLog(t *testing.T) {
	b, srv := towerTestBroker(t)
	var buf bytes.Buffer
	restore := captureLog(&buf)
	for i := 0; i < 10; i++ {
		resp, err := http.Post(srv.URL+"/tower/edge/attach", "application/json", strings.NewReader(`{}`))
		require.NoError(t, err)
		resp.Body.Close()
	}
	restore()
	require.Equal(t, 1, strings.Count(buf.String(), "kind=station_attach_refused"), "one line a minute for unsigned callers")
	_ = b
}

// An unsigned request costs no store read to log, and is paced as one stream.
func TestUnsignedTowerRefusalsArePacedAsOneStream(t *testing.T) {
	counting := &eventCountingStore{Store: admit.NewMemStore()}
	b, srv := towerTestBrokerWithAdmitStore(t, counting)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	var buf bytes.Buffer
	restore := captureLog(&buf)
	for i := 0; i < 10; i++ {
		resp, err := http.Post(srv.URL+"/tower/session", "application/json", bytes.NewReader(jsonOf(t, helloFor(lt))))
		require.NoError(t, err)
		resp.Body.Close()
	}
	restore()
	require.Equal(t, 1, strings.Count(buf.String(), "reason=unsigned"))
	require.Len(t, towerEventsOf(t, b, lt.id, "request_refused"), 1)
}

func TestAStationPromotionCarriesTheReason(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	attachStation(t, b, "st-q", lt.id, ownerPubkeyOf(t, b, op.login))
	_, err := b.tower.stationStore.SetState("st-q", attach.StateQuarantine)
	require.NoError(t, err)
	b.adminKey = "admin-secret"
	code, raw := adminTowerPost(t, srv, "/tower/station/promote", `{"station_id":"st-q","reason":"probes clean"}`)
	require.Equal(t, http.StatusOK, code, raw)
	evs := towerEventsOf(t, b, lt.id, "station_promoted")
	require.Len(t, evs, 1)
	require.Equal(t, "probes clean", evs[0].Reason)
}

// A flush defers to a record another instance already wrote within the hour.
func TestAFlushDefersToARefusalAlreadyRecordedThisHour(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateSuspended))
	for i := 0; i < 3; i++ {
		_, _ = lt.call(t, srv, "/tower/session/heartbeat", jsonOf(t, frameFor(lt, "s")), nil)
	}
	b.towerRefusals.shift(time.Hour + time.Minute)
	// A peer recorded the same reason a moment ago.
	now := b.towerRefusals.clock()
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evRequestRefused, Actor: admit.ActorTower,
		Reason: "state_suspended", Instance: "peer", At: now.Add(-time.Minute)}))
	b.towerGovSweepOnce(now)
	n, err := b.tower.registry.CountEvents(lt.id, evRequestRefused, "state_suspended", "", now.Add(-2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 2, n, "the first one here and the peer's; the flush waits")
}

// The governance sweep runs on its own, whether or not the deployment has a Station store.
func TestTheGovernanceSweepDoesNotDependOnTheStationStore(t *testing.T) {
	b, _ := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.ExpireLease(lt.id))
	b.tower.stationStore = nil
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { b.towerGovSweep(stop, time.Millisecond); close(done) }()
	require.Eventually(t, func() bool { return len(towerEventsOf(t, b, lt.id, "lease_lapsed")) == 1 },
		2*time.Second, 5*time.Millisecond)
	close(stop)
	<-done
}
