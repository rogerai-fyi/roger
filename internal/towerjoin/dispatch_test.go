package towerjoin

// dispatch_test.go covers the Tower's courier leg: collecting work, carrying it to a Station,
// and carrying the answer back.
//
// The property under test throughout is that NOTHING IS ALTERED IN TRANSIT. The Tower is the
// untrusted party in this exchange and holds every byte of it; the grant commits to a digest
// of the request and the receipt to a digest of the answer, so a Tower that re-encodes either
// one turns a good result into a rejected one.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
)

func TestPollingReturnsTheWorkCoreHandedOut(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/dispatch"] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"attempt_id":"att-1","grant":{"station_id":"st-1"},"request":{"m":1}}`))
	}
	got, err := PollDispatch(st, nil)
	require.NoError(t, err)
	require.Equal(t, "att-1", got.AttemptID)
	require.JSONEq(t, `{"station_id":"st-1"}`, string(got.Grant))
	require.JSONEq(t, `{"m":1}`, string(got.Request))
	require.Equal(t, "tw-1", core.bodies["/tower/dispatch"]["tower_id"])
}

// AN IDLE FLEET SPENDS MOST OF ITS TIME HERE, so "nothing to do" must be its own answer
// rather than an error the loop backs off on - and a 200 with nothing in it must not be
// mistaken for work, or the loop spins.
func TestNothingToCollectIsNotAFailure(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)

	core.replies["/tower/dispatch"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusNoContent)
	}
	_, err := PollDispatch(st, nil)
	require.ErrorIs(t, err, ErrNoWork)

	core.replies["/tower/dispatch"] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{}`))
	}
	_, err = PollDispatch(st, nil)
	require.ErrorIs(t, err, ErrNoWork, "a 200 with no attempt in it is not work")
}

func TestPollingReportsARefusalAndNeedsARegisteredTower(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/dispatch"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"this Tower is not eligible to take work yet"}}`))
	}
	_, err := PollDispatch(st, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not eligible")

	_, err = PollDispatch(joinedTower(t), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "register")
}

// THE ANSWER IS RELAYED VERBATIM. The receipt commits to a digest of these exact bytes, so a
// Tower that re-encoded them - a map round trip, a re-indent - would invalidate a perfectly
// good result and look, from Core, exactly like tampering.
func TestTheResultIsReturnedByteForByte(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/dispatch/result"] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}

	body := json.RawMessage(`{"answer":"  keep   my   spacing  ","n":1}`)
	require.NoError(t, ReturnResult(st, "att-1", station.ExecuteResponse{
		Receipt: &dispatch.Receipt{AttemptID: "att-1", ResponseDigest: "d"},
		Body:    body,
	}))

	sent := core.bodies["/tower/dispatch/result"]
	require.Equal(t, "att-1", sent["attempt_id"])
	require.Equal(t, "tw-1", sent["tower_id"])
	got, err := json.Marshal(sent["body"])
	require.NoError(t, err)
	require.Contains(t, string(got), "  keep   my   spacing  ")
	require.NotContains(t, sent, "failure")
}

// A FAILURE CARRIES NO RECEIPT, and a result with no receipt is reported as a failure rather
// than sent as an answer. Either way Core is told, because Core has a caller waiting.
func TestAFailureIsReportedAsOneAndNeverAsAResult(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/dispatch/result"] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}

	require.NoError(t, ReturnResult(st, "att-1",
		station.ExecuteResponse{Failure: "the model is not loaded"}))
	require.Equal(t, "the model is not loaded", core.bodies["/tower/dispatch/result"]["failure"])
	require.NotContains(t, core.bodies["/tower/dispatch/result"], "receipt")

	// A body with no receipt is not a result: signing is what makes it one.
	require.NoError(t, ReturnResult(st, "att-2", station.ExecuteResponse{
		Body: json.RawMessage(`{"answer":"unsigned"}`),
	}))
	failure, _ := core.bodies["/tower/dispatch/result"]["failure"].(string)
	require.Contains(t, failure, "no receipt")

	require.Error(t, ReturnResult(joinedTower(t), "att-1", station.ExecuteResponse{}))
}

// --- carrying it to the Station --------------------------------------------

func TestTheStationSeesExactlyWhatCoreSent(t *testing.T) {
	var saw station.ExecuteRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&saw))
		_, _ = w.Write([]byte(`{"receipt":{"attempt_id":"att-1"},"body":{"ok":true}}`))
	}))
	defer srv.Close()

	work := Work{
		AttemptID: "att-1",
		Grant:     json.RawMessage(`{"station_id":"st-1","nonce":"abc"}`),
		Request:   json.RawMessage(`{"model":"m1","messages":[]}`),
	}
	resp := RelayToStation(srv.URL, work, nil)
	require.Empty(t, resp.Failure)
	require.NotNil(t, resp.Receipt)
	require.JSONEq(t, string(work.Grant), string(saw.Grant))
	require.Equal(t, string(work.Request), string(saw.Request))
}

// EVERY WAY THE STATION LEG CAN FAIL becomes a Failure, never a dropped attempt. Core is
// waiting on the other side, and a fast honest failure beats a deadline.
func TestEveryStationFailureBecomesAReportableOne(t *testing.T) {
	work := Work{AttemptID: "att-1", Grant: json.RawMessage(`{}`), Request: json.RawMessage(`{}`)}

	t.Run("unreachable", func(t *testing.T) {
		resp := RelayToStation("http://127.0.0.1:1/execute", work, nil)
		require.Contains(t, resp.Failure, "could not be reached")
	})

	t.Run("an error status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		require.Contains(t, RelayToStation(srv.URL, work, nil).Failure, "replied 500")
	})

	t.Run("an unreadable answer", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))
		defer srv.Close()
		require.Contains(t, RelayToStation(srv.URL, work, nil).Failure, "not readable")
	})

	t.Run("not an address", func(t *testing.T) {
		require.NotEmpty(t, RelayToStation("://nope", work, nil).Failure)
	})
}

// The long-poll client's timeout must EXCEED Core's poll window, or every quiet poll would
// look like a transport failure to the Tower and like a disconnect to Core.
func TestTheDispatchClientOutwaitsCoresPoll(t *testing.T) {
	require.Greater(t, DispatchClient().Timeout, dispatchPollWaitUpperBound,
		"a client that gives up before Core answers turns an idle fleet into an error loop")
}

// dispatchPollWaitUpperBound is Core's poll window as this Tower must assume it. Written
// here rather than imported because the two are separate programs: a Tower cannot read
// Core's constant, and the relationship still has to hold.
const dispatchPollWaitUpperBound = 30 * time.Second

// --- pausing, resuming and retiring, from the operator's side ---------------

func TestSettingOwnStateIsSignedByTheAccount(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/self/lifecycle"] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"ok":true,"state":"draining"}`))
	}
	require.NoError(t, SetOwnState(st, "draining"))

	sent := core.bodies["/tower/self/lifecycle"]
	require.Equal(t, "tw-1", sent["tower_id"], "the Tower comes from our own admission record")
	require.Equal(t, "draining", sent["state"])
	// Signed by the ACCOUNT, because retiring hardware has to work when the Tower itself is
	// the thing that has gone wrong.
	require.NotEmpty(t, core.pubkeys["/tower/self/lifecycle"])
}

func TestSettingOwnStateReportsARefusalAndNeedsRegistration(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/self/lifecycle"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"moving it from quarantine to \"active\" is an administrator's decision"}}`))
	}
	err := SetOwnState(st, "active")
	require.Error(t, err)
	require.Contains(t, err.Error(), "administrator")

	err = SetOwnState(joinedTower(t), "draining")
	require.Error(t, err)
	require.Contains(t, err.Error(), "register")
}
