package main

// The Station-free hub check, as the broker runs it. Contract:
// features/tower/tower_trust_ramp.feature ("the hub check").

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/towercore/health"
	"rogerai.fm/roger/v7/internal/towerhub"
)

// realHubFor mounts a towerhub server keyed with the Tower's own identity key.
func realHubFor(t *testing.T, lt linkTower) string {
	t.Helper()
	s := towerhub.NewServer(towerhub.New(), nil, towerhub.ServerOptions{TowerID: lt.id, EpochKey: lt.priv,
		SubmitTTL: time.Second, PollTTL: 50 * time.Millisecond})
	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathPoll, s.Poll)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func openWithHub(t *testing.T, b *broker, srv *httptest.Server, lt linkTower, endpoint string) {
	t.Helper()
	h := helloFor(lt)
	h.RelayEndpoint = endpoint
	code, raw := lt.call(t, srv, "/tower/session", jsonOf(t, h), nil)
	require.Equal(t, http.StatusOK, code, raw)
}

func TestEveryLiveTowerWithAHubIsCheckedWithoutStations(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	openWithHub(t, b, srv, lt, realHubFor(t, lt))

	b.towerHubCheckOnce(time.Now())
	sum, err := b.tower.health.Summary(lt.id, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, sum.Passed)
	require.Equal(t, 0, sum.Failed)
	require.Zero(t, sum.FailStreak)
	require.Equal(t, health.SourceHubCheck, sum.LastSource, "recorded as a hub check")
	require.Greater(t, sum.LastRTT, time.Duration(0), "with its round trip")
	require.Empty(t, towerEventsOf(t, b, lt.id, evHubCheckFailed), "a pass writes no event")
}

func TestAFailedHubCheckIsRecordedAndEvented(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	impostor := enrolledTower(t, b, op.login) // a hub keyed with somebody else's identity
	openWithHub(t, b, srv, lt, realHubFor(t, impostor))

	b.towerHubCheckOnce(time.Now())
	sum, _ := b.tower.health.Summary(lt.id, time.Now().Add(-time.Hour))
	require.Equal(t, 1, sum.Failed)
	evs := towerEventsOf(t, b, lt.id, evHubCheckFailed)
	require.Len(t, evs, 1)
	require.Equal(t, "wrong_key", evs[0].Reason)
	require.Equal(t, "system", evs[0].Actor)
}

func TestATowerWithNoHubIsNotChecked(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	openSessionOf(t, srv.URL, lt, call)
	b.towerHubCheckOnce(time.Now())
	sum, _ := b.tower.health.Summary(lt.id, time.Now().Add(-time.Hour))
	require.Zero(t, sum.Passed+sum.Failed)
}

func TestOnlyTheInstanceHoldingTheLinkChecks(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	openWithHub(t, b, srv, lt, realHubFor(t, lt))
	b.tower.link.Close(mustCurrent(t, b, lt.id), lt.id)
	b.towerHubCheckOnce(time.Now())
	sum, _ := b.tower.health.Summary(lt.id, time.Now().Add(-time.Hour))
	require.Zero(t, sum.Passed+sum.Failed, "a Tower this instance does not hold is left to its holder")
}

func TestAHubCheckRunsAtMostOncePerCanaryIntervalPerTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	openWithHub(t, b, srv, lt, realHubFor(t, lt))
	now := time.Now()
	b.towerHubCheckOnce(now)
	b.towerHubCheckOnce(now.Add(time.Minute))
	sum, _ := b.tower.health.Summary(lt.id, now.Add(-time.Hour))
	require.Equal(t, 1, sum.Passed)
	b.towerHubCheckOnce(now.Add(canaryInterval))
	sum, _ = b.tower.health.Summary(lt.id, now.Add(-time.Hour))
	require.Equal(t, 2, sum.Passed)
}

func TestStationCanariesCountAsHealthChecksButStationFaultsDoNot(t *testing.T) {
	b, _ := towerTestBroker(t)
	b.recordTowerHealth("tw-1", "canary_pass")
	b.recordTowerHealth("tw-1", "canary_fail")
	b.recordTowerHealth("tw-1", "station_fault")
	b.recordTowerHealth("tw-1", "aborted")
	sum, _ := b.tower.health.Summary("tw-1", time.Now().Add(-time.Hour))
	require.Equal(t, 1, sum.Passed)
	require.Equal(t, 1, sum.Failed, "a Station's own fault is not the Tower's health")
}

func mustCurrent(t *testing.T, b *broker, towerID string) string {
	t.Helper()
	id, ok := b.tower.link.Current(towerID)
	require.True(t, ok)
	return id
}

// AUDIT FINDING: a slow hub check was stamped with the pass's start time.
func TestAHubCheckIsStampedWhenItFinishes(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	openWithHub(t, b, srv, lt, realHubFor(t, lt))
	start := time.Now()
	b.towerHubCheckOnce(start)
	sum, _ := b.tower.health.Summary(lt.id, start.Add(-time.Hour))
	require.True(t, sum.LastAt.After(start), "stamped after the pass began")
}

// recordHealthResult files one hub-check result, as the hub check does - the tests' way of
// saying "a health check of this Tower passed or failed at this time".
func (b *broker) recordHealthResult(towerID string, ok bool, at time.Time) {
	b.recordHealthCheck(towerID, health.Check{OK: ok, At: at, Source: health.SourceHubCheck, Slot: at.UnixNano()})
}

// A hub check and a canary failing in the same sweep are one outage (audit finding), even when
// the sweep straddles a slot boundary; a canary alone in a later sweep advances the streak, so
// a failing relay with a live hub is still caught.
func TestOneFailurePerSweepFromAnySource(t *testing.T) {
	b, _ := towerTestBroker(t)
	// A sweep that starts just before a boundary; its canary lands after it.
	boundary := time.Unix(0, (time.Now().UnixNano()/int64(canaryInterval)+1)*int64(canaryInterval))
	start := boundary.Add(-time.Second)
	b.sweepSlot.Store(healthSlot(start))
	b.recordHealthCheck("tw-1", health.Check{OK: false, At: start, Source: health.SourceHubCheck, Slot: b.slotFor(start)})
	b.recordTowerHealth("tw-1", "canary_fail") // same sweep, whatever the clock says now
	b.sweepSlot.Store(0)
	sum, _ := b.tower.health.Summary("tw-1", start.Add(-time.Hour))
	require.Equal(t, 1, sum.FailStreak)
	require.Equal(t, 2, sum.Failed)
	require.Empty(t, towerEventsOf(t, b, "tw-1", evHealthFailing), "one outage is not health_failing")

	next := start.Add(canaryInterval + time.Minute)
	b.sweepSlot.Store(healthSlot(next))
	b.recordTowerHealth("tw-1", "canary_fail")
	b.sweepSlot.Store(0)
	sum, _ = b.tower.health.Summary("tw-1", start.Add(-time.Hour))
	require.Equal(t, 2, sum.FailStreak, "a canary in the next sweep counts")
	require.Len(t, towerEventsOf(t, b, "tw-1", evHealthFailing), 1)
}

// recordCheckForTest files a hub check in a slot of its own, so each call counts on its own.
func recordCheckForTest(s health.Store, towerID string, ok bool, at time.Time) error {
	return s.RecordCheck(towerID, health.Check{OK: ok, At: at, Source: health.SourceHubCheck, Slot: at.UnixNano()})
}
