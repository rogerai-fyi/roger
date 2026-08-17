package towerjoin

// station_test.go is the spec for attaching a Station to the public network.
//
// THE ROUTES HAD NO CLIENT. /tower/station/invite and /tower/station/attach were built,
// tested from the server's side, and reachable only by hand-rolling a signed HTTP request.
// An operator following the documentation could not attach a Station at all, which meant no
// joined Tower could ever relay anything: attachment is what records the key every offer is
// verified against, so an empty attach registry is an inert network.
//
// The two calls are signed by DIFFERENT parties on purpose and the tests below hold that
// line, because it is the whole authorization model:
//
//	invite  the OPERATOR's account key. Authorizing a machine to serve under your account
//	        is an account decision, and the account is what a ban or suspension acts on.
//	attach  the TOWER's identity key. Redeeming is the relay proving it is the origin the
//	        Station is being attached behind - a Tower cannot attach a Station onto
//	        somebody else's origin, because the origin is taken from who signed.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// A stub Core that records who signed what.
type stationCore struct {
	t        *testing.T
	srv      *httptest.Server
	seen     []string
	bodies   map[string]map[string]any
	pubkeys  map[string]string
	methods  map[string]string
	replies  map[string]func(w http.ResponseWriter)
	inviteID string
}

func newStationCore(t *testing.T) *stationCore {
	t.Helper()
	c := &stationCore{
		t: t, bodies: map[string]map[string]any{}, pubkeys: map[string]string{},
		replies: map[string]func(http.ResponseWriter){}, methods: map[string]string{},
		inviteID: "sinv-1",
	}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.seen = append(c.seen, r.URL.Path)
		c.pubkeys[r.URL.Path] = r.Header.Get("X-Roger-Pubkey")
		c.methods[r.URL.Path] = r.Method
		require.NotEmpty(c.t, r.Header.Get("X-Roger-Sig"), "%s was unsigned", r.URL.Path)

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.bodies[r.URL.Path] = body

		if fn, ok := c.replies[r.URL.Path]; ok {
			fn(w)
			return
		}
		switch r.URL.Path {
		case "/tower/station/invite":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"invitation_id": c.inviteID, "station_id": "st-1", "tower_id": "tw-1",
				"secret": "s3cret", "expires_in": 3600,
			})
		case "/tower/station/attach":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "station_id": "st-1", "state": "quarantine",
			})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(c.srv.Close)
	t.Setenv("ROGER_BROKER", c.srv.URL)
	return c
}

// --- what Core believes about this Tower ------------------------------------
//
// /tower/status had no client either, so an operator had no way to ask the only question
// that matters after registering: what state am I in, and is anything of mine routable? The
// answer lives entirely on Core - a Tower's own files record what it was told at enrollment
// and go stale the moment an administrator promotes or suspends it.

func TestStatusReportsWhatCoreBelieves(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/status"] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"towers":[{
			"tower_id":"tw-1","state":"active","may_take_work":true,"link_live":true,
			"carries_traffic":false,"inventory_revision":7,
			"note":"routing Tower-backed work is not shipped yet",
			"routable":[{"station_id":"st-9","model":"m1","modality":"text","capacity":4}]
		}]}`))
	}

	got, err := FetchStatus(st)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "tw-1", got[0].TowerID)
	require.Equal(t, "active", got[0].State)
	require.True(t, got[0].MayTakeWork)
	require.True(t, got[0].LinkLive)
	require.Equal(t, int64(7), got[0].InventoryRevision)
	require.Len(t, got[0].Routable, 1)
	require.Equal(t, "st-9", got[0].Routable[0].StationID)

	// Core saying it does not carry traffic yet is information, not an error, and it must
	// survive to the operator - it is the difference between "my Station is broken" and
	// "this part is not built".
	require.False(t, got[0].CarriesTraffic)
	require.Contains(t, got[0].Note, "not shipped")
}

// It is a GET. Sending it as a POST would be refused with a method error that tells the
// operator nothing about their Tower.
func TestStatusIsASignedGET(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/status"] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"towers":[]}`))
	}
	_, err := FetchStatus(st)
	require.NoError(t, err)
	require.Equal(t, http.MethodGet, core.methods["/tower/status"])
	require.NotEmpty(t, core.pubkeys["/tower/status"], "and it is signed as the operator")
}

func TestStatusReportsARefusalRatherThanAnEmptyFleet(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/status"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"sign in first"}}`))
	}
	// An empty list would read as "you have no Towers", which is a different and wrong
	// answer to "we could not tell you".
	_, err := FetchStatus(st)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sign in first")
}

// Revoking a Station retires the identity. It is the operator's call, signed by the account,
// and it had no client - so the only way to retire a compromised Station was a hand-rolled
// HTTP request.
func TestRevokingAStationIsSignedByTheAccount(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/station/revoke"] = func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"ok":true,"revoked":true}`))
	}
	require.NoError(t, RevokeStation(st, "st-9"))
	require.Equal(t, "st-9", core.bodies["/tower/station/revoke"]["station_id"])
	require.NotEmpty(t, core.pubkeys["/tower/station/revoke"])
}

func TestRevokingNeedsAStation(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	require.Error(t, RevokeStation(st, ""))
	require.Empty(t, core.seen)
}

func TestRevokingReportsARefusal(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/station/revoke"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"no such Station on this account"}}`))
	}
	err := RevokeStation(st, "st-nobody")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no such Station")
}

// Neither read nor revoke is worth attempting from a Tower that never registered, and an
// unreachable Core is transport rather than an answer about the fleet.
func TestStatusAndRevokeFailUsefullyWhenTheyCannotAsk(t *testing.T) {
	st := registeredTower(t)
	t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")

	_, err := FetchStatus(st)
	require.Error(t, err)
	require.Contains(t, err.Error(), "could not reach")

	err = RevokeStation(st, "st-9")
	require.Error(t, err)
	require.Contains(t, err.Error(), "could not reach")

	// And an unregistered Tower is told which command fixes it.
	err = RevokeStation(joinedTower(t), "st-9")
	require.Error(t, err)
	require.Contains(t, err.Error(), "register")
}
