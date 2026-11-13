package main

// towergov7_test.go: the minor findings left by the passing audit
// (features/tower/governance_record.feature).

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/towercore/admit"
	"rogerai.fm/roger/v7/internal/towercore/attach"
)

// Retiring your own Tower is terminal and cannot loop, so the hourly budget never blocks it.
func TestTheBudgetNeverStopsAnOperatorRetiringTheirTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"active"}`)
	for i := 0; i < towerEventBudget; i++ {
		to := "draining"
		if i%2 == 1 {
			to = "active"
		}
		_, _ = op.call(t, srv, http.MethodPost, "/tower/self/lifecycle", map[string]string{"tower_id": lt.id, "state": to}, nil)
	}
	code, raw := op.call(t, srv, http.MethodPost, "/tower/self/lifecycle", map[string]string{"tower_id": lt.id, "state": "revoked"}, nil)
	require.Equal(t, http.StatusOK, code, raw)
}

func TestEveryRecordedAdminActionPrintsItsLine(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	attachStation(t, b, "st-q", lt.id, ownerPubkeyOf(t, b, op.login))
	_, err := b.tower.stationStore.SetState("st-q", attach.StateQuarantine)
	require.NoError(t, err)
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
	_, err = b.tower.registry.RecordRenewal(lt.id, admit.Renewal{CertSerial: "4242", TLSKeyHash: "h", At: time.Now()})
	require.NoError(t, err)
	b.adminKey = "admin-secret"

	var buf bytes.Buffer
	restore := captureLog(&buf)
	code, _ := adminTowerPost(t, srv, "/tower/station/promote", `{"station_id":"st-q"}`)
	require.Equal(t, http.StatusOK, code)
	code, _ = adminTowerPost(t, srv, "/tower/cert/revoke", `{"tower_id":"`+lt.id+`"}`)
	require.Equal(t, http.StatusOK, code)
	restore()
	require.Contains(t, buf.String(), "tower-event kind=station_promoted tower="+lt.id)
	require.Contains(t, buf.String(), "tower-event kind=lifecycle tower="+lt.id+" actor=admin:broker-key from=active to=suspended")
}

// A count deferred to another instance's record is deferred ONCE; the next hour it is
// written, so two instances cannot keep losing each other's counts.
func TestADeferredRefusalCountIsWrittenTheNextHour(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateSuspended))
	for i := 0; i < 3; i++ {
		_, _ = lt.call(t, srv, "/tower/session/heartbeat", jsonOf(t, frameFor(lt, "s")), nil)
	}
	b.towerRefusals.shift(time.Hour + time.Minute)
	now := b.towerRefusals.clock()
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evRequestRefused, Actor: admit.ActorTower,
		Reason: "state_suspended", Instance: "peer", At: now.Add(-time.Minute)}))
	b.towerGovSweepOnce(now) // deferred
	b.towerRefusals.shift(time.Hour + time.Minute)
	now = b.towerRefusals.clock()
	require.NoError(t, b.tower.registry.Record(admit.Event{TowerID: lt.id, Kind: evRequestRefused, Actor: admit.ActorTower,
		Reason: "state_suspended", Instance: "peer", At: now.Add(-time.Minute)}))
	b.towerGovSweepOnce(now) // written this time
	var flushed int
	for _, ev := range towerEventsOf(t, b, lt.id, "request_refused") {
		if ev.Detail["flushed"] == true {
			flushed++
			require.EqualValues(t, 2, ev.Detail["count"])
		}
	}
	require.Equal(t, 1, flushed)
}

// Two administrators promoting one Station at once: the one whose promote found it already
// active did not fail, and the record must not say it did.
func TestAPromotionAPeerAlreadyMadeIsNotRecordedAsFailed(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	attachStation(t, b, "st-q", lt.id, ownerPubkeyOf(t, b, op.login))
	// A peer promotes between this request's read and its promote.
	b.tower.stationStore = &promotingPeerStore{Store: b.tower.stationStore}
	_, err := b.tower.stationStore.SetState("st-q", attach.StateQuarantine)
	require.NoError(t, err)
	b.adminKey = "admin-secret"
	code, _ := adminTowerPost(t, srv, "/tower/station/promote", `{"station_id":"st-q"}`)
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, towerEventsOf(t, b, lt.id, "station_promote_failed"))
}

type promotingPeerStore struct {
	attach.Store
	reads int
}

func (p *promotingPeerStore) ByStation(id string) (attach.Attachment, bool, error) {
	at, ok, err := p.Store.ByStation(id)
	p.reads++
	if p.reads == 1 && ok && at.State == attach.StateQuarantine {
		_, _ = p.Store.SetState(id, attach.StateActive) // the peer's promotion lands now
	}
	return at, ok, err
}
