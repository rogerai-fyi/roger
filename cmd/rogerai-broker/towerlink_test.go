package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/stationattach"
	"rogerai.fm/roger/v5/internal/store"
	"rogerai.fm/roger/v5/internal/towerinv"
	"rogerai.fm/roger/v5/internal/towerlink"
	"rogerai.fm/roger/v5/internal/towerobj"
)

// THE LINK, OVER REAL HTTP.
//
// Registration proved who a Tower is; these routes are where it starts telling us what it
// has. The pieces underneath each have their own suites, so what is tested here is the
// WIRING: that the caller is authenticated as a Tower rather than an operator, that the key
// which authenticated the request is the key the inventory is verified against, that a
// session is required before inventory, and that an accepted revision reaches the durable
// head.
//
// Enrollment is done straight through the registry rather than over its HTTP dance. That
// flow has its own end-to-end test; repeating it here would make a link failure look like an
// enrollment failure.

// linkTower is an enrolled Tower holding its identity key, as `roger-tower register` leaves
// it on disk.
type linkTower struct {
	id   string
	priv ed25519.PrivateKey
}

func enrolledTower(t *testing.T, b *broker, owner string) linkTower {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	tokenID, err := b.tower.registry.IssueToken(owner)
	require.NoError(t, err)
	sum := sha256.Sum256(pub)
	tw, err := b.tower.registry.Enroll(tokenID, hex.EncodeToString(sum[:]))
	require.NoError(t, err)
	return linkTower{id: tw.ID, priv: priv}
}

// call signs as the TOWER, which is what these routes authenticate.
func (lt linkTower) call(t *testing.T, srv *httptest.Server, path string, body []byte, out any) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(string(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(lt.priv, http.MethodPost, path, body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	require.NoError(t, err)
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, string(raw)
}

// mandatoryCaps are the two a joined session must carry. Both are integrity properties, not
// features: without the first a modified frame is indistinguishable from an honest one, and
// without the second the Tower could read the traffic it is carrying.
func mandatoryCaps() []string {
	return []string{towerlink.CapIntegrity, towerlink.CapInnerSession}
}

func jsonOf(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// attachStation records a Station so its offers can be verified, and returns its signing key.
func attachStation(t *testing.T, b *broker, stationID, towerID, owner string) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	assertion := hex.EncodeToString(pub)
	session := hex.EncodeToString([]byte(stationID + "-session-key"))

	auth, secret, err := stationattach.NewInvite(stationattach.Authorization{
		ID: "auth-" + stationID, Network: towerlink.PublicNetwork, StationID: stationID,
		Owner:        owner,
		Origin:       stationattach.Origin{Kind: stationattach.OriginJoined, TowerID: towerID},
		AssertionKey: assertion, SessionKey: session,
	}, time.Hour, time.Now())
	require.NoError(t, err)
	require.NoError(t, b.tower.stationStore.PutAuthorization(auth))
	_, err = b.tower.stations.Admit(stationattach.Proof{
		AuthID: "auth-" + stationID, Secret: secret,
		Network: towerlink.PublicNetwork, StationID: stationID,
		Owner:        owner,
		Origin:       stationattach.Origin{Kind: stationattach.OriginJoined, TowerID: towerID},
		AssertionKey: assertion, SessionKey: session,
	})
	require.NoError(t, err)
	return priv
}

// signedInventory builds a real Tower-signed revision carrying one real Station-signed leaf.
func signedInventory(t *testing.T, lt linkTower, stPriv ed25519.PrivateKey, stationID string, rev int64, prev string) []byte {
	t.Helper()
	now := time.Now()
	leaf := map[string]any{
		"network": towerlink.PublicNetwork, "tower_id": lt.id, "station_id": stationID,
		"offer_id": "offer-" + stationID, "model": "roger-1", "modality": "text",
		"price_in": "1000", "price_out": "2000", "earn_in": "800", "earn_out": "1600",
		"capacity": "4", "capabilities": []any{"chat"},
		"expires": towerobj.FormatInt(now.Add(30 * time.Minute).Unix()),
	}
	signedLeaf, err := towerobj.Sign(stPriv, towerlink.PublicNetwork, towerinv.TypeOffer,
		towerinv.Version, jsonOf(t, leaf), "station_sig")
	require.NoError(t, err)
	var leafObj map[string]any
	require.NoError(t, json.Unmarshal(signedLeaf, &leafObj))

	inv := map[string]any{
		"network": towerlink.PublicNetwork, "tower_id": lt.id,
		"revision": towerobj.FormatInt(rev), "prev_hash": prev,
		"lease_head": "lease-1", "lifecycle_head": "life-1",
		"issued":  towerobj.FormatInt(now.Unix()),
		"expires": towerobj.FormatInt(now.Add(30 * time.Minute).Unix()),
		"leaves":  []any{leafObj},
	}
	signed, err := towerobj.Sign(lt.priv, towerlink.PublicNetwork, towerinv.TypeInventory,
		towerinv.Version, jsonOf(t, inv), "sig")
	require.NoError(t, err)
	return signed
}

func TestATowerOpensALinkAndPushesInventory(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	stPriv := attachStation(t, b, "st-1", lt.id, ownerPubkeyOf(t, b, op.login))

	// A first connect has no head to resume from, so a full snapshot is demanded.
	var acc towerlink.Accepted
	code, raw := lt.call(t, srv, "/tower/session",
		jsonOf(t, towerlink.Hello{
			Network: towerlink.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
			Capabilities: mandatoryCaps(),
		}), &acc)
	require.Equal(t, http.StatusOK, code, raw)
	require.NotEmpty(t, acc.SessionID)
	require.True(t, acc.NeedFullInventory, "a Tower we have no head for must resend everything")

	// The snapshot lands, and the leaf becomes routable.
	var res struct {
		OK       bool                      `json:"ok"`
		Revision int64                     `json:"revision"`
		Hash     string                    `json:"hash"`
		Routable int                       `json:"routable"`
		Excluded []struct{ Reason string } `json:"excluded"`
	}
	code, raw = lt.call(t, srv, "/tower/inventory",
		signedInventory(t, lt, stPriv, "st-1", 1, "genesis"), &res)
	require.Equal(t, http.StatusOK, code, raw)
	require.True(t, res.OK)
	require.Equal(t, int64(1), res.Revision)
	require.Equal(t, 1, res.Routable, "the attached Station's offer must be routable: %+v", res.Excluded)

	// The head reached the DURABLE store, which is what lets another instance answer the
	// next reconnect without a snapshot.
	h, ok, err := b.tower.heads.Head(lt.id)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(1), h.Revision)
	require.Equal(t, res.Hash, h.Hash)

	// Heartbeat keeps it alive.
	code, raw = lt.call(t, srv, "/tower/session/heartbeat",
		jsonOf(t, towerlink.Frame{
			Network: towerlink.PublicNetwork, Version: 1, TowerID: lt.id, SessionID: acc.SessionID,
		}), nil)
	require.Equal(t, http.StatusOK, code, raw)

	// A reconnect quoting the recorded head RESUMES - the whole reason the head is stored.
	code, raw = lt.call(t, srv, "/tower/session",
		jsonOf(t, towerlink.Hello{
			Network: towerlink.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
			Capabilities: mandatoryCaps(),
			HeadRevision: h.Revision, HeadHash: h.Hash,
		}), &acc)
	require.Equal(t, http.StatusOK, code, raw)
	require.False(t, acc.NeedFullInventory,
		"a Tower quoting our exact head must not be made to resend ~5.4 MB")
}

// The link authenticates the TOWER. An operator's account key signs perfectly well and must
// still not drive a Tower's link.
func TestAnOperatorKeyCannotDriveTheLink(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	code, _ := op.call(t, srv, http.MethodPost, "/tower/session", towerlink.Hello{
		Network: towerlink.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(),
	}, nil)
	require.Equal(t, http.StatusForbidden, code,
		"the operator's key is not the Tower's key, however well it signs")
}

// Naming another Tower gets you nothing: the key hash recorded for THAT id must match the
// key that actually signed.
func TestATowerCannotSpeakForAnother(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	victim := enrolledTower(t, b, op.login)
	attacker := enrolledTower(t, b, op.login)

	// The attacker signs, but claims the victim's ID.
	code, _ := attacker.call(t, srv, "/tower/session",
		jsonOf(t, towerlink.Hello{
			Network: towerlink.PublicNetwork, Versions: []int{1}, TowerID: victim.id,
			Capabilities: mandatoryCaps(),
		}), nil)
	require.Equal(t, http.StatusForbidden, code)
}

// Inventory outside a session has no lifetime and nothing to expire against.
func TestInventoryNeedsASessionFirst(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	stPriv := attachStation(t, b, "st-1", lt.id, ownerPubkeyOf(t, b, op.login))

	code, raw := lt.call(t, srv, "/tower/inventory",
		signedInventory(t, lt, stPriv, "st-1", 1, "genesis"), nil)
	require.Equal(t, http.StatusConflict, code, raw)
	require.Contains(t, raw, "open a session")
}

// A delta the Tower cannot be placed on asks for a full snapshot with an explicit
// instruction, not a bare refusal - a Tower that cannot tell them apart retries the wrong one.
func TestAnUnplaceableDeltaAsksForAFullSnapshot(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	attachStation(t, b, "st-1", lt.id, ownerPubkeyOf(t, b, op.login))

	var acc towerlink.Accepted
	code, raw := lt.call(t, srv, "/tower/session",
		jsonOf(t, towerlink.Hello{
			Network: towerlink.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
			Capabilities: mandatoryCaps(),
		}), &acc)
	require.Equal(t, http.StatusOK, code, raw)

	delta := map[string]any{
		"network": towerlink.PublicNetwork, "tower_id": lt.id,
		"base_revision": "40", "revision": "41", "prev_hash": "nothing-we-accepted",
		"issued":  towerobj.FormatInt(time.Now().Unix()),
		"expires": towerobj.FormatInt(time.Now().Add(30 * time.Minute).Unix()),
		"ops":     []any{},
	}
	signed, err := towerobj.Sign(lt.priv, towerlink.PublicNetwork, towerinv.TypeDelta,
		towerinv.Version, jsonOf(t, delta), "sig")
	require.NoError(t, err)

	var out struct {
		NeedFull bool `json:"need_full_inventory"`
	}
	code, raw = lt.call(t, srv, "/tower/inventory/delta", signed, &out)
	require.Equal(t, http.StatusConflict, code, raw)
	require.True(t, out.NeedFull, "the Tower must be told to resend, not just refused")
}

// An orderly drain takes the fleet out of routing at once rather than aging it out.
func TestClosingTheSessionDrainsTheInventory(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	stPriv := attachStation(t, b, "st-1", lt.id, ownerPubkeyOf(t, b, op.login))

	var acc towerlink.Accepted
	_, _ = lt.call(t, srv, "/tower/session",
		jsonOf(t, towerlink.Hello{
			Network: towerlink.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
			Capabilities: mandatoryCaps(),
		}), &acc)
	code, raw := lt.call(t, srv, "/tower/inventory",
		signedInventory(t, lt, stPriv, "st-1", 1, "genesis"), nil)
	require.Equal(t, http.StatusOK, code, raw)
	require.Len(t, b.tower.inv.Routable(lt.id), 1)

	code, raw = lt.call(t, srv, "/tower/session/close",
		jsonOf(t, towerlink.Frame{
			Network: towerlink.PublicNetwork, Version: 1, TowerID: lt.id, SessionID: acc.SessionID,
		}), nil)
	require.Equal(t, http.StatusOK, code, raw)
	require.Empty(t, b.tower.inv.Routable(lt.id),
		"an operator who SAID they were going must not leave leaves taking work")
}

// ownerPubkeyOf resolves the account pubkey a Station is attached under.
func ownerPubkeyOf(t *testing.T, b *broker, login string) string {
	t.Helper()
	o, ok, err := b.db.OwnerByLogin(login)
	require.NoError(t, err)
	require.True(t, ok)
	return o.Pubkey
}

// --- guards -----------------------------------------------------------------

// Each link route answers the wrong method, a malformed body and an absent subsystem the
// same way every other route does. Cheap to get wrong, and a 500 where a 405 belongs is how
// an operator ends up reading a stack trace instead of an instruction.
func TestLinkRoutesGuardMethodAndBody(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	for _, path := range []string{
		"/tower/session", "/tower/session/heartbeat", "/tower/session/close",
		"/tower/inventory", "/tower/inventory/delta",
	} {
		t.Run("GET "+path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + path)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
		})
		t.Run("malformed body "+path, func(t *testing.T) {
			code, raw := lt.call(t, srv, path, []byte("{not json"), nil)
			require.Equal(t, http.StatusBadRequest, code, raw)
		})
	}
}

// A deployment without joined Towers answers 503 rather than panicking on a nil subsystem.
// Standalone Towers need nothing from us, so this is a legitimate configuration.
func TestLinkRoutesAreUnavailableWithoutTheSubsystem(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	b.tower = nil
	mux := http.NewServeMux()
	mux.HandleFunc("/tower/session", b.towerSessionOpen)
	mux.HandleFunc("/tower/session/heartbeat", b.towerHeartbeat)
	mux.HandleFunc("/tower/session/close", b.towerSessionClose)
	mux.HandleFunc("/tower/inventory", b.towerInventory(false))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	for _, path := range []string{
		"/tower/session", "/tower/session/heartbeat", "/tower/session/close", "/tower/inventory",
	} {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader("{}"))
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, path)
	}
}

// An unsigned request, or one naming no Tower, is refused before anything else happens.
func TestTheLinkRefusesAnUnauthenticatedCaller(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	_ = enrolledTower(t, b, op.login)

	resp, err := http.Post(srv.URL+"/tower/session", "application/json",
		strings.NewReader(`{"network":"roger-public","versions":[1],"tower_id":"tw-nope"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode,
		"an unsigned request is not a Tower, whatever it claims to be")
}

// A heartbeat for a session that was never opened is a conflict, not a success: a Tower that
// believes it is live when it is not would sit there sending frames into nothing.
func TestAHeartbeatWithoutASessionIsRefused(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	code, raw := lt.call(t, srv, "/tower/session/heartbeat",
		jsonOf(t, towerlink.Frame{
			Network: towerlink.PublicNetwork, Version: 1, TowerID: lt.id, SessionID: "sess-nope",
		}), nil)
	require.Equal(t, http.StatusConflict, code, raw)
}

// --- policy helpers ---------------------------------------------------------

func TestTowerPriceBandRefusesWhatItCannotPrice(t *testing.T) {
	_, _, ok := towerPriceBand("")
	require.False(t, ok, "a model with no name has no band")

	floor, ceiling, ok := towerPriceBand("roger-1")
	require.True(t, ok)
	require.Zero(t, floor, "free is a legitimate public price")
	require.Positive(t, ceiling, "and the ceiling is the same global one direct registration uses")
}

func TestTowerModalityIsChatOnlyInV1(t *testing.T) {
	require.True(t, towerModalityAllowed("text"))
	require.True(t, towerModalityAllowed("chat"))
	require.False(t, towerModalityAllowed("tts"),
		"a voice band diverts to a different path and has never been routable through a Tower")
	require.False(t, towerModalityAllowed(""))
}

func TestAnUnknownOwnerIsNotPresent(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	_, found, err := brokerOwners{b: b}.OwnerByPubkey("nobody")
	require.NoError(t, err)
	require.False(t, found, "an account Core has no record of cannot own a Station")
}

// A rejected revision is a 400 the Tower must FIX, distinct from the 409 that means resend.
// Conflating them would have a Tower retrying a snapshot that will never be accepted.
func TestARejectedInventoryIsAFourHundred(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	attachStation(t, b, "st-1", lt.id, ownerPubkeyOf(t, b, op.login))

	var acc towerlink.Accepted
	code, raw := lt.call(t, srv, "/tower/session",
		jsonOf(t, towerlink.Hello{
			Network: towerlink.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
			Capabilities: mandatoryCaps(),
		}), &acc)
	require.Equal(t, http.StatusOK, code, raw)

	// Signed by a key that is NOT this Tower's: the request authenticates (the caller holds
	// the Tower key) but the OBJECT does not verify against it.
	_, impostor, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	other := linkTower{id: lt.id, priv: impostor}
	stPriv := attachStation(t, b, "st-2", lt.id, ownerPubkeyOf(t, b, op.login))
	body := signedInventory(t, other, stPriv, "st-2", 1, "genesis")

	code, raw = lt.call(t, srv, "/tower/inventory", body, nil)
	require.Equal(t, http.StatusBadRequest, code, raw)
	require.NotContains(t, raw, "need_full_inventory",
		"a revision that will never verify must not be answered with 'send it again'")
}

// Negotiation failure is the Tower's to fix and says so - it is not a server fault.
func TestANegotiationFailureIsTheTowersToFix(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	code, raw := lt.call(t, srv, "/tower/session",
		jsonOf(t, towerlink.Hello{
			Network: towerlink.PublicNetwork, Versions: []int{99}, TowerID: lt.id,
			Capabilities: mandatoryCaps(),
		}), nil)
	require.Equal(t, http.StatusBadRequest, code, raw)
	require.Contains(t, raw, "negotiat")
}

// loadTowerSubsystem against a REAL database.
//
// This is the function that decides whether the broker boots at all: it is fatal on failure
// by design, because a deployment that CONFIGURED Towers and could not start them must not
// come up with the feature quietly missing. Every store it provisions - admission, custody,
// enrollment, Station attachment, inventory heads - has to apply its schema against a
// least-privilege role, and that is exactly the check a memory store cannot make. The
// `rogerai` schema permission bug that would have taken admission offline in production got
// through because nothing exercised this path with a real database.
func TestTheSubsystemLoadsAgainstARealDatabase(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL")
	}
	pg, err := store.NewPostgres(dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pg.Close() })

	b := testBrokerWithDB(pg)
	ts, err := loadTowerSubsystem(b, pg)
	require.NoError(t, err, "a configured deployment must be able to start joined admission")
	require.NotNil(t, ts, "a Postgres store means joined Towers ARE configured")

	// Every collaborator the link needs is wired, not just the admission half.
	require.NotNil(t, ts.registry)
	require.NotNil(t, ts.enroller)
	require.NotNil(t, ts.ca)
	require.NotNil(t, ts.link)
	require.NotNil(t, ts.inv)
	require.NotNil(t, ts.heads)
	require.NotNil(t, ts.stations)
	require.NotNil(t, ts.policy)

	// And the durable head store really is durable: write through the subsystem, read back
	// through a second one built over the same database.
	_, err = ts.heads.Accept("tw-durable", 7, "hash-7")
	require.NoError(t, err)

	again, err := loadTowerSubsystem(b, pg)
	require.NoError(t, err)
	h, ok, err := again.heads.Head("tw-durable")
	require.NoError(t, err)
	require.True(t, ok, "a second instance must see the head the first recorded")
	require.Equal(t, int64(7), h.Revision)
}

// No database is NOT a misconfiguration: standalone Towers need nothing from us, so the
// broker carries on with joined admission legitimately unavailable.
func TestNoDatabaseMeansJoinedTowersAreSimplyUnavailable(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	ts, err := loadTowerSubsystem(b, store.NewMem())
	require.NoError(t, err, "a deployment without Postgres must still boot")
	require.Nil(t, ts, "and joined admission is off rather than half-configured")
}

// --- Station invitations ----------------------------------------------------

// THE GAP THIS ROUTE CLOSED. Before it existed, nothing outside a test could ever create a
// Station attachment authorization - so the registry every leaf is verified against could
// only be empty, and towerinv refused every offer with "not consistent with any registered
// key". Everything built on top of it was correct and unreachable.
//
// This walks the whole thing on the real routes: the operator invites, the Station attaches
// with the one-use secret, the Tower relays its signed offer, and it becomes routable.
func TestAnOperatorInvitesAStationAndItsOfferBecomesRoutable(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	stPub, stPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sessPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var inv struct {
		InvitationID string `json:"invitation_id"`
		StationID    string `json:"station_id"`
		Secret       string `json:"secret"`
	}
	code, raw := op.call(t, srv, http.MethodPost, "/tower/station/invite", map[string]any{
		"tower_id":      lt.id,
		"assertion_key": hex.EncodeToString(stPub),
		"session_key":   hex.EncodeToString(sessPub),
	}, &inv)
	require.Equal(t, http.StatusOK, code, raw)
	require.NotEmpty(t, inv.InvitationID)
	require.NotEmpty(t, inv.StationID)
	require.NotEmpty(t, inv.Secret, "the plaintext is shown once, here")

	// The secret is NOT recoverable from what was stored.
	stored, ok, err := b.tower.stationStore.Authorization(inv.InvitationID)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEqual(t, inv.Secret, stored.SecretHash, "only a verifier is kept")
	require.Equal(t, ownerPubkeyOf(t, b, op.login), stored.Owner,
		"an attachment records the account PUBKEY - that is what towerpolicy resolves")

	// The Station redeems it.
	at, err := b.tower.stations.Admit(stationattach.Proof{
		AuthID: inv.InvitationID, Secret: inv.Secret,
		Network: towerlink.PublicNetwork, StationID: inv.StationID, Owner: ownerPubkeyOf(t, b, op.login),
		Origin:       stationattach.Origin{Kind: stationattach.OriginJoined, TowerID: lt.id},
		AssertionKey: hex.EncodeToString(stPub), SessionKey: hex.EncodeToString(sessPub),
	})
	require.NoError(t, err)
	require.Equal(t, stationattach.StateQuarantine, at.State)

	// And now a leaf signed by that Station verifies all the way through.
	var acc towerlink.Accepted
	code, raw = lt.call(t, srv, "/tower/session",
		jsonOf(t, towerlink.Hello{
			Network: towerlink.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
			Capabilities: mandatoryCaps(),
		}), &acc)
	require.Equal(t, http.StatusOK, code, raw)

	var res struct {
		Routable int                       `json:"routable"`
		Excluded []struct{ Reason string } `json:"excluded"`
	}
	code, raw = lt.call(t, srv, "/tower/inventory",
		signedInventory(t, lt, stPriv, inv.StationID, 1, "genesis"), &res)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, 1, res.Routable,
		"an invited, attached Station's offer must be routable: %+v", res.Excluded)
}

func TestAnInviteMustNameYourOwnTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	mine := signedInOperator(t, b, "octocat")
	theirs := signedInOperator(t, b, "hubot")
	theirTower := enrolledTower(t, b, theirs.login)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	other, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	code, raw := mine.call(t, srv, http.MethodPost, "/tower/station/invite", map[string]any{
		"tower_id":      theirTower.id,
		"assertion_key": hex.EncodeToString(pub),
		"session_key":   hex.EncodeToString(other),
	}, nil)
	require.Equal(t, http.StatusNotFound, code, raw)
	require.Contains(t, raw, "no such Tower on this account",
		"refused the same way as a Tower that does not exist, so this cannot enumerate")
}

func TestAnInviteValidatesItsKeysBeforeStoringAnything(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	good, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	for _, tc := range []struct{ name, assertion, session string }{
		{"an assertion key that is not hex", "zzzz", hex.EncodeToString(good)},
		{"a session key of the wrong length", hex.EncodeToString(good), hex.EncodeToString([]byte("short"))},
		{"one key for both purposes", hex.EncodeToString(good), hex.EncodeToString(good)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := op.call(t, srv, http.MethodPost, "/tower/station/invite", map[string]any{
				"tower_id": lt.id, "assertion_key": tc.assertion, "session_key": tc.session,
			}, nil)
			require.Equal(t, http.StatusBadRequest, code, raw)
		})
	}
}

func TestAnInviteRequiresASignedInAccount(t *testing.T) {
	_, srv := towerTestBroker(t)
	resp, err := http.Post(srv.URL+"/tower/station/invite", "application/json",
		strings.NewReader(`{"tower_id":"tw-1"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// A Station that is already attached cannot be re-invited: attaching a second identity to
// one Station ID is exactly what the origin rules forbid.
func TestAnAlreadyAttachedStationCannotBeReinvited(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	attachStation(t, b, "st-taken", lt.id, ownerPubkeyOf(t, b, op.login))

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	other, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	code, raw := op.call(t, srv, http.MethodPost, "/tower/station/invite", map[string]any{
		"tower_id": lt.id, "station_id": "st-taken",
		"assertion_key": hex.EncodeToString(pub), "session_key": hex.EncodeToString(other),
	}, nil)
	require.Equal(t, http.StatusConflict, code, raw)
	require.Contains(t, raw, "already attached")
}
