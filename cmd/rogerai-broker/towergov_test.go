package main

// towergov_test.go: the Tower governance record as the broker writes it.
//
// Contract: features/tower/governance_record.feature (approved 2026-11-13). The first
// outside Tower was approved and Core could not say whether it ever connected; these pin
// that every governance event leaves an attributed record and one greppable log line.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"math/big"
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
	"rogerai.fm/roger/v7/internal/towercore/link"
)

func towerEventsOf(t *testing.T, b *broker, towerID string, kinds ...string) []admit.Event {
	t.Helper()
	evs, err := b.tower.registry.Events(towerID, 500)
	require.NoError(t, err)
	if len(kinds) == 0 {
		return evs
	}
	var out []admit.Event
	for _, ev := range evs {
		for _, k := range kinds {
			if ev.Kind == k {
				out = append(out, ev)
			}
		}
	}
	return out
}

func helloFor(lt linkTower) link.Hello {
	return link.Hello{Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id, Capabilities: mandatoryCaps()}
}

func openSessionOf(t *testing.T, srvURL string, lt linkTower, call func(path string, body []byte, out any) (int, string)) link.Accepted {
	t.Helper()
	var acc link.Accepted
	code, raw := call("/tower/session", jsonOf(t, helloFor(lt)), &acc)
	require.Equal(t, http.StatusOK, code, raw)
	return acc
}

func frameFor(lt linkTower, sessionID string) link.Frame {
	return link.Frame{Network: link.PublicNetwork, Version: 1, TowerID: lt.id, SessionID: sessionID}
}

// --- the log line -------------------------------------------------------------------

func TestTheTowerEventLogLineIsOneGreppableLine(t *testing.T) {
	line := formatTowerEvent(admit.Event{TowerID: "tw-1", Kind: "lifecycle", Actor: "admin:gh:luis",
		From: admit.StateQuarantine, To: admit.StateActive, Reason: "checked with operator",
		Instance: "i-1", Detail: map[string]any{"count": 3, "station": "st-1"}})
	require.Equal(t, `tower-event kind=lifecycle tower=tw-1 actor=admin:gh:luis from=quarantine to=active `+
		`reason="checked with operator" count=3 instance=i-1 station=st-1`, line)
}

func TestATowerEventLogLineCannotBeForgedThroughAField(t *testing.T) {
	line := formatTowerEvent(admit.Event{TowerID: "tw-1", Kind: "lifecycle", Actor: "system",
		Reason: "x kind=forged\nnext line", Detail: map[string]any{"k": "a=b"}})
	require.NotContains(t, line, "\n", "one event, one line")
	require.Contains(t, line, `reason="x kind=forged\nnext line"`, "a value with = or a newline is quoted")
	require.Contains(t, line, `k="a=b"`)
	require.True(t, strings.HasPrefix(line, "tower-event kind=lifecycle "), "the real kind leads the line")
}

// --- the link ---------------------------------------------------------------------------

func TestOpeningASessionIsRecordedAndTheFirstIsMarked(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }

	var buf bytes.Buffer
	defer captureLog(&buf)()
	acc := openSessionOf(t, srv.URL, lt, call)

	evs := towerEventsOf(t, b, lt.id, "session_opened")
	require.Len(t, evs, 1)
	ev := evs[0]
	require.Equal(t, admit.ActorTower, ev.Actor)
	require.Equal(t, true, ev.Detail["first"])
	require.Equal(t, acc.SessionID[:8], ev.Detail["session"], "a prefix, never the whole session id")
	require.Equal(t, true, ev.Detail["full_inventory"])
	require.EqualValues(t, 1, ev.Detail["version"])
	require.Contains(t, buf.String(), "tower-event kind=session_opened tower="+lt.id)

	// A reconnect replaces the live session and is not the first.
	_ = openSessionOf(t, srv.URL, lt, call)
	evs = towerEventsOf(t, b, lt.id, "session_opened")
	require.Len(t, evs, 2)
	require.Nil(t, evs[0].Detail["first"])
	require.Equal(t, true, evs[0].Detail["replaced"])
}

func TestHeartbeatsAreNotRecordedOneByOne(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	acc := openSessionOf(t, srv.URL, lt, call)

	for i := 0; i < 20; i++ {
		code, raw := call("/tower/session/heartbeat", jsonOf(t, frameFor(lt, acc.SessionID)), nil)
		require.Equal(t, http.StatusOK, code, raw)
	}
	require.Len(t, towerEventsOf(t, b, lt.id), 1, "only the open is recorded")
}

func TestAnOrderlyCloseIsRecordedWithItsDuration(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	acc := openSessionOf(t, srv.URL, lt, call)

	code, raw := call("/tower/session/close", jsonOf(t, frameFor(lt, acc.SessionID)), nil)
	require.Equal(t, http.StatusOK, code, raw)
	evs := towerEventsOf(t, b, lt.id, "session_closed")
	require.Len(t, evs, 1)
	require.Equal(t, admit.ActorTower, evs[0].Actor)
	require.Contains(t, evs[0].Detail, "duration_seconds")

	// The sweep after the window does not call an orderly close "stale".
	b.towerGovSweepOnce(time.Now().Add(time.Hour))
	require.Empty(t, towerEventsOf(t, b, lt.id, "session_stale"))
}

func TestAQuietTowerIsRecordedStaleOnceAndItsReturnIsRecorded(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	acc := openSessionOf(t, srv.URL, lt, call)

	// Inside the window: nothing.
	b.towerGovSweepOnce(time.Now().Add(towerFreshnessWindow / 2))
	require.Empty(t, towerEventsOf(t, b, lt.id, "session_stale"))

	// Past it: one event, however many sweeps (and instances) look.
	later := time.Now().Add(towerFreshnessWindow + time.Minute)
	b.towerGovSweepOnce(later)
	b.towerGovSweepOnce(later)
	b.towerGovSweepOnce(later.Add(time.Hour))
	stale := towerEventsOf(t, b, lt.id, "session_stale")
	require.Len(t, stale, 1)
	require.Equal(t, admit.ActorSystem, stale[0].Actor)
	require.Contains(t, stale[0].Detail, "last_seen")

	// The next heartbeat brings it back, and says so.
	code, raw := call("/tower/session/heartbeat", jsonOf(t, frameFor(lt, acc.SessionID)), nil)
	require.Equal(t, http.StatusOK, code, raw)
	resumed := towerEventsOf(t, b, lt.id, "session_resumed")
	require.Len(t, resumed, 1)
	require.Contains(t, resumed[0].Detail, "gone_seconds")

	// Further heartbeats do not repeat it.
	_, _ = call("/tower/session/heartbeat", jsonOf(t, frameFor(lt, acc.SessionID)), nil)
	require.Len(t, towerEventsOf(t, b, lt.id, "session_resumed"), 1)
}

func TestReopeningAfterGoingQuietSaysHowLongItWasGone(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }
	_ = openSessionOf(t, srv.URL, lt, call)
	b.towerGovSweepOnce(time.Now().Add(towerFreshnessWindow + time.Minute))

	_ = openSessionOf(t, srv.URL, lt, call)
	evs := towerEventsOf(t, b, lt.id, "session_opened")
	require.Len(t, evs, 2)
	require.Contains(t, evs[0].Detail, "back_after_seconds")
}

func TestATowerThatNeverConnectedIsNeverCalledStale(t *testing.T) {
	b, _ := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	b.towerGovSweepOnce(time.Now().Add(24 * time.Hour))
	require.Empty(t, towerEventsOf(t, b, lt.id, "session_stale"))
}

func TestARefusedNegotiationIsRecordedAndAnsweredAsBefore(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	h := helloFor(lt)
	h.Versions = []int{99}
	code, _ := lt.call(t, srv, "/tower/session", jsonOf(t, h), nil)
	require.Equal(t, http.StatusBadRequest, code)
	evs := towerEventsOf(t, b, lt.id, "session_refused")
	require.Len(t, evs, 1)
	require.NotEmpty(t, evs[0].Reason)
}

// --- refused Tower requests ---------------------------------------------------------------

func TestARefusedTowerRequestIsLoggedAndRecordedWithItsReason(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		setup  func(t *testing.T, b *broker, lt *linkTower)
	}{
		{"key mismatch", "key_mismatch", func(t *testing.T, b *broker, lt *linkTower) {
			_, other, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			lt.priv = other
		}},
		{"suspended", "state_suspended", func(t *testing.T, b *broker, lt *linkTower) {
			require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateSuspended))
		}},
		{"revoked", "state_revoked", func(t *testing.T, b *broker, lt *linkTower) {
			require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateRevoked))
		}},
		{"lease lapsed", "lease_lapsed", func(t *testing.T, b *broker, lt *linkTower) {
			require.NoError(t, b.tower.registry.ExpireLease(lt.id))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, srv := towerTestBroker(t)
			op := signedInOperator(t, b, "octocat")
			lt := enrolledTower(t, b, op.login)
			tc.setup(t, b, &lt)

			var buf bytes.Buffer
			restore := captureLog(&buf)
			code, raw := lt.call(t, srv, "/tower/session", jsonOf(t, helloFor(lt)), nil)
			restore()
			require.Equal(t, http.StatusForbidden, code)
			require.Contains(t, raw, "this link requires a registered Tower's own signed request",
				"the Tower's answer is unchanged and discloses no reason")
			require.NotContains(t, raw, tc.reason)
			require.Contains(t, buf.String(), "tower-event kind=request_refused tower="+lt.id+" actor="+refusalActor(tc.reason)+" reason="+tc.reason)
			evs := towerEventsOf(t, b, lt.id, "request_refused")
			require.Len(t, evs, 1)
			require.Equal(t, tc.reason, evs[0].Reason)
			require.Equal(t, "/tower/session", evs[0].Detail["route"])
		})
	}
}

func TestAnUnsignedTowerRequestIsRecordedAsUnsigned(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	resp, err := http.Post(srv.URL+"/tower/session", "application/json", bytes.NewReader(jsonOf(t, helloFor(lt))))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	evs := towerEventsOf(t, b, lt.id, "request_refused")
	require.Len(t, evs, 1)
	require.Equal(t, "unsigned", evs[0].Reason)
}

func TestARevokedCertificateSerialIsRecordedAsCertRevoked(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
	_, err := b.tower.registry.RecordRenewal(lt.id, admit.Renewal{CertSerial: "12345", TLSKeyHash: "h", At: time.Now()})
	require.NoError(t, err)
	// The serial alone, with the Tower still active: the refusal must name the certificate.
	require.NoError(t, b.tower.ca.Revoke(big.NewInt(12345)))

	code, _ := lt.call(t, srv, "/tower/session", jsonOf(t, helloFor(lt)), nil)
	require.Equal(t, http.StatusForbidden, code)
	evs := towerEventsOf(t, b, lt.id, "request_refused")
	require.Len(t, evs, 1)
	require.Equal(t, "cert_revoked", evs[0].Reason)
}

func TestRepeatedRefusalsDoNotFloodTheLogOrTheRecord(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateSuspended))

	var buf bytes.Buffer
	restore := captureLog(&buf)
	for i := 0; i < 50; i++ {
		code, _ := lt.call(t, srv, "/tower/session/heartbeat", jsonOf(t, frameFor(lt, "s")), nil)
		require.Equal(t, http.StatusForbidden, code)
	}
	restore()
	require.Equal(t, 1, strings.Count(buf.String(), "kind=request_refused"), "one log line per reason per minute")
	require.Len(t, towerEventsOf(t, b, lt.id, "request_refused"), 1, "one record per reason per hour")

	// An hour on, the next refusal records again and carries the count it stands for.
	b.towerRefusals.shift(time.Hour + time.Minute)
	code, _ := lt.call(t, srv, "/tower/session/heartbeat", jsonOf(t, frameFor(lt, "s")), nil)
	require.Equal(t, http.StatusForbidden, code)
	evs := towerEventsOf(t, b, lt.id, "request_refused")
	require.Len(t, evs, 2)
	require.EqualValues(t, 50, evs[0].Detail["count"], "the 49 suppressed plus this one")
}

func TestARequestNamingAnUnknownTowerIsLoggedButNotRecorded(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	ghost := lt
	ghost.id = "tw-nope"

	var buf bytes.Buffer
	restore := captureLog(&buf)
	for i := 0; i < 5; i++ {
		code, _ := ghost.call(t, srv, "/tower/session", jsonOf(t, helloFor(ghost)), nil)
		require.Equal(t, http.StatusForbidden, code)
	}
	restore()
	require.Equal(t, 1, strings.Count(buf.String(), "tower-event kind=request_refused tower=tw-nope actor=unauthenticated reason=unknown_tower"))
	require.Empty(t, towerEventsOf(t, b, "tw-nope"), "no record is created for a Tower Core never admitted")
}

// --- administrator actions -------------------------------------------------------------------

func TestAnAdminLifecycleChangeNamesTheAdminAndKeepsTheReason(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	var buf bytes.Buffer
	restore := captureLog(&buf)
	code, raw := adminTowerPost(t, srv, "/tower/lifecycle",
		`{"tower_id":"`+lt.id+`","state":"active","reason":"checked with operator"}`)
	restore()
	require.Equal(t, http.StatusOK, code, raw)

	evs := towerEventsOf(t, b, lt.id, "lifecycle")
	require.Len(t, evs, 1)
	require.Equal(t, "admin:broker-key", evs[0].Actor)
	require.Equal(t, admit.StateQuarantine, evs[0].From)
	require.Equal(t, admit.StateActive, evs[0].To)
	require.Equal(t, "checked with operator", evs[0].Reason)
	require.Contains(t, buf.String(), "actor=admin:broker-key from=quarantine to=active")
	require.NotContains(t, buf.String(), "by an administrator", "the log names the administrator")
	require.NotContains(t, buf.String(), "admin-secret")
}

func TestAnAdminLifecycleChangeNeedsNoReason(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	code, raw := adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"active"}`)
	require.Equal(t, http.StatusOK, code, raw)
	evs := towerEventsOf(t, b, lt.id, "lifecycle")
	require.Len(t, evs, 1)
	require.Empty(t, evs[0].Reason)
}

func TestARefusedAdminLifecycleChangeIsRecorded(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	code, _ := adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"quarantine"}`)
	require.Equal(t, http.StatusConflict, code)
	code, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"draining"}`)
	require.Equal(t, http.StatusConflict, code)

	evs := towerEventsOf(t, b, lt.id, "lifecycle_refused")
	require.Len(t, evs, 2)
	require.Equal(t, "illegal_transition", evs[0].Reason)
	require.Equal(t, admit.StateDraining, evs[0].To)
	require.Equal(t, "already_in_state", evs[1].Reason)
	require.Equal(t, "admin:broker-key", evs[1].Actor)
	require.Equal(t, admit.StateQuarantine, evs[1].From)
}

func TestAnAdminRequestForAnUnknownTowerOrStateIsLoggedOnly(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	var buf bytes.Buffer
	restore := captureLog(&buf)
	code, _ := adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"tw-nope","state":"active"}`)
	require.Equal(t, http.StatusNotFound, code)
	code, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"bogus"}`)
	require.Equal(t, http.StatusBadRequest, code)
	restore()
	require.Contains(t, buf.String(), "tower-event kind=lifecycle_refused tower=tw-nope actor=admin:broker-key")
	require.Contains(t, buf.String(), "reason=unknown_state")
	require.Empty(t, towerEventsOf(t, b, "tw-nope"))
	require.Empty(t, towerEventsOf(t, b, lt.id))
}

// A CHANGE CORE CANNOT RECORD IS A CHANGE CORE DID NOT MAKE.
func TestAnAdminLifecycleChangeThatCannotBeRecordedDoesNotHappen(t *testing.T) {
	b, srv := towerTestBrokerWithAdmitStore(t, &eventFailingStore{Store: admit.NewMemStore()})
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	code, raw := adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"active"}`)
	require.Equal(t, http.StatusServiceUnavailable, code, raw)
	require.Contains(t, raw, "not made")
	tw, _ := b.tower.registry.Get(lt.id)
	require.Equal(t, admit.StateQuarantine, tw.State)
}

// --- operator actions ---------------------------------------------------------------------

func TestAnOperatorsOwnLifecycleChangeIsRecordedAndARefusalToo(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	// Refused first: an operator may not promote out of quarantine.
	code, _ := op.call(t, srv, http.MethodPost, "/tower/self/lifecycle",
		map[string]string{"tower_id": lt.id, "state": "active"}, nil)
	require.NotEqual(t, http.StatusOK, code)
	refused := towerEventsOf(t, b, lt.id, "lifecycle_refused")
	require.Len(t, refused, 1)
	require.Equal(t, "operator:octocat", refused[0].Actor)

	_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"active"}`)
	code, raw := op.call(t, srv, http.MethodPost, "/tower/self/lifecycle",
		map[string]string{"tower_id": lt.id, "state": "draining"}, nil)
	require.Equal(t, http.StatusOK, code, raw)
	evs := towerEventsOf(t, b, lt.id, "lifecycle")
	require.Len(t, evs, 2)
	require.Equal(t, "operator:octocat", evs[0].Actor)
	require.Equal(t, admit.StateActive, evs[0].From)
	require.Equal(t, admit.StateDraining, evs[0].To)
}

// --- reading the record -----------------------------------------------------------------------

func TestTheAdminDetailShowsTheLinkHistoryAndTheEvents(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }

	// Never connected, approved: the detail says so, and by whom.
	_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"active"}`)
	detail := adminTowerDetailOf(t, srv, lt.id)
	conn := detail["connection"].(map[string]any)
	require.Equal(t, "never connected", conn["status"])
	evs := detail["events"].([]any)
	require.Len(t, evs, 1)
	require.Equal(t, "admin:broker-key", evs[0].(map[string]any)["actor"])

	acc := openSessionOf(t, srv.URL, lt, call)
	conn = adminTowerDetailOf(t, srv, lt.id)["connection"].(map[string]any)
	require.Equal(t, "connected", conn["status"])
	require.NotEmpty(t, conn["first_connected"])
	require.NotEmpty(t, conn["last_seen"])

	_, _ = call("/tower/session/close", jsonOf(t, frameFor(lt, acc.SessionID)), nil)
	conn = adminTowerDetailOf(t, srv, lt.id)["connection"].(map[string]any)
	require.Equal(t, "closed", conn["status"])
	require.NotEmpty(t, conn["last_seen"], "last seen survives an orderly close")
	require.NotEmpty(t, conn["last_closed"])
}

func TestTheAdminListShowsWhetherEachTowerIsConnected(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	never := enrolledTower(t, b, op.login)
	live := enrolledTower(t, b, op.login)
	_ = openSessionOf(t, srv.URL, live, func(p string, body []byte, out any) (int, string) { return live.call(t, srv, p, body, out) })

	var buf bytes.Buffer
	restore := captureLog(&buf)
	rows := adminTowersOf(t, srv)
	restore()
	byID := map[string]map[string]any{}
	for _, r := range rows {
		byID[r["tower_id"].(string)] = r
	}
	require.Equal(t, "never connected", byID[never.id]["connection"])
	require.Equal(t, "connected", byID[live.id]["connection"])
	require.NotEmpty(t, byID[live.id]["last_seen"])
	require.Contains(t, buf.String(), "admin read: towers list by admin:broker-key")
}

func TestAdminReadsOfADetailAreLoggedNotRecorded(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	var buf bytes.Buffer
	restore := captureLog(&buf)
	_ = adminTowerDetailOf(t, srv, lt.id)
	restore()
	require.Contains(t, buf.String(), "admin read: tower "+lt.id+" by admin:broker-key")
	require.Empty(t, towerEventsOf(t, b, lt.id))
}

// --- failure handling -------------------------------------------------------------------------

func TestLinkEventsNeverBlockATower(t *testing.T) {
	b, srv := towerTestBrokerWithAdmitStore(t, &eventFailingStore{Store: admit.NewMemStore()})
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	call := func(p string, body []byte, out any) (int, string) { return lt.call(t, srv, p, body, out) }

	var buf bytes.Buffer
	restore := captureLog(&buf)
	acc := openSessionOf(t, srv.URL, lt, call)
	for i := 0; i < 3; i++ {
		code, raw := call("/tower/session/heartbeat", jsonOf(t, frameFor(lt, acc.SessionID)), nil)
		require.Equal(t, http.StatusOK, code, raw)
	}
	code, raw := call("/tower/session/close", jsonOf(t, frameFor(lt, acc.SessionID)), nil)
	restore()
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, 1, strings.Count(buf.String(), "governance record unavailable"),
		"the failure is logged once per minute, not once per request")
}

// --- helpers ---------------------------------------------------------------------------------

// eventFailingStore is an admission store whose governance record cannot be written.
type eventFailingStore struct{ admit.Store }

func (e *eventFailingStore) AppendEvent(admit.Event) error { return admit.ErrUnavailable }
func (e *eventFailingStore) CASTowerEvent(admit.Tower, admit.Event) (bool, error) {
	return false, admit.ErrUnavailable
}
func (e *eventFailingStore) AppendEventIfLast(admit.Event, []string, int64) (bool, error) {
	return false, admit.ErrUnavailable
}

func adminGetJSON(t *testing.T, srvURL, path string, out any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srvURL+path, nil)
	require.NoError(t, err)
	req.Header.Set("X-Roger-Admin", "admin-secret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
}

func adminTowerDetailOf(t *testing.T, srv *httptest.Server, id string) map[string]any {
	t.Helper()
	var out map[string]any
	adminGetJSON(t, srv.URL, "/admin/tower?id="+id, &out)
	return out
}

func adminTowersOf(t *testing.T, srv *httptest.Server) []map[string]any {
	t.Helper()
	var out struct {
		Towers []map[string]any `json:"towers"`
	}
	adminGetJSON(t, srv.URL, "/admin/towers", &out)
	return out.Towers
}

// towerTestBrokerWithAdmitStore is towerTestBroker over a chosen admission store.
func towerTestBrokerWithAdmitStore(t *testing.T, as admit.Store) (*broker, *httptest.Server) {
	t.Helper()
	b := testBrokerWithDB(store.NewMem())
	ts, err := newTowerSubsystem(b, as, cert.NewMemCustody(), enroll.NewMemStore(),
		cert.Config{TTL: time.Hour},
		linkDeps{stations: attach.NewMemStore(), heads: head.NewMemStore()})
	require.NoError(t, err)
	b.tower = ts
	mux := http.NewServeMux()
	b.registerTowerRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return b, srv
}
