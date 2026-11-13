package main

// towergov3_test.go: the pre-push audit's findings on the governance record, each pinned
// as a regression (features/tower/governance_record.feature).

import (
	"bytes"
	"net/http"
	"net/http/httptest"
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
)

// A close that ended nothing must not read as a close: it would hide the live session from
// the "went quiet" sweep and tell the admin the Tower closed when it did not.
func TestACloseNamingAStaleSessionIsNotRecordedAsAClose(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	old := openSessionOf(t, srv.URL, lt, call)
	_ = openSessionOf(t, srv.URL, lt, call) // replaces it

	code, _ := call("/tower/session/close", jsonOf(t, frameFor(lt, old.SessionID)), nil)
	require.Equal(t, http.StatusOK, code, "the answer is unchanged")
	require.Empty(t, towerEventsOf(t, b, lt.id, "session_closed"))

	b.towerGovSweepOnce(time.Now().Add(towerFreshnessWindow + time.Minute))
	require.Len(t, towerEventsOf(t, b, lt.id, "session_stale"), 1, "the live session can still go quiet")
}

func TestAReconnectNamesTheSessionItReplaced(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	first := openSessionOf(t, srv.URL, lt, call)
	_ = openSessionOf(t, srv.URL, lt, call)
	evs := towerEventsOf(t, b, lt.id, "session_opened")
	require.Equal(t, first.SessionID[:8], evs[0].Detail["replaced_session"])
}

// ONE TOWER CANNOT GROW THE RECORD WITHOUT BOUND. Tower- and operator-driven kinds are
// capped per Tower per kind per hour, across instances (counted in the shared store).
func TestATowerReconnectingInALoopCannotFloodTheRecord(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }

	var buf bytes.Buffer
	restore := captureLog(&buf)
	for i := 0; i < towerEventBudget+15; i++ {
		_ = openSessionOf(t, srv.URL, lt, call)
	}
	restore()
	require.Len(t, towerEventsOf(t, b, lt.id, "session_opened"), towerEventBudget)
	require.Contains(t, buf.String(), "budget reached", "the cap says so in the log, once a minute")
	require.Equal(t, 1, strings.Count(buf.String(), "budget reached"))
}

// The cap never applies to what Core or an administrator decides: those are never paced.
func TestAdministratorAndSystemEventsAreNeverCapped(t *testing.T) {
	require.False(t, pacedKind(evLifecycle))
	require.False(t, pacedKind(evSessionStale))
	require.False(t, pacedKind(evLeaseLapsed))
	require.False(t, pacedKind(evCertRevoked))
	for _, k := range []string{evSessionOpened, evSessionClosed, evSessionRefused, evLifecycleRefused,
		evStationAttached, evStationRevoked, evCertRenewed} {
		require.True(t, pacedKind(k), k)
	}
}

func TestAPromotionThatDoesNotHappenIsRecordedAsFailed(t *testing.T) {
	b, srv := towerTestBrokerWithStations(t, &promoteFailingStations{})
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	attachStation(t, b, "st-q", lt.id, ownerPubkeyOf(t, b, op.login))
	_, err := b.tower.stationStore.SetState("st-q", attach.StateQuarantine)
	require.NoError(t, err)
	b.tower.stationStore.(*promoteFailingStations).fail = true
	b.adminKey = "admin-secret"
	code, _ := adminTowerPost(t, srv, "/tower/station/promote", `{"station_id":"st-q"}`)
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Len(t, towerEventsOf(t, b, lt.id, "station_promoted"), 1)
	failed := towerEventsOf(t, b, lt.id, "station_promote_failed")
	require.Len(t, failed, 1, "the record does not leave a promotion standing that did not happen")
	require.Equal(t, "st-q", failed[0].Detail["station"])
}

// Two instances, or one restarted, still record one refusal per reason per hour: the check
// reads the shared record for that reason, whoever wrote it.
func TestARefusalRecordedByAnotherInstanceIsNotRecordedAgain(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateSuspended))
	// Another instance recorded this reason ten minutes ago, then a different one after.
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evRequestRefused, Actor: admit.ActorTower,
		Reason: "state_suspended", Instance: "peer", At: time.Now().Add(-10 * time.Minute)}))
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evRequestRefused, Actor: admit.ActorTower,
		Reason: "key_mismatch", Instance: "peer", At: time.Now().Add(-5 * time.Minute)}))

	code, _ := lt.call(t, srv, "/tower/session", jsonOf(t, helloFor(lt)), nil)
	require.Equal(t, http.StatusForbidden, code)
	n, err := b.tower.registry.CountEvents(lt.id, evRequestRefused, "state_suspended", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

// Suppressed refusals are not lost when the retries stop: the sweep writes what is pending.
func TestTheSweepFlushesSuppressedRefusalCounts(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateSuspended))
	for i := 0; i < 7; i++ {
		_, _ = lt.call(t, srv, "/tower/session/heartbeat", jsonOf(t, frameFor(lt, "s")), nil)
	}
	require.Len(t, towerEventsOf(t, b, lt.id, "request_refused"), 1)

	b.towerRefusals.shift(time.Hour + time.Minute)
	b.towerGovSweepOnce(time.Now())
	evs := towerEventsOf(t, b, lt.id, "request_refused")
	require.Len(t, evs, 2)
	require.EqualValues(t, 6, evs[0].Detail["count"], "the six suppressed after the first")

	b.towerGovSweepOnce(time.Now())
	require.Len(t, towerEventsOf(t, b, lt.id, "request_refused"), 2, "nothing pending, nothing written")
}

func TestARefusedAttachIsLoggedWithItsReason(t *testing.T) {
	b, srv := towerTestBroker(t)
	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)
	var buf bytes.Buffer
	restore := captureLog(&buf)
	code, _ := node.attach(t, srv, body, nil) // no live Tower can host it
	restore()
	require.NotEqual(t, http.StatusOK, code)
	require.Contains(t, buf.String(), "tower-event kind=station_attach_refused")
	require.Contains(t, buf.String(), "actor=operator:node-op")
}

func TestALeaseExpiryLostToAConcurrentChangeIsAConflict(t *testing.T) {
	b, srv := towerTestBrokerWithAdmitStore(t, &casLosingStore{Store: admit.NewMemStore()})
	lt := enrolledTower(t, b, "owner-1")
	b.tower.admitStore.(*casLosingStore).lose = true
	b.adminKey = "admin-secret"
	code, _ := adminTowerPost(t, srv, "/tower/lease/expire", `{"tower_id":"`+lt.id+`"}`)
	require.Equal(t, http.StatusConflict, code)
	code, _ = adminTowerPost(t, srv, "/tower/lease/expire", `{"tower_id":"tw-nope"}`)
	require.Equal(t, http.StatusNotFound, code)
}

// The list view reads every Tower's link history in one store call.
func TestTheAdminListReadsLinkHistoryOnce(t *testing.T) {
	counting := &eventCountingStore{Store: admit.NewMemStore()}
	b, srv := towerTestBrokerWithAdmitStore(t, counting)
	b.adminKey = "admin-secret"
	for i := 0; i < 5; i++ {
		_ = enrolledTower(t, b, "owner-1")
	}
	counting.finds, counting.summaries = 0, 0
	_ = adminTowersOf(t, srv)
	require.Equal(t, 1, counting.summaries)
	require.Zero(t, counting.finds, "no per-Tower event lookups")
}

type eventCountingStore struct {
	admit.Store
	finds, summaries int
}

func (c *eventCountingStore) FindEvent(id string, kinds []string, oldest bool) (admit.Event, bool, error) {
	c.finds++
	return c.Store.FindEvent(id, kinds, oldest)
}

func (c *eventCountingStore) LinkSummaries(kinds []string) (map[string]admit.LinkSummary, error) {
	c.summaries++
	return c.Store.LinkSummaries(kinds)
}

// promoteFailingStations is the in-process attachment store whose move to active fails on
// demand - the write a promotion makes.
type promoteFailingStations struct {
	attach.Store
	fail, failReads bool
}

func (p *promoteFailingStations) ByStation(stationID string) (attach.Attachment, bool, error) {
	if p.failReads {
		return attach.Attachment{}, false, attach.ErrUnavailable
	}
	return p.Store.ByStation(stationID)
}

func (p *promoteFailingStations) SetState(stationID, state string) (bool, error) {
	if p.fail && state == attach.StateActive {
		return false, attach.ErrUnavailable
	}
	return p.Store.SetState(stationID, state)
}

// towerTestBrokerWithStations is towerTestBroker over a chosen attachment store.
func towerTestBrokerWithStations(t *testing.T, st *promoteFailingStations) (*broker, *httptest.Server) {
	t.Helper()
	st.Store = attach.NewMemStore()
	b := testBrokerWithDB(store.NewMem())
	ts, err := newTowerSubsystem(b, admit.NewMemStore(), cert.NewMemCustody(), enroll.NewMemStore(),
		cert.Config{TTL: time.Hour}, linkDeps{stations: st, heads: head.NewMemStore()})
	require.NoError(t, err)
	b.tower = ts
	mux := http.NewServeMux()
	b.registerTowerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return b, srv
}
