package main

// towergov6_test.go: the pre-push review's findings, pinned as regressions
// (features/tower/governance_record.feature).

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/tower"
	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/attach"
	"rogerai.fm/roger/v7/internal/towercore/reputation"
	"rogerai.fm/roger/v7/internal/towerjoin"
)

// AN ENROLLMENT REPLAY IS NOT A NEW ENROLLMENT. Enroll is idempotent per transaction, and a
// replay must not append another "enrolled" event (with whatever state the Tower is in now).
func TestAnEnrollmentReplayDoesNotRecordASecondEnrollment(t *testing.T) {
	b, _ := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
	for i := 0; i < 3; i++ {
		b.recordEnrolled(lt.id, "owner-1", admit.StateActive, "serial", "hash")
	}
	evs := towerEventsOf(t, b, lt.id, "enrolled")
	require.Len(t, evs, 1, "one enrollment per Tower, however many replays")
}

func TestARealEnrollmentIsRecordedOnceEvenWhenRetried(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("ROGER_CONFIG_DIR", filepath.Join(cfg, "roger"))
	b, srv := towerTestBroker(t)
	t.Setenv("ROGER_BROKER", srv.URL)
	require.NoError(t, b.db.BindOwner(store.Owner{Pubkey: client.UserPubHex(), Login: "nora",
		Email: "nora@rogerai.fm", EmailVerifiedAt: time.Now().Unix()}))
	dir := t.TempDir()
	_, err := tower.Init(dir, tower.ModeJoined)
	require.NoError(t, err)
	st, err := tower.Open(dir)
	require.NoError(t, err)
	require.NoError(t, towerjoin.Register(st, towerjoin.Account{Login: "nora"}))
	adm, _ := towerjoin.LoadAdmission(dir)
	require.Len(t, towerEventsOf(t, b, adm.TowerID, "enrolled"), 1)
}

// A REQUEST THAT IS NOT THE TOWER'S OWN SIGNED REQUEST IS NOT THE TOWER TRYING. Anyone can
// send an unsigned or wrongly signed request naming a real id; the record must not say the
// Tower did.
func TestRefusalsAnyoneCouldSendAreNotAttributedToTheTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	b.towerGovSweepOnce(time.Now())

	resp, err := http.Post(srv.URL+"/tower/session", "application/json", bytes.NewReader(jsonOf(t, helloFor(lt))))
	require.NoError(t, err)
	resp.Body.Close()
	impostor := lt
	_, impostor.priv, err = ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	code, _ := impostor.call(t, srv, "/tower/session", jsonOf(t, helloFor(impostor)), nil)
	require.Equal(t, http.StatusForbidden, code)

	for _, ev := range towerEventsOf(t, b, lt.id, "request_refused") {
		require.Equal(t, actorUnauthenticated, ev.Actor, ev.Reason)
	}
	require.Len(t, towerEventsOf(t, b, lt.id, "request_refused"), 2)

	// A refusal of the Tower's OWN signed request still names the Tower.
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateSuspended))
	code, _ = lt.call(t, srv, "/tower/session", jsonOf(t, helloFor(lt)), nil)
	require.Equal(t, http.StatusForbidden, code)
	var own bool
	for _, ev := range towerEventsOf(t, b, lt.id, "request_refused") {
		if ev.Reason == "state_suspended" {
			own = ev.Actor == admit.ActorTower
		}
	}
	require.True(t, own)
}

// A SUSPENSION ANOTHER INSTANCE ALREADY MADE IS NOT A FAILED ONE.
func TestASuspensionAPeerAlreadyMadeIsNotRecordedAsFailed(t *testing.T) {
	inner := admit.NewMemStore()
	b, _ := towerTestBrokerWithAdmitStore(t, &suspendRacingStore{Store: inner})
	lt := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
	b.tower.admitStore.(*suspendRacingStore).race = true
	b.autoSuspend(lt.id, reputation.Tally{TowerID: lt.id, CanaryFail: 20}, reputation.Tally{})
	require.Empty(t, towerEventsOf(t, b, lt.id, "lifecycle_refused"))
}

// THE BUDGET IS PER ACTOR: one account's attaches cannot use up another's room on the same
// Tower, and an administrator's records never count against an operator.
func TestTheBudgetIsCountedPerActor(t *testing.T) {
	b, _ := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")
	for i := 0; i < towerEventBudget+5; i++ {
		b.towerEvent(admit.Event{TowerID: lt.id, Kind: evStationAttached, Actor: "operator:noisy",
			Detail: map[string]any{"station": "n"}})
	}
	b.towerEvent(admit.Event{TowerID: lt.id, Kind: evStationAttached, Actor: "operator:quiet",
		Detail: map[string]any{"station": "q"}})
	var quiet, noisy int
	for _, ev := range towerEventsOf(t, b, lt.id, "station_attached") {
		switch ev.Actor {
		case "operator:quiet":
			quiet++
		case "operator:noisy":
			noisy++
		}
	}
	require.Equal(t, 1, quiet)
	require.Equal(t, towerEventBudget, noisy)
}

func TestAnOperatorsLifecycleBudgetCountsOnlyTheirOwnMoves(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	// The administrator moves the Tower back and forth many times.
	for i := 0; i < towerEventBudget/2+1; i++ {
		_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"active"}`)
		_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"suspended"}`)
		_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"quarantine"}`)
	}
	_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"active"}`)
	code, raw := op.call(t, srv, http.MethodPost, "/tower/self/lifecycle", map[string]string{"tower_id": lt.id, "state": "draining"}, nil)
	require.Equal(t, http.StatusOK, code, raw)
}

// A Tower decides its own canary result and its own resumptions, so those are capped too.
func TestCanaryFlipsAndResumptionsAreCapped(t *testing.T) {
	b, _ := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")
	for i := 0; i < towerEventBudget+10; i++ {
		outcome := reputation.CanaryPass
		if i%2 == 1 {
			outcome = reputation.CanaryFail
		}
		b.noteCanary(lt.id, "st-1", outcome)
	}
	require.Len(t, towerEventsOf(t, b, lt.id, "canary"), towerEventBudget)
	require.True(t, pacedEvent(admit.Event{Kind: evSessionResumed, Actor: admit.ActorTower}))
}

// One event per Tower per sweep, naming the Stations: attach/revoke churn cannot turn the
// terminal reap into an uncapped stream of system events.
func TestASweepWritesOneEventPerTowerNamingItsStations(t *testing.T) {
	b, _ := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	owner := ownerPubkeyOf(t, b, op.login)
	for i := 0; i < 30; i++ {
		id := "st-" + strings.Repeat("a", i+1)
		attachStation(t, b, id, lt.id, owner)
		_, err := b.tower.stations.Revoke(id)
		require.NoError(t, err)
	}
	b.towerInviteSweepOnce(time.Now().Add(terminalAttachmentHorizon + time.Hour))
	evs := towerEventsOf(t, b, lt.id, "station_retired")
	require.Len(t, evs, 1)
	require.EqualValues(t, 30, evs[0].Detail["count"])
	require.Len(t, evs[0].Detail["stations"], 20, "the names, capped")
	_ = attach.StateRevoked
}

// "Did it ever connect" answers with when it was approved and by whom, even after churn has
// pushed the approval out of the newest-50 list.
func TestTheDetailNamesTheApprovalEvenUnderChurn(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"active"}`)
	for i := 0; i < 60; i++ {
		require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: "noise", Actor: admit.ActorSystem}))
	}
	conn := adminTowerDetailOf(t, srv, lt.id)["connection"].(map[string]any)
	require.Equal(t, "never connected", conn["status"])
	require.Equal(t, "admin:broker-key", conn["approved_by"])
	require.NotEmpty(t, conn["approved_at"])
}

func TestRefusedRenewalAndEnrollmentLogsArePaced(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")
	var buf bytes.Buffer
	restore := captureLog(&buf)
	for i := 0; i < 5; i++ {
		_, _ = lt.call(t, srv, "/tower/renew", jsonOf(t, map[string]any{
			"tower_id": lt.id, "nonce": "n", "identity_key": "AAAA", "signature": "AAAA", "csr": "AAAA"}), nil)
	}
	restore()
	require.LessOrEqual(t, strings.Count(buf.String(), "kind=cert_renew_refused"), 1)
}

// suspendRacingStore lets a peer suspend the Tower between the read and the write.
type suspendRacingStore struct {
	admit.Store
	race  bool
	reads int
}

// The peer moves on the SECOND read: after autoSuspend has seen the Tower active, before
// the transition reads it again.
func (s *suspendRacingStore) TowerByID(id string) (admit.Tower, bool, error) {
	tw, ok, err := s.Store.TowerByID(id)
	if s.race {
		s.reads++
	}
	if s.race && s.reads == 2 && ok && tw.State == admit.StateActive {
		s.race = false
		peer := tw
		peer.State = admit.StateSuspended
		_, _ = s.Store.CASTower(peer)
		tw.State = admit.StateSuspended // this read already sees the peer's move
	}
	return tw, ok, err
}

func TestAnOperatorResumeIsNotShownAsTheApproval(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"active"}`)
	for _, to := range []string{"draining", "active"} {
		code, raw := op.call(t, srv, http.MethodPost, "/tower/self/lifecycle", map[string]string{"tower_id": lt.id, "state": to}, nil)
		require.Equal(t, http.StatusOK, code, raw)
	}
	conn := adminTowerDetailOf(t, srv, lt.id)["connection"].(map[string]any)
	require.Equal(t, "admin:broker-key", conn["approved_by"])
}

// A Tower whose open was not recorded (over the budget) but whose close was, and which then
// opens again and goes quiet, is still recorded as quiet: the link, not only the record,
// says whether a session is open.
func TestAQuietTowerIsCaughtEvenWhenItsOpenWasNotRecorded(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evSessionClosed, Actor: admit.ActorTower,
		At: time.Now().Add(-time.Hour)}))
	// A session opens, but its record is lost (as when over the budget).
	_, err := b.tower.link.Open(helloFor(lt), lt.id)
	require.NoError(t, err)
	_ = call
	b.towerGovSweepOnce(time.Now().Add(towerFreshnessWindow + time.Minute))
	require.Len(t, towerEventsOf(t, b, lt.id, "session_stale"), 1)
}
