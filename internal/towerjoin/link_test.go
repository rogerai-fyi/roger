package towerjoin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v5/internal/tower"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

// THE PROTOCOL FINALLY HAS TWO PARTICIPANTS.
//
// Core's link routes were exercised only by tests speaking HTTP directly, so every property
// of the wire format was asserted from one side. These tests drive the REAL client against a
// stub Core, which is the half that was missing - in particular the 409-resend / 400-refuse
// distinction, which Core has always drawn and nothing was in a position to act on.
//
// The stub is local on purpose. internal/towerjoin's broker base defaults to production, and
// a test that forgets to pin it posts to the live network - which this repo has done once
// already and does not intend to do again.

// registeredTower is joinedTower (join_test.go) plus the admission record `register` would
// have left behind, which is what the link requires.
func registeredTower(t *testing.T) *tower.State {
	t.Helper()
	st := joinedTower(t)
	require.NoError(t, saveAdmission(st.Dir(), Admission{TowerID: "tw-1"}))
	return st
}

// linkCore records what the client sent and replies with what the test wants.
type linkCore struct {
	t         *testing.T
	srv       *httptest.Server
	seen      []string
	sessions  int
	inventory []byte
	reply     map[string]func(w http.ResponseWriter)
}

func newLinkCore(t *testing.T) *linkCore {
	t.Helper()
	c := &linkCore{t: t, reply: map[string]func(w http.ResponseWriter){}}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.seen = append(c.seen, r.URL.Path)

		// Every link call must be signed AS THE TOWER. Asserting it here is the only place
		// the client's side of that rule is checked.
		require.NotEmpty(c.t, r.Header.Get("X-Roger-Pubkey"), "%s was unsigned", r.URL.Path)
		require.NotEmpty(c.t, r.Header.Get("X-Roger-Sig"), "%s carried no signature", r.URL.Path)

		if fn, ok := c.reply[r.URL.Path]; ok {
			fn(w)
			return
		}
		switch r.URL.Path {
		case "/tower/session":
			c.sessions++
			_ = json.NewEncoder(w).Encode(link.Accepted{
				Version: 1, SessionID: "sess-1", HeartbeatSeconds: 60, FreshnessSeconds: 180,
				NeedFullInventory: true,
			})
		case "/tower/inventory":
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			c.inventory = body
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "revision": 1, "hash": "hash-1", "routable": 0,
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	t.Setenv("ROGER_BROKER", c.srv.URL)
	t.Cleanup(c.srv.Close)
	return c
}

func TestTheClientOpensASessionAndPushesAnInventory(t *testing.T) {
	core := newLinkCore(t)
	st := registeredTower(t)

	sess, err := OpenSession(st, Head{}, link.RelayPlane{})
	require.NoError(t, err)
	require.Equal(t, "tw-1", sess.TowerID)
	require.Equal(t, "sess-1", sess.SessionID)
	require.True(t, sess.NeedFullInventory, "a first connect is told to send everything")

	res, err := PushFullInventory(st, 1, "genesis", nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), res.Revision)

	// A Tower with nothing attached still sends a VALID inventory - "I am here and I have
	// nothing" - rather than skipping the push and looking absent.
	require.Contains(t, string(core.inventory), `"leaves":[]`)
	require.Contains(t, string(core.inventory), `"tower_id":"tw-1"`)
	require.Contains(t, string(core.inventory), `"sig":`, "signed by the Tower's identity key")

	require.NoError(t, sess.SendHeartbeat(st))
	require.NoError(t, sess.Close(st))
	require.Equal(t, []string{
		"/tower/session", "/tower/inventory", "/tower/session/heartbeat", "/tower/session/close",
	}, core.seen)
}

// THE DISTINCTION THIS FILE EXISTS TO CONSUME. Core answers 409 with need_full_inventory
// when it cannot place what we sent, and 400 when the thing will never be accepted. A client
// that cannot tell them apart retries the wrong one - forever, in the second case.
func TestTheClientTellsResendApartFromRefuse(t *testing.T) {
	t.Run("409 need_full_inventory asks for a snapshot", func(t *testing.T) {
		core := newLinkCore(t)
		st := registeredTower(t)
		core.reply["/tower/inventory"] = func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"ok":false,"need_full_inventory":true,"error":"cannot place it"}`))
		}
		_, err := PushFullInventory(st, 5, "some-hash", nil)
		require.ErrorIs(t, err, ErrNeedFullInventory)
		require.NotErrorIs(t, err, ErrRefused, "resend and refuse call for opposite behaviour")
	})

	t.Run("400 is a refusal to fix, not to retry", func(t *testing.T) {
		core := newLinkCore(t)
		st := registeredTower(t)
		core.reply["/tower/inventory"] = func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"revision 5 skips 1"}}`))
		}
		_, err := PushFullInventory(st, 5, "some-hash", nil)
		require.ErrorIs(t, err, ErrRefused)
		require.NotErrorIs(t, err, ErrNeedFullInventory)
		require.Contains(t, err.Error(), "skips",
			"the sentence Core wrote is what an operator needs, not the JSON envelope")
	})

	// A 403 CARRYING A REASON REPORTS THAT REASON.
	//
	// This subtest previously asserted the opposite - that every 403 became "this Tower may
	// not hold a link (suspended, revoked, or its lease lapsed)" - and that was wrong in a
	// way only visible once a second route started answering 403. It is the right sentence
	// for the session routes and a misdirection everywhere else: a Station attachment
	// refused over a mistyped invitation secret would send its operator off to investigate
	// their Tower's lifecycle, which is fine. The canned line is the FALLBACK below, not the
	// answer to every 403.
	t.Run("403 reports the reason Core gave", func(t *testing.T) {
		core := newLinkCore(t)
		st := registeredTower(t)
		core.reply["/tower/session"] = func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"this link requires a registered Tower's own signed request"}}`))
		}
		_, err := OpenSession(st, Head{}, link.RelayPlane{})
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), "own signed request",
			"the sentence Core wrote is what an operator can act on")
	})

	// And when Core says nothing at all, the three reasons an admitted Tower gets turned
	// away are still worth naming: an operator staring at a bare 403 has nowhere to start.
	t.Run("403 with no reason names the three that are possible", func(t *testing.T) {
		core := newLinkCore(t)
		st := registeredTower(t)
		core.reply["/tower/session"] = func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusForbidden)
		}
		_, err := OpenSession(st, Head{}, link.RelayPlane{})
		require.ErrorIs(t, err, ErrRefused)
		require.Contains(t, err.Error(), "lease lapsed")
	})
}

// A transport failure is not a refusal: one calls for a backoff and the other for a fix.
func TestAnUnreachableCoreIsNotARefusal(t *testing.T) {
	st := registeredTower(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")

	_, err := OpenSession(st, Head{}, link.RelayPlane{})
	require.ErrorIs(t, err, ErrUnreachable)
	require.NotErrorIs(t, err, ErrRefused)
}

// Quoting a head is what makes a reconnect cheap when Core is in step.
func TestAReconnectQuotesTheHeadItHolds(t *testing.T) {
	core := newLinkCore(t)
	st := registeredTower(t)
	var got link.Hello
	core.reply["/tower/session"] = func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(link.Accepted{
			Version: 1, SessionID: "sess-2", HeartbeatSeconds: 60, NeedFullInventory: false,
		})
	}
	core.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = io.ReadFull(r.Body, body)
		_ = json.Unmarshal(body, &got)
		_ = json.NewEncoder(w).Encode(link.Accepted{
			Version: 1, SessionID: "sess-2", HeartbeatSeconds: 60, NeedFullInventory: false,
		})
	})

	sess, err := OpenSession(st, Head{Revision: 7, Hash: "hash-7"}, link.RelayPlane{})
	require.NoError(t, err)
	require.False(t, sess.NeedFullInventory, "Core is in step, so no snapshot is demanded")
	require.Equal(t, int64(7), got.HeadRevision, "the client quotes what it holds")
	require.Equal(t, "hash-7", got.HeadHash)
}

// An unregistered Tower is told what to do rather than failing at the transport.
func TestServingBeforeRegisteringSaysSo(t *testing.T) {
	st := joinedTower(t) // no admission record: never registered

	_, err := OpenSession(st, Head{}, link.RelayPlane{})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "register"),
		"the error must name the command that fixes it")
}

// A Tower that has never registered cannot push either - and is told which command fixes it,
// rather than being handed a transport error from a request that should never have been made.
func TestPushingBeforeRegisteringSaysSo(t *testing.T) {
	newLinkCore(t)
	st := joinedTower(t)

	_, err := PushFullInventory(st, 1, "genesis", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not registered")
}

// Station-signed leaves are relayed BYTE FOR BYTE. A Tower that re-encoded an offer could
// change what the Station signed, which would make "signed by the Station" mean nothing.
func TestLeavesAreRelayedWithoutBeingRewritten(t *testing.T) {
	core := newLinkCore(t)
	st := registeredTower(t)

	leaf := json.RawMessage(`{"offer_id":"o-1","station_id":"st-1","station_sig":"abc"}`)
	_, err := PushFullInventory(st, 1, "genesis", []json.RawMessage{leaf})
	require.NoError(t, err)

	// Every member the Station signed survives the trip.
	sent := string(core.inventory)
	require.Contains(t, sent, `"station_sig":"abc"`)
	require.Contains(t, sent, `"offer_id":"o-1"`)
	require.Contains(t, sent, `"station_id":"st-1"`)
}

// A reply that is not JSON is a broken Core, not a refusal the operator can act on - and it
// must not be reported as if the inventory were at fault.
func TestAnUnreadableReplyIsReportedAsSuch(t *testing.T) {
	core := newLinkCore(t)
	st := registeredTower(t)
	core.reply["/tower/inventory"] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte("this is not json"))
	}
	_, err := PushFullInventory(st, 1, "genesis", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "could not read")
}

// A refusal with no JSON envelope still surfaces whatever Core said, rather than an empty
// message an operator cannot act on.
func TestARefusalWithoutAnEnvelopeStillSaysSomething(t *testing.T) {
	core := newLinkCore(t)
	st := registeredTower(t)
	core.reply["/tower/inventory"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream exploded"))
	}
	_, err := PushFullInventory(st, 1, "genesis", nil)
	require.ErrorIs(t, err, ErrRefused)
	require.Contains(t, err.Error(), "upstream exploded")
}

// A 409 that is NOT a resend request is still a refusal - the client must not read every
// conflict as "send everything again".
func TestAConflictThatIsNotAResendIsARefusal(t *testing.T) {
	core := newLinkCore(t)
	st := registeredTower(t)
	core.reply["/tower/inventory"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"open a session before pushing inventory"}`))
	}
	_, err := PushFullInventory(st, 1, "genesis", nil)
	require.ErrorIs(t, err, ErrRefused)
	require.NotErrorIs(t, err, ErrNeedFullInventory)
	require.Contains(t, err.Error(), "open a session")
}

// Heartbeat and close report an unreachable Core as transport, not as refusal: the caller
// backs off rather than tearing the session down.
func TestHeartbeatAndCloseDistinguishTransportFailure(t *testing.T) {
	core := newLinkCore(t)
	st := registeredTower(t)
	sess, err := OpenSession(st, Head{}, link.RelayPlane{})
	require.NoError(t, err)

	core.srv.Close() // Core goes away mid-session
	require.ErrorIs(t, sess.SendHeartbeat(st), ErrUnreachable)
	require.ErrorIs(t, sess.Close(st), ErrUnreachable)
}
