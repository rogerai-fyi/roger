package main

// towergov2_test.go: the rest of the governance record (features/tower/governance_record.feature):
// what Core decides on its own, enrollment, certificates, Stations and probes.

import (
	"bytes"
	"encoding/base64"
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

// --- what Core decides on its own -------------------------------------------------------

func TestAnAutomaticSuspensionKeepsItsEvidence(t *testing.T) {
	b, _ := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))

	tally := reputation.Tally{TowerID: lt.id, Total: 40, CanaryPass: 2, CanaryFail: 18}
	fleet := reputation.Tally{Total: 900, CanaryPass: 300, CanaryFail: 3}
	b.autoSuspend(lt.id, tally, fleet)

	tw, _ := b.tower.registry.Get(lt.id)
	require.Equal(t, admit.StateSuspended, tw.State)
	evs := towerEventsOf(t, b, lt.id, "lifecycle")
	require.Len(t, evs, 1)
	ev := evs[0]
	require.Equal(t, admit.ActorSystem, ev.Actor)
	require.Equal(t, "reputation", ev.Reason)
	require.Equal(t, admit.StateActive, ev.From)
	require.Equal(t, admit.StateSuspended, ev.To)
	// The evidence is ON the event, so it survives the outcome rows being reaped at 24h.
	require.EqualValues(t, 18, ev.Detail["canary_fail"])
	require.EqualValues(t, 40, ev.Detail["total"])
	require.EqualValues(t, 3, ev.Detail["fleet_canary_fail"])
	require.EqualValues(t, 900, ev.Detail["fleet_total"])
}

func TestATowerAlreadyOffIsNotReportedAgainAndAgain(t *testing.T) {
	b, _ := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateSuspended))

	var buf bytes.Buffer
	restore := captureLog(&buf)
	for i := 0; i < 5; i++ {
		b.autoSuspend(lt.id, reputation.Tally{TowerID: lt.id, CanaryFail: 20}, reputation.Tally{})
	}
	restore()
	require.Empty(t, towerEventsOf(t, b, lt.id))
	require.NotContains(t, buf.String(), "evidence warrants suspension but the move failed")
}

func TestAFailedAutomaticSuspensionIsRecorded(t *testing.T) {
	b, _ := towerTestBrokerWithAdmitStore(t, &casLosingStore{Store: admit.NewMemStore()})
	lt := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
	b.tower.admitStore.(*casLosingStore).lose = true

	b.autoSuspend(lt.id, reputation.Tally{TowerID: lt.id, CanaryFail: 20}, reputation.Tally{})
	evs := towerEventsOf(t, b, lt.id, "lifecycle_refused")
	require.Len(t, evs, 1)
	require.Equal(t, admit.ActorSystem, evs[0].Actor)
	require.Equal(t, "concurrent_change", evs[0].Reason)
}

func TestALapsedLeaseIsRecordedOnceAcrossInstances(t *testing.T) {
	b, _ := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.ExpireLease(lt.id))

	b.towerGovSweepOnce(time.Now())
	b.towerGovSweepOnce(time.Now()) // the second instance, or the next sweep
	evs := towerEventsOf(t, b, lt.id, "lease_lapsed")
	require.Len(t, evs, 1)
	require.Equal(t, admit.ActorSystem, evs[0].Actor)
	tw, _ := b.tower.registry.Get(lt.id)
	require.Equal(t, admit.StateQuarantine, tw.State, "this slice records the lapse; it does not change the state")

	// A NEW lease that lapses again is a new lapse.
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateSuspended))
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateQuarantine)) // restarts the lease
	require.NoError(t, b.tower.registry.ExpireLease(lt.id))
	b.towerGovSweepOnce(time.Now().Add(time.Second))
	require.Len(t, towerEventsOf(t, b, lt.id, "lease_lapsed"), 2)
}

// --- enrollment and certificates (through the real CLI, against the real routes) ---------

func TestEnrollmentAndRenewalAreRecorded(t *testing.T) {
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

	var buf bytes.Buffer
	restore := captureLog(&buf)
	require.NoError(t, towerjoin.Register(st, towerjoin.Account{Login: "nora"}))
	restore()
	adm, ok := towerjoin.LoadAdmission(dir)
	require.True(t, ok)

	enrolled := towerEventsOf(t, b, adm.TowerID, "enrolled")
	require.Len(t, enrolled, 1)
	require.Equal(t, "operator:nora", enrolled[0].Actor)
	require.Equal(t, admit.StateQuarantine, enrolled[0].To)
	require.Contains(t, buf.String(), "tower-event kind=enrolled tower="+adm.TowerID,
		"logged whether or not an admin email is configured")

	before, _ := b.tower.registry.Get(adm.TowerID)
	did, err := towerjoin.RenewIfDue(st, adm.NotAfter)
	require.NoError(t, err)
	require.True(t, did)
	renewed := towerEventsOf(t, b, adm.TowerID, "cert_renewed")
	require.Len(t, renewed, 1)
	require.Equal(t, admit.ActorTower, renewed[0].Actor)
	require.Equal(t, before.CertSerial, renewed[0].Detail["old_serial"])
	after, _ := b.tower.registry.Get(adm.TowerID)
	require.Equal(t, after.CertSerial, renewed[0].Detail["new_serial"])
	require.NotEmpty(t, renewed[0].Detail["lease_expires"])
}

func TestARefusedEnrollmentIsLoggedWithoutTheToken(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	junk := base64.StdEncoding.EncodeToString([]byte("not-a-real-thing-at-all-32-bytes"))

	var buf bytes.Buffer
	restore := captureLog(&buf)
	code, _ := op.call(t, srv, http.MethodPost, "/tower/enroll", map[string]any{
		"token": "secret-token-value", "transaction_id": "tx-1", "nonce": "secret-nonce-value",
		"identity_key": junk, "signature": junk, "csr": junk, "protocol_version": 1,
	}, nil)
	restore()
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, buf.String(), "tower-event kind=enroll_refused")
	require.Contains(t, buf.String(), "actor=operator:octocat")
	require.NotContains(t, buf.String(), "secret-token-value")
	require.NotContains(t, buf.String(), "secret-nonce-value")
}

func TestARefusedRenewalIsLoggedWithItsReasonClass(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")
	junk := base64.StdEncoding.EncodeToString([]byte("junk"))
	var buf bytes.Buffer
	restore := captureLog(&buf)
	code, _ := lt.call(t, srv, "/tower/renew", jsonOf(t, map[string]any{
		"tower_id": lt.id, "nonce": "secret-nonce", "identity_key": junk, "signature": junk, "csr": junk,
	}), nil)
	restore()
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, buf.String(), "tower-event kind=cert_renew_refused tower="+lt.id)
	require.NotContains(t, buf.String(), "secret-nonce")
}

// --- administrator actions on certificates, leases and Stations ---------------------------

func TestAnAdminCertificateRevocationIsRecordedWithTheAdmin(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
	_, err := b.tower.registry.RecordRenewal(lt.id, admit.Renewal{CertSerial: "12345", TLSKeyHash: "h", At: time.Now()})
	require.NoError(t, err)
	b.adminKey = "admin-secret"
	code, raw := adminTowerPost(t, srv, "/tower/cert/revoke", `{"tower_id":"`+lt.id+`","reason":"key leaked"}`)
	require.Equal(t, http.StatusOK, code, raw)

	evs := towerEventsOf(t, b, lt.id, "cert_revoked")
	require.Len(t, evs, 1)
	require.Equal(t, "admin:broker-key", evs[0].Actor)
	require.Equal(t, "key leaked", evs[0].Reason)
	require.Equal(t, "12345", evs[0].Detail["serial"])
	suspended := towerEventsOf(t, b, lt.id, "lifecycle")
	require.Len(t, suspended, 1, "the suspension that goes with it is on the record too")
	require.Equal(t, admit.StateSuspended, suspended[0].To)
	require.Equal(t, "admin:broker-key", suspended[0].Actor)
}

func TestAnAdminCertificateRevocationThatCannotBeRecordedDoesNotHappen(t *testing.T) {
	b, srv := towerTestBrokerWithAdmitStore(t, &eventFailingStore{Store: admit.NewMemStore()})
	lt := enrolledTower(t, b, "owner-1")
	_, err := b.tower.registry.RecordRenewal(lt.id, admit.Renewal{CertSerial: "777", TLSKeyHash: "h", At: time.Now()})
	require.NoError(t, err)
	b.adminKey = "admin-secret"
	code, _ := adminTowerPost(t, srv, "/tower/cert/revoke", `{"tower_id":"`+lt.id+`"}`)
	require.Equal(t, http.StatusServiceUnavailable, code)
	tw, _ := b.tower.registry.Get(lt.id)
	require.False(t, b.tower.ca.SerialRevoked(tw.CertSerial), "an unrecorded revocation is not made")
}

func TestAnAdminLeaseExpiryIsRecordedWithTheAdmin(t *testing.T) {
	b, srv := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")
	b.adminKey = "admin-secret"
	code, raw := adminTowerPost(t, srv, "/tower/lease/expire", `{"tower_id":"`+lt.id+`","reason":"testing"}`)
	require.Equal(t, http.StatusOK, code, raw)
	evs := towerEventsOf(t, b, lt.id, "lease_expired")
	require.Len(t, evs, 1)
	require.Equal(t, "admin:broker-key", evs[0].Actor)
	require.Equal(t, "testing", evs[0].Reason)
}

func TestAnAdminLeaseExpiryThatCannotBeRecordedDoesNotHappen(t *testing.T) {
	b, srv := towerTestBrokerWithAdmitStore(t, &eventFailingStore{Store: admit.NewMemStore()})
	lt := enrolledTower(t, b, "owner-1")
	b.adminKey = "admin-secret"
	code, _ := adminTowerPost(t, srv, "/tower/lease/expire", `{"tower_id":"`+lt.id+`"}`)
	require.Equal(t, http.StatusServiceUnavailable, code)
	tw, _ := b.tower.registry.Get(lt.id)
	require.True(t, tw.LeaseExpires.After(time.Now()), "an unrecorded expiry is not made")
}

func TestAnAdminStationPromotionIsRecordedOnItsTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	attachStation(t, b, "st-q", lt.id, ownerPubkeyOf(t, b, op.login))
	_, err := b.tower.stationStore.SetState("st-q", attach.StateQuarantine)
	require.NoError(t, err)
	b.adminKey = "admin-secret"
	code, raw := adminTowerPost(t, srv, "/tower/station/promote", `{"station_id":"st-q"}`)
	require.Equal(t, http.StatusOK, code, raw)
	evs := towerEventsOf(t, b, lt.id, "station_promoted")
	require.Len(t, evs, 1)
	require.Equal(t, "admin:broker-key", evs[0].Actor)
	require.Equal(t, "st-q", evs[0].Detail["station"])

	// A no-op promotion (already promoted) records nothing.
	code, _ = adminTowerPost(t, srv, "/tower/station/promote", `{"station_id":"st-q"}`)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, towerEventsOf(t, b, lt.id, "station_promoted"), 1)
}

// --- Stations --------------------------------------------------------------------------------

func TestAnOperatorRevokingAStationIsRecorded(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	attachStation(t, b, "st-mine", lt.id, ownerPubkeyOf(t, b, op.login))
	code, raw := op.call(t, srv, http.MethodPost, "/tower/station/revoke", map[string]any{"station_id": "st-mine"}, nil)
	require.Equal(t, http.StatusOK, code, raw)
	evs := towerEventsOf(t, b, lt.id, "station_revoked")
	require.Len(t, evs, 1)
	require.Equal(t, "operator:octocat", evs[0].Actor)
	require.Equal(t, "st-mine", evs[0].Detail["station"])
}

func TestANodeAttachingToATowerIsRecorded(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)
	var out struct {
		StationID string `json:"station_id"`
	}
	code, raw := node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusOK, code, raw)
	evs := towerEventsOf(t, b, tw.id, "station_attached")
	require.Len(t, evs, 1)
	require.Equal(t, out.StationID, evs[0].Detail["station"])
	require.Contains(t, evs[0].Detail, "quarantine")
	require.True(t, strings.HasPrefix(evs[0].Actor, "operator:"))
}

func TestSweepsThatRetireStationsNameThem(t *testing.T) {
	b, _ := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	attachStation(t, b, "st-old", lt.id, ownerPubkeyOf(t, b, op.login))
	_, err := b.tower.stations.Revoke("st-old")
	require.NoError(t, err)

	var buf bytes.Buffer
	restore := captureLog(&buf)
	b.towerInviteSweepOnce(time.Now().Add(terminalAttachmentHorizon + time.Hour))
	restore()
	require.Contains(t, buf.String(), "st-old", "the log names the Station, not only a count")
	evs := towerEventsOf(t, b, lt.id, "station_retired")
	require.Len(t, evs, 1)
	require.Equal(t, admit.ActorSystem, evs[0].Actor)
	require.Equal(t, []any{"st-old"}, toAnySlice(evs[0].Detail["stations"]))
}

// --- probes ------------------------------------------------------------------------------------

func TestProbeResultsAreRecordedWhenTheyChange(t *testing.T) {
	b, _ := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")

	b.noteCanary(lt.id, "st-1", reputation.CanaryPass)
	for i := 0; i < 50; i++ {
		b.noteCanary(lt.id, "st-1", reputation.CanaryPass)
	}
	require.Len(t, towerEventsOf(t, b, lt.id, "canary"), 1)

	var buf bytes.Buffer
	restore := captureLog(&buf)
	b.noteCanary(lt.id, "st-2", reputation.CanaryFail)
	restore()
	evs := towerEventsOf(t, b, lt.id, "canary")
	require.Len(t, evs, 2)
	require.Equal(t, "fail", evs[0].Detail["result"])
	require.Equal(t, "st-2", evs[0].Detail["station"])
	require.Equal(t, "canary_fail", evs[0].Reason)
	require.Contains(t, buf.String(), "canary: tower "+lt.id+" failed station=st-2 outcome=canary_fail",
		"every failure is logged with the Station and the reason")

	b.noteCanary(lt.id, "st-2", reputation.CanaryFail) // logged, not recorded again
	b.noteCanary(lt.id, "st-1", reputation.CanaryPass)
	evs = towerEventsOf(t, b, lt.id, "canary")
	require.Len(t, evs, 3)
	require.Equal(t, "pass", evs[0].Detail["result"])
}

func TestATowerSkippedByDesignIsRecordedOnce(t *testing.T) {
	b, _ := towerTestBroker(t)
	lt := enrolledTower(t, b, "owner-1")
	for i := 0; i < 3; i++ {
		b.noteCanarySkip(lt.id, "st-1")
	}
	evs := towerEventsOf(t, b, lt.id, "canary")
	require.Len(t, evs, 1)
	require.Equal(t, "unreachable_by_design", evs[0].Detail["result"])
}

// casLosingStore loses every CAS on demand, the way a concurrent writer would.
type casLosingStore struct {
	admit.Store
	lose bool
}

func (c *casLosingStore) CASTowerEvent(tw admit.Tower, ev admit.Event) (bool, error) {
	if c.lose {
		return false, nil
	}
	return c.Store.CASTowerEvent(tw, ev)
}

// toAnySlice normalises a detail list read from either store ([]string in memory, []any
// from JSON).
func toAnySlice(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	}
	return nil
}
