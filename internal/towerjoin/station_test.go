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
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestInvitingAStationReturnsTheOneTimeSecret(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)

	got, err := InviteStation(st, StationKeys{
		StationID: "st-1", AssertionKey: "aa", SessionKey: "bb",
	})
	require.NoError(t, err)
	require.Equal(t, "sinv-1", got.InvitationID)
	require.Equal(t, "st-1", got.StationID)
	require.Equal(t, "s3cret", got.Secret)

	// The Tower it is for comes from this Tower's own admission record, not from a flag: an
	// operator naming the wrong Tower would authorize a Station behind a relay they meant
	// nothing by.
	require.Equal(t, "tw-1", core.bodies["/tower/station/invite"]["tower_id"])
	require.Equal(t, "aa", core.bodies["/tower/station/invite"]["assertion_key"])
	require.Equal(t, "bb", core.bodies["/tower/station/invite"]["session_key"])
}

// ATTACH IS SIGNED BY THE TOWER, INVITE BY THE OPERATOR. If both were signed by the same
// key the origin check would be meaningless - Core takes the attaching Tower from whoever
// signed, precisely so a relay cannot attach a Station behind a different one.
func TestInviteAndAttachAreSignedByDifferentKeys(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)

	_, err := InviteStation(st, StationKeys{StationID: "st-1", AssertionKey: "aa", SessionKey: "bb"})
	require.NoError(t, err)
	_, err = AttachStation(st, Invitation{
		InvitationID: "sinv-1", Secret: "s3cret",
		Keys: StationKeys{StationID: "st-1", AssertionKey: "aa", SessionKey: "bb"},
	})
	require.NoError(t, err)

	operatorKey := core.pubkeys["/tower/station/invite"]
	towerKey := core.pubkeys["/tower/station/attach"]
	require.NotEmpty(t, operatorKey)
	require.NotEmpty(t, towerKey)
	require.NotEqual(t, operatorKey, towerKey,
		"the operator's account key and the Tower's identity key must not be the same key")
}

// The attach call carries the OWNER, because the invitation was recorded against the account
// that authorized it and Core matches them. Sending the login name instead of the account
// pubkey is a real bug this route already hit once on the server side.
func TestAttachNamesTheAccountThatAuthorizedIt(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)

	_, err := InviteStation(st, StationKeys{StationID: "st-1", AssertionKey: "aa", SessionKey: "bb"})
	require.NoError(t, err)
	_, err = AttachStation(st, Invitation{
		InvitationID: "sinv-1", Secret: "s3cret",
		Keys: StationKeys{StationID: "st-1", AssertionKey: "aa", SessionKey: "bb"},
	})
	require.NoError(t, err)

	body := core.bodies["/tower/station/attach"]
	require.Equal(t, "tw-1", body["tower_id"])
	require.Equal(t, "sinv-1", body["invitation_id"])
	require.Equal(t, "s3cret", body["secret"])

	// The owner is the ACCOUNT key - the one that signed the invite - and NOT the Tower
	// identity key this very request is signed with. They are different keys and the
	// distinction is the point: the invitation was recorded against the account, which is
	// what a ban or suspension acts on, while the signature proves which relay is redeeming.
	require.Equal(t, core.pubkeys["/tower/station/invite"], body["owner"],
		"the attachment must record the account that authorized it")
	require.NotEqual(t, core.pubkeys["/tower/station/attach"], body["owner"],
		"the owner is the account, not the Tower doing the redeeming")
}

func TestAttachReportsTheStateCoreRecorded(t *testing.T) {
	newStationCore(t)
	st := registeredTower(t)

	got, err := AttachStation(st, Invitation{
		InvitationID: "sinv-1", Secret: "s3cret",
		Keys: StationKeys{StationID: "st-1", AssertionKey: "aa", SessionKey: "bb"},
	})
	require.NoError(t, err)
	require.Equal(t, "st-1", got.StationID)
	// Quarantine is the expected answer and not a failure: a Station is never trusted with
	// public work on arrival.
	require.Equal(t, "quarantine", got.State)
}

// A refusal is uniform by design on the server - which check refused it is an oracle a
// Station has no business probing - so the client's job is to pass the sentence through
// rather than dress it up as something more specific than Core was willing to say.
func TestARefusedRedemptionIsReportedAsCoreWroteIt(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/station/attach"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"that invitation cannot be redeemed"}}`))
	}
	_, err := AttachStation(st, Invitation{
		InvitationID: "sinv-1", Secret: "nope",
		Keys: StationKeys{StationID: "st-1", AssertionKey: "aa", SessionKey: "bb"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be redeemed")
}

func TestInvitingRefusesWhenTheOwnerHasTooManyOpen(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/station/invite"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"you already have 25 unredeemed Station invitations"}}`))
	}
	_, err := InviteStation(st, StationKeys{StationID: "st-1", AssertionKey: "aa", SessionKey: "bb"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unredeemed")
}

// Neither call is worth making without a registered Tower, and saying which command fixes it
// beats a transport error from a request that should never have been sent.
func TestNeitherCallIsMadeBeforeRegistering(t *testing.T) {
	core := newStationCore(t)
	st := joinedTower(t)

	_, err := InviteStation(st, StationKeys{StationID: "st-1", AssertionKey: "aa", SessionKey: "bb"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "register")

	_, err = AttachStation(st, Invitation{InvitationID: "i", Secret: "s"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "register")

	require.Empty(t, core.seen, "nothing was sent")
}

// Both keys are required, and they must be different: one key doing both jobs means
// compromising the channel hands over the ability to sign offers too. Core refuses it, and
// so does this - the operator finds out before a round trip.
func TestBothKeysAreRequiredAndMustDiffer(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)

	for name, keys := range map[string]StationKeys{
		"no assertion key": {StationID: "st-1", SessionKey: "bb"},
		"no session key":   {StationID: "st-1", AssertionKey: "aa"},
		"one key twice":    {StationID: "st-1", AssertionKey: "aa", SessionKey: "aa"},
	} {
		_, err := InviteStation(st, keys)
		require.Error(t, err, name)
	}
	require.Empty(t, core.seen, "nothing was sent")
}

// BOTH ERROR ENVELOPE SHAPES. The broker writes {"error":{"message":...}}; a bare
// {"error":"..."} is what a hand-written handler reaches for first. Reading only one of the
// two is precisely how the operator-facing refusals were lost - the client understood the
// string form, the server has always sent the object form, and the test stub sent the
// string, so the tests agreed with nobody.
func TestBothErrorEnvelopeShapesReachTheOperator(t *testing.T) {
	for name, body := range map[string]string{
		"object": `{"error":{"message":"the reason it failed"}}`,
		"string": `{"error":"the reason it failed"}`,
	} {
		t.Run(name, func(t *testing.T) {
			core := newStationCore(t)
			st := registeredTower(t)
			core.replies["/tower/station/invite"] = func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(body))
			}
			_, err := InviteStation(st, StationKeys{
				StationID: "st-1", AssertionKey: "aa", SessionKey: "bb",
			})
			require.Error(t, err)
			require.Contains(t, err.Error(), "the reason it failed")
		})
	}

	// And a body that is not an envelope at all leaves the status as the only fact there is,
	// so the status must survive rather than being replaced by an HTML page.
	core := newStationCore(t)
	st := registeredTower(t)
	core.replies["/tower/station/invite"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>gateway</html>"))
	}
	_, err := InviteStation(st, StationKeys{StationID: "st-1", AssertionKey: "aa", SessionKey: "bb"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "502")
}

// Redeeming needs both halves of the invitation. Missing either is caught before a request
// that could only be refused.
func TestRedeemingNeedsTheInvitationAndItsSecret(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)

	_, err := AttachStation(st, Invitation{Secret: "s3cret"})
	require.Error(t, err)
	_, err = AttachStation(st, Invitation{InvitationID: "sinv-1"})
	require.Error(t, err)
	require.Empty(t, core.seen, "nothing was sent")
}

// The Station ID can come from either the invitation Core minted or the keys the operator
// supplied. When the operator did not name one, Core's allocation is what gets redeemed.
func TestRedeemingFallsBackToTheStationCoreAllocated(t *testing.T) {
	core := newStationCore(t)
	st := registeredTower(t)

	_, err := AttachStation(st, Invitation{
		InvitationID: "sinv-1", Secret: "s3cret", StationID: "st-allocated",
		Keys: StationKeys{AssertionKey: "aa", SessionKey: "bb"},
	})
	require.NoError(t, err)
	require.Equal(t, "st-allocated", core.bodies["/tower/station/attach"]["station_id"])
}

// An invitation with no Station ID of its own adopts the one Core allocated, so the operator
// can hand the whole record to the attach step without patching it up first.
func TestAnInviteWithoutAStationIDAdoptsCoresAllocation(t *testing.T) {
	newStationCore(t)
	st := registeredTower(t)

	got, err := InviteStation(st, StationKeys{AssertionKey: "aa", SessionKey: "bb"})
	require.NoError(t, err)
	require.Equal(t, "st-1", got.Keys.StationID)
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

// RequestEdgeCert submits a CSR and decodes the certificate Core returns.
func TestRequestingAnEdgeCertificateReturnsIt(t *testing.T) {
	core := newStationCore(t)
	core.replies["/tower/station/edge-cert"] = func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"station_id":  "st-1",
			"relay_name":  "st-1.relay.example",
			"certificate": base64.StdEncoding.EncodeToString([]byte("cert-der")),
			"ca":          base64.StdEncoding.EncodeToString([]byte("ca-der")),
			"not_after":   time.Now().Add(time.Hour).Unix(),
		})
	}
	_ = registeredTower(t)

	got, err := RequestEdgeCert("st-1", []byte("csr-der"))
	require.NoError(t, err)
	require.Equal(t, "st-1.relay.example", got.RelayName)
	require.Equal(t, []byte("cert-der"), got.Certificate)
	require.Equal(t, []byte("ca-der"), got.CA)
	require.Contains(t, core.seen, "/tower/station/edge-cert")
	// The request was signed - it is an account decision.
	require.NotEmpty(t, core.pubkeys["/tower/station/edge-cert"])
}

// A certificate Core returns as unreadable base64 is an error, not silent garbage.
func TestAnUnreadableIssuedCertificateIsRejected(t *testing.T) {
	core := newStationCore(t)
	core.replies["/tower/station/edge-cert"] = func(w http.ResponseWriter) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"station_id": "st-1", "relay_name": "n", "certificate": "!!!", "ca": "AAAA",
		})
	}
	_ = registeredTower(t)
	_, err := RequestEdgeCert("st-1", []byte("csr"))
	require.ErrorContains(t, err, "could not be read")
}

func TestAFailedEdgeCertRequestSurfaces(t *testing.T) {
	core := newStationCore(t)
	core.replies["/tower/station/edge-cert"] = func(w http.ResponseWriter) {
		http.Error(w, `{"error":{"message":"no such Station on this account"}}`, http.StatusNotFound)
	}
	_ = registeredTower(t)
	_, err := RequestEdgeCert("st-1", []byte("csr"))
	require.ErrorContains(t, err, "no such Station")
}
