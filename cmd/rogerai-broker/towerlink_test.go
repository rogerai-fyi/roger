package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"path/filepath"

	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/store"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/attach"
	"rogerai.fm/roger/v5/internal/towercore/inv"
	"rogerai.fm/roger/v5/internal/towercore/link"
	towerpolicy "rogerai.fm/roger/v5/internal/towercore/policy"
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
	return []string{link.CapIntegrity, link.CapInnerSession}
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
	// A REAL 32-byte session key. targetFor refuses anything else - a Station whose recorded
	// session key is unusable is not dispatchable - so a placeholder here quietly excluded
	// every test Station from selection.
	sessionRaw := make([]byte, 32)
	copy(sessionRaw, stationID)
	session := hex.EncodeToString(sessionRaw)

	auth, secret, err := attach.NewInvite(attach.Authorization{
		ID: "auth-" + stationID, Network: link.PublicNetwork, StationID: stationID,
		Owner:        owner,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: assertion, SessionKey: session,
	}, time.Hour, time.Now())
	require.NoError(t, err)
	require.NoError(t, b.tower.stationStore.PutAuthorization(auth))
	_, err = b.tower.stations.Admit(attach.Proof{
		AuthID: "auth-" + stationID, Secret: secret,
		Network: link.PublicNetwork, StationID: stationID,
		Owner:        owner,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: assertion, SessionKey: session,
	})
	require.NoError(t, err)
	// Promoted, because this helper exists to produce a Station that WORKS. Attachment admits
	// into quarantine and quarantine withholds eligibility, so a test that wants a routable
	// offer has to pass that gate - see TestAnInvitationCanActuallyBeRedeemed for the gate
	// itself.
	_, err = b.tower.stations.Promote(stationID)
	require.NoError(t, err)
	return priv
}

// signedInventory builds a real Tower-signed revision carrying one real Station-signed leaf.
func signedInventory(t *testing.T, lt linkTower, stPriv ed25519.PrivateKey, stationID string, rev int64, prev string) []byte {
	t.Helper()
	now := time.Now()
	leaf := map[string]any{
		"network": link.PublicNetwork, "tower_id": lt.id, "station_id": stationID,
		"offer_id": "offer-" + stationID, "model": "roger-1", "modality": "text",
		"price_in": "1000", "price_out": "2000", "earn_in": "800", "earn_out": "1600",
		"capacity": "4", "capabilities": []any{"chat"},
		"expires": towerobj.FormatInt(now.Add(30 * time.Minute).Unix()),
	}
	signedLeaf, err := towerobj.Sign(stPriv, link.PublicNetwork, inv.TypeOffer,
		inv.Version, jsonOf(t, leaf), "station_sig")
	require.NoError(t, err)
	var leafObj map[string]any
	require.NoError(t, json.Unmarshal(signedLeaf, &leafObj))

	invObj := map[string]any{
		"network": link.PublicNetwork, "tower_id": lt.id,
		"revision": towerobj.FormatInt(rev), "prev_hash": prev,
		"lease_head": "lease-1", "lifecycle_head": "life-1",
		"issued":  towerobj.FormatInt(now.Unix()),
		"expires": towerobj.FormatInt(now.Add(30 * time.Minute).Unix()),
		"leaves":  []any{leafObj},
	}
	signed, err := towerobj.Sign(lt.priv, link.PublicNetwork, inv.TypeInventory,
		inv.Version, jsonOf(t, invObj), "sig")
	require.NoError(t, err)
	return signed
}

func TestATowerOpensALinkAndPushesInventory(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	stPriv := attachStation(t, b, "st-1", lt.id, ownerPubkeyOf(t, b, op.login))

	// A first connect has no head to resume from, so a full snapshot is demanded.
	var acc link.Accepted
	code, raw := lt.call(t, srv, "/tower/session",
		jsonOf(t, link.Hello{
			Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
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
		jsonOf(t, link.Frame{
			Network: link.PublicNetwork, Version: 1, TowerID: lt.id, SessionID: acc.SessionID,
		}), nil)
	require.Equal(t, http.StatusOK, code, raw)

	// A reconnect quoting the recorded head RESUMES - the whole reason the head is stored.
	code, raw = lt.call(t, srv, "/tower/session",
		jsonOf(t, link.Hello{
			Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
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

	code, _ := op.call(t, srv, http.MethodPost, "/tower/session", link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
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
		jsonOf(t, link.Hello{
			Network: link.PublicNetwork, Versions: []int{1}, TowerID: victim.id,
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

	var acc link.Accepted
	code, raw := lt.call(t, srv, "/tower/session",
		jsonOf(t, link.Hello{
			Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
			Capabilities: mandatoryCaps(),
		}), &acc)
	require.Equal(t, http.StatusOK, code, raw)

	delta := map[string]any{
		"network": link.PublicNetwork, "tower_id": lt.id,
		"base_revision": "40", "revision": "41", "prev_hash": "nothing-we-accepted",
		"issued":  towerobj.FormatInt(time.Now().Unix()),
		"expires": towerobj.FormatInt(time.Now().Add(30 * time.Minute).Unix()),
		"ops":     []any{},
	}
	signed, err := towerobj.Sign(lt.priv, link.PublicNetwork, inv.TypeDelta,
		inv.Version, jsonOf(t, delta), "sig")
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

	var acc link.Accepted
	_, _ = lt.call(t, srv, "/tower/session",
		jsonOf(t, link.Hello{
			Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
			Capabilities: mandatoryCaps(),
		}), &acc)
	code, raw := lt.call(t, srv, "/tower/inventory",
		signedInventory(t, lt, stPriv, "st-1", 1, "genesis"), nil)
	require.Equal(t, http.StatusOK, code, raw)
	require.Len(t, b.tower.inv.Routable(lt.id), 1)

	code, raw = lt.call(t, srv, "/tower/session/close",
		jsonOf(t, link.Frame{
			Network: link.PublicNetwork, Version: 1, TowerID: lt.id, SessionID: acc.SessionID,
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
		jsonOf(t, link.Frame{
			Network: link.PublicNetwork, Version: 1, TowerID: lt.id, SessionID: "sess-nope",
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

	var acc link.Accepted
	code, raw := lt.call(t, srv, "/tower/session",
		jsonOf(t, link.Hello{
			Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
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
		jsonOf(t, link.Hello{
			Network: link.PublicNetwork, Versions: []int{99}, TowerID: lt.id,
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
	// A PRIVATE database, for the same reason internal/store keeps one: packages run in
	// parallel against one ROGERAI_TEST_DATABASE_URL, and this test loads the CA from
	// rogerai.tower_ca_root - a table another package's custody test seeds with the
	// placeholder "key-pem". Reading somebody else's fixture made this fail with "the Tower
	// CA key is not a usable PEM private key", which is a true statement about a root this
	// deployment never wrote.
	pg, err := store.NewPostgres(brokerPrivateDSN(t, dsn))
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

// brokerPrivateDSN gives this test its own database on the same server.
func brokerPrivateDSN(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || u.Path == "" || u.Path == "/" {
		return dsn
	}
	name := strings.TrimPrefix(u.Path, "/") + "_towerlink"
	admin, aerr := sql.Open("pgx", dsn)
	require.NoError(t, aerr)
	defer admin.Close()
	if _, cerr := admin.Exec(`CREATE DATABASE "` + name + `"`); cerr != nil &&
		!strings.Contains(cerr.Error(), "already exists") {
		t.Fatalf("private broker db: %v", cerr)
	}
	u.Path = "/" + name
	own, oerr := sql.Open("pgx", u.String())
	require.NoError(t, oerr)
	defer own.Close()
	_, _ = own.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	return u.String()
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

func res2Hash(t *testing.T, b *broker, towerID string) string {
	t.Helper()
	_, hash, ok := b.tower.inv.Head(towerID)
	require.True(t, ok)
	return hash
}

func TestAnOperatorCanRevokeTheirOwnStation(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	ownerPub := ownerPubkeyOf(t, b, op.login)
	attachStation(t, b, "st-mine", lt.id, ownerPub)

	code, raw := op.call(t, srv, http.MethodPost, "/tower/station/revoke",
		map[string]any{"station_id": "st-mine"}, nil)
	require.Equal(t, http.StatusOK, code, raw)

	at, found, err := b.tower.stations.Station("st-mine")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, attach.StateRevoked, at.State)

	// Somebody else's Station answers exactly like one that does not exist.
	stranger := signedInOperator(t, b, "hubot")
	code, raw = stranger.call(t, srv, http.MethodPost, "/tower/station/revoke",
		map[string]any{"station_id": "st-mine"}, nil)
	require.Equal(t, http.StatusNotFound, code, raw)
}

// A fresh instance holding the durable head but NOT the inventory must still ask for a full
// snapshot. Reporting resume on the head alone promised something it could not honour: the
// body is never stored, so the first delta touching an unknown leaf 409s anyway.
func TestAFreshInstanceWithTheHeadStillAsksForEverything(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	// The head store knows this Tower; this broker's inventory does not.
	_, err := b.tower.heads.Accept(lt.id, 7, "hash-7")
	require.NoError(t, err)

	var acc link.Accepted
	code, raw := lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(), HeadRevision: 7, HeadHash: "hash-7",
	}), &acc)
	require.Equal(t, http.StatusOK, code, raw)
	require.True(t, acc.NeedFullInventory,
		"the head agrees, but this instance holds no leaves - resuming would promise a delta "+
			"it must then refuse")
}

func TestResumeRequiresThisInstanceToBeAtTheSamePosition(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	stPriv := attachStation(t, b, "st-1", lt.id, ownerPubkeyOf(t, b, op.login))

	var acc link.Accepted
	_, _ = lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(),
	}), &acc)
	code, raw := lt.call(t, srv, "/tower/inventory",
		signedInventory(t, lt, stPriv, "st-1", 1, "genesis"), nil)
	require.Equal(t, http.StatusOK, code, raw)

	// The Tower claims a revision AHEAD of what this instance holds, and the durable head
	// would agree if it had been recorded elsewhere. Presence alone would have said Resume.
	_, err := b.tower.heads.Accept(lt.id, 9, "hash-9")
	require.NoError(t, err)
	code, raw = lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(), HeadRevision: 9, HeadHash: "hash-9",
	}), &acc)
	require.Equal(t, http.StatusOK, code, raw)
	require.True(t, acc.NeedFullInventory,
		"this instance is at revision 1; resuming at 9 would promise a delta it must refuse")
}

func TestRevokingOneStationLeavesTheTowersChainAlone(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	ownerPub := ownerPubkeyOf(t, b, op.login)
	stPriv := attachStation(t, b, "st-1", lt.id, ownerPub)
	attachStation(t, b, "st-2", lt.id, ownerPub)

	var acc link.Accepted
	_, _ = lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(),
	}), &acc)
	code, raw := lt.call(t, srv, "/tower/inventory",
		signedInventory(t, lt, stPriv, "st-1", 1, "genesis"), nil)
	require.Equal(t, http.StatusOK, code, raw)
	rev, _, ok := b.tower.inv.Head(lt.id)
	require.True(t, ok)

	code, raw = op.call(t, srv, http.MethodPost, "/tower/station/revoke",
		map[string]any{"station_id": "st-2"}, nil)
	require.Equal(t, http.StatusOK, code, raw)

	gotRev, _, stillThere := b.tower.inv.Head(lt.id)
	require.True(t, stillThere,
		"retiring one Station must not drop the whole Tower's chain and resync every sibling")
	require.Equal(t, rev, gotRev)
}

// A DRAINING TOWER MUST KEEP ITS LINK. Draining is precisely when a Tower needs to
// heartbeat and then close: refusing it would leave the fleet to age out over the freshness
// window, which is the outcome an orderly drain exists to avoid.
func TestADrainingTowerCanStillDrain(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	var acc link.Accepted
	code, raw := lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(),
	}), &acc)
	require.Equal(t, http.StatusOK, code, raw)

	// quarantine -> active -> draining: the lifecycle will not let a Tower drain from
	// quarantine, which is itself correct.
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateDraining))

	code, raw = lt.call(t, srv, "/tower/session/heartbeat", jsonOf(t, link.Frame{
		Network: link.PublicNetwork, Version: 1, TowerID: lt.id, SessionID: acc.SessionID,
	}), nil)
	require.Equal(t, http.StatusOK, code, raw, "a draining Tower must still heartbeat")

	code, raw = lt.call(t, srv, "/tower/session/close", jsonOf(t, link.Frame{
		Network: link.PublicNetwork, Version: 1, TowerID: lt.id, SessionID: acc.SessionID,
	}), nil)
	require.Equal(t, http.StatusOK, code, raw, "and must be able to close cleanly")
}

func TestExpiredInvitationsAreReapedAndConsumedOnesAreKept(t *testing.T) {
	b, _ := towerTestBroker(t)
	now := time.Now()

	expired, _, err := attach.NewInvite(attach.Authorization{
		ID: "inv-old", Network: link.PublicNetwork, StationID: "st-old", Owner: "o",
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: "tw-1"},
		AssertionKey: "A", SessionKey: "K",
	}, time.Minute, now.Add(-2*time.Hour))
	require.NoError(t, err)
	require.NoError(t, b.tower.stationStore.PutAuthorization(expired))

	spent := expired
	spent.ID, spent.Consumed, spent.ConsumedBy = "inv-spent", true, "st-x"
	require.NoError(t, b.tower.stationStore.PutAuthorization(spent))

	b.towerInviteSweepOnce(now)

	_, ok, err := b.tower.stationStore.Authorization("inv-old")
	require.NoError(t, err)
	require.False(t, ok, "an expired unredeemed invitation is reaped")
	_, ok, err = b.tower.stationStore.Authorization("inv-spent")
	require.NoError(t, err)
	require.True(t, ok, "a consumed one stays - it is what answers a lost-response retry")
}

// Banning an operator must reach the Tower policy's CACHED ban set at once.
//
// The previous version of this test primed the cache, banned, and then asserted
// b.isOwnerBanned - a plain map read that passed identically with the Invalidate call
// deleted. An audit called it vacuous and it was: the test was named for a fix it never
// touched. This one sets the refresh window to an hour, so ONLY an invalidation can make the
// second read see the ban.
func TestBanningAnOperatorInvalidatesTheTowerPolicyCache(t *testing.T) {
	b, _ := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	ownerPub := ownerPubkeyOf(t, b, op.login)
	attachStation(t, b, "st-1", lt.id, ownerPub)

	// A long window: without an invalidation the ban could not be visible for an hour.
	b.tower.policy = towerpolicy.New(b.tower.stations, b.db, brokerOwners{b: b}, towerpolicy.Config{
		ModelAllowed:    towerModelAllowed,
		ModalityAllowed: towerModalityAllowed,
		PriceBand:       towerPriceBand,
		BanRefresh:      time.Hour,
	})

	require.False(t, b.tower.policy.Station("st-1").Banned, "not banned yet, and now cached")

	b.banOwner(ownerPub, "abuse", "{}")

	require.True(t, b.tower.policy.Station("st-1").Banned,
		"a ban must reach the cached set at once - with the Invalidate call removed this "+
			"read would still be served from an hour-long cache")
}

// The promote route's remaining answers: a Station that is not in quarantine, and one that
// does not exist. An administrator needs to know WHICH, so these are 200 with a reason
// rather than an opaque 404.
func TestPromoteReportsWhyItDidNothing(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	attachStation(t, b, "st-live", lt.id, ownerPubkeyOf(t, b, op.login)) // helper promotes

	adminPost := func(station string) (int, string) {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/tower/station/promote",
			strings.NewReader(`{"station_id":"`+station+`"}`))
		require.NoError(t, err)
		req.Header.Set("X-Roger-Admin", "admin-secret")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return resp.StatusCode, string(raw)
	}

	code, raw := adminPost("st-live")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, raw, `"promoted":false`, "already active is not a promotion")
	require.Contains(t, raw, "active")

	code, raw = adminPost("st-nobody")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, raw, `"promoted":false`)
	require.Contains(t, raw, "unknown")

	// And a malformed body is a 400, not a 500.
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/tower/station/promote",
		strings.NewReader("{nope"))
	require.NoError(t, err)
	req.Header.Set("X-Roger-Admin", "admin-secret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// The sweeper is a no-op when joined Towers are not configured - a deployment without them
// must not panic on a nil subsystem every ten minutes.
func TestTheInviteSweepIsSafeWithoutTheSubsystem(t *testing.T) {
	b := testBrokerWithDB(store.NewMem())
	b.tower = nil
	require.NotPanics(t, func() { b.towerInviteSweepOnce(time.Now()) })
}

// The admin routes answer a CORS preflight like their siblings, since requireAdmin accepts a
// browser session and the console can reach them.
func TestAdminTowerRoutesAnswerAPreflight(t *testing.T) {
	_, srv := towerTestBroker(t)
	for _, path := range []string{"/tower/station/promote", "/tower/lease/expire", "/tower/lifecycle"} {
		req, err := http.NewRequest(http.MethodOptions, srv.URL+path, nil)
		require.NoError(t, err)
		req.Header.Set("Origin", "https://rogerai.fm")
		req.Header.Set("Access-Control-Request-Method", "POST")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, "https://rogerai.fm", resp.Header.Get("Access-Control-Allow-Origin"), path)
		require.Equal(t, "true", resp.Header.Get("Access-Control-Allow-Credentials"), path)
	}
}

// Ending a lease takes a Tower off the link now, which is the whole reason ExpireLease is in
// the production binary rather than a test file.
func TestAnAdminCanEndATowersLease(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	code, raw := lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(),
	}), nil)
	require.Equal(t, http.StatusOK, code, raw)

	// An operator cannot do this to their own Tower, and certainly not to anyone else's.
	code, raw = op.call(t, srv, http.MethodPost, "/tower/lease/expire",
		map[string]any{"tower_id": lt.id}, nil)
	require.Equal(t, http.StatusForbidden, code, raw)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/tower/lease/expire",
		strings.NewReader(`{"tower_id":"`+lt.id+`"}`))
	require.NoError(t, err)
	req.Header.Set("X-Roger-Admin", "admin-secret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// And the link is gone: towerMayHoldLink keys off the lease.
	code, raw = lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(),
	}), nil)
	require.Equal(t, http.StatusForbidden, code, raw)
}

// inv.Routable finally has a production reader, and this is it.
//
// The audit's closing observation across four rounds was that inventory was verified,
// chained, persisted and reconciled - and then nothing read it. An operator could not tell a
// Station that was carrying nothing from a Station Core had refused, because the exclusion
// reasons were computed at admission and then discarded.
func TestTowerStatusShowsWhatCoreActuallyBelieves(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	ownerPub := ownerPubkeyOf(t, b, op.login)
	stPriv := attachStation(t, b, "st-live", lt.id, ownerPub) // helper promotes

	var acc link.Accepted
	code, raw := lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(),
	}), &acc)
	require.Equal(t, http.StatusOK, code, raw)
	code, raw = lt.call(t, srv, "/tower/inventory",
		signedInventory(t, lt, stPriv, "st-live", 1, "genesis"), nil)
	require.Equal(t, http.StatusOK, code, raw)

	var status struct {
		Towers []struct {
			TowerID     string `json:"tower_id"`
			LinkLive    bool   `json:"link_live"`
			Revision    int64  `json:"inventory_revision"`
			Carries     bool   `json:"carries_traffic"`
			Compensated bool   `json:"compensated"`
			Note        string `json:"note"`
			Routable    []struct {
				StationID string `json:"station_id"`
				Model     string `json:"model"`
			} `json:"routable"`
		} `json:"towers"`
	}
	code, raw = op.call(t, srv, http.MethodGet, "/tower/status", nil, &status)
	require.Equal(t, http.StatusOK, code, raw)
	require.Len(t, status.Towers, 1)
	got := status.Towers[0]
	require.Equal(t, lt.id, got.TowerID)
	require.True(t, got.LinkLive, "the operator can see the link is up")
	require.Equal(t, int64(1), got.Revision, "and which revision Core accepted")
	require.Len(t, got.Routable, 1, "and which of their Stations is eligible")
	require.Equal(t, "st-live", got.Routable[0].StationID)
	require.Equal(t, "roger-1", got.Routable[0].Model)

	// DISPATCH SHIPS, but is NOT YET compensated for the operator: the overflow path real
	// The status must be honest: the data plane is the sealed hub, settlement rails exist end
	// to end, and the payout rail that moves money OUT is not live - claiming compensation an
	// operator cannot actually collect would make them distrust a $0 relay line.
	require.True(t, got.Carries, "the sealed hub carries work now")
	require.False(t, got.Compensated, "earning is not live for the operator yet")
	require.Contains(t, got.Note, "sealed hub")
	require.Contains(t, got.Note, "Earning is not live yet",
		"the status is honest that the payout rail is not live for the operator")
	require.Contains(t, got.Note, "payout rail")
}

// A quarantined Station is attached and verified but NOT routable, and the operator can see
// exactly that rather than guessing why their fleet is idle.
func TestTowerStatusDistinguishesQuarantineFromRoutable(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	ownerPub := ownerPubkeyOf(t, b, op.login)

	stPub, stPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sessPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	auth, secret, err := attach.NewInvite(attach.Authorization{
		ID: "inv-q", Network: link.PublicNetwork, StationID: "st-quarantined", Owner: ownerPub,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: lt.id},
		AssertionKey: hex.EncodeToString(stPub), SessionKey: hex.EncodeToString(sessPub),
	}, time.Hour, time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.NoError(t, b.tower.stationStore.PutAuthorization(auth))
	_, err = b.tower.stations.Admit(attach.Proof{
		AuthID: "inv-q", Secret: secret, Network: link.PublicNetwork,
		StationID: "st-quarantined", Owner: ownerPub,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: lt.id},
		AssertionKey: hex.EncodeToString(stPub), SessionKey: hex.EncodeToString(sessPub),
	})
	require.NoError(t, err) // left in quarantine on purpose

	var acc link.Accepted
	_, _ = lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(),
	}), &acc)
	code, raw := lt.call(t, srv, "/tower/inventory",
		signedInventory(t, lt, stPriv, "st-quarantined", 1, "genesis"), nil)
	require.Equal(t, http.StatusOK, code, raw)

	var status struct {
		Towers []struct {
			Routable []struct {
				StationID string `json:"station_id"`
			} `json:"routable"`
		} `json:"towers"`
	}
	code, raw = op.call(t, srv, http.MethodGet, "/tower/status", nil, &status)
	require.Equal(t, http.StatusOK, code, raw)
	require.Len(t, status.Towers, 1)
	require.Empty(t, status.Towers[0].Routable,
		"a quarantined Station is attached and verified, and still not eligible")
}

// --- Tower lifecycle: the quarantine gate ----------------------------------
//
// EVERY ENROLLED TOWER WAS STUCK. The state machine has the quarantine->active edge and
// Registry.Transition enforces the whole approved table, but nothing in the broker ever
// called it: there was no route, no admin control, nothing. A Tower could enroll, hold the
// link, push inventory - and never become eligible for a single request, forever. Nothing
// failed and no test noticed, because every test that cared about eligibility set the state
// directly.

// The gate opens: an administrator promotes a quarantined Tower and it becomes eligible.
func TestAnAdminCanPromoteATowerOutOfQuarantine(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	tw, ok := b.tower.registry.Get(lt.id)
	require.True(t, ok)
	require.Equal(t, admit.StateQuarantine, tw.State, "a new Tower starts in quarantine")
	require.False(t, b.tower.registry.MayTakeWork(lt.id), "and may not take work")

	code, raw := adminTowerPost(t, srv, "/tower/lifecycle",
		`{"tower_id":"`+lt.id+`","state":"active"}`)
	require.Equal(t, http.StatusOK, code, raw)
	require.Contains(t, raw, `"ok":true`)
	require.Contains(t, raw, `"state":"active"`)

	require.True(t, b.tower.registry.MayTakeWork(lt.id),
		"a promoted Tower is eligible for ordinary work")
}

// Promotion is ADMIN-gated, not operator-gated, for the same reason Station promotion is:
// admission and eligibility are separate decisions, and the person asking to be trusted
// cannot also be the one granting it. An operator promoting their own Tower would collapse
// the two and quarantine would mean nothing.
func TestAnOperatorCannotPromoteTheirOwnTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	code, _ := op.call(t, srv, http.MethodPost, "/tower/lifecycle",
		map[string]string{"tower_id": lt.id, "state": "active"}, nil)
	require.NotEqual(t, http.StatusOK, code, "an operator may not promote their own Tower")
	require.False(t, b.tower.registry.MayTakeWork(lt.id))
}

// The route applies the APPROVED TABLE rather than a state string. Suspended does not go
// straight back to active - clearing a suspension returns a Tower to quarantine, where it
// must pass fresh probes - and an illegal jump is refused with the reason.
func TestTheLifecycleRouteRefusesAnIllegalTransition(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	code, raw := adminTowerPost(t, srv, "/tower/lifecycle",
		`{"tower_id":"`+lt.id+`","state":"suspended"}`)
	require.Equal(t, http.StatusOK, code, raw)

	// suspended -> active is NOT an edge. It must go back through quarantine.
	code, raw = adminTowerPost(t, srv, "/tower/lifecycle",
		`{"tower_id":"`+lt.id+`","state":"active"}`)
	require.Equal(t, http.StatusConflict, code, raw)
	require.Contains(t, raw, "cannot become")
	require.False(t, b.tower.registry.MayTakeWork(lt.id), "the refusal took effect")

	// Back through quarantine, then active, is allowed.
	code, raw = adminTowerPost(t, srv, "/tower/lifecycle",
		`{"tower_id":"`+lt.id+`","state":"quarantine"}`)
	require.Equal(t, http.StatusOK, code, raw)
	code, raw = adminTowerPost(t, srv, "/tower/lifecycle",
		`{"tower_id":"`+lt.id+`","state":"active"}`)
	require.Equal(t, http.StatusOK, code, raw)
	require.True(t, b.tower.registry.MayTakeWork(lt.id))
}

// Suspending an ACTIVE Tower takes it off public traffic at once. This is the control an
// operator on call reaches for, so it must not need a deploy.
func TestSuspendingAnActiveTowerStopsItTakingWork(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"active"}`)
	require.True(t, b.tower.registry.MayTakeWork(lt.id))

	code, raw := adminTowerPost(t, srv, "/tower/lifecycle",
		`{"tower_id":"`+lt.id+`","state":"suspended"}`)
	require.Equal(t, http.StatusOK, code, raw)
	require.False(t, b.tower.registry.MayTakeWork(lt.id), "a suspended Tower takes nothing")
}

// The remaining answers, each said plainly: an unknown Tower, a state that is not a state,
// and a malformed body. None of them is a 500.
func TestTheLifecycleRouteAnswersItsBadInputsPlainly(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"

	code, raw := adminTowerPost(t, srv, "/tower/lifecycle",
		`{"tower_id":"tw-nobody","state":"active"}`)
	require.Equal(t, http.StatusNotFound, code, raw)

	code, raw = adminTowerPost(t, srv, "/tower/lifecycle",
		`{"tower_id":"tw-1","state":"marvellous"}`)
	require.Equal(t, http.StatusBadRequest, code, raw)
	require.Contains(t, raw, "not a Tower state")

	code, _ = adminTowerPost(t, srv, "/tower/lifecycle", "{nope")
	require.Equal(t, http.StatusBadRequest, code)

	code, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"state":"active"}`)
	require.Equal(t, http.StatusBadRequest, code, "a lifecycle change needs a Tower")
}

func adminTowerPost(t *testing.T, srv *httptest.Server, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("X-Roger-Admin", "admin-secret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, string(raw)
}

// The admin routes' shared preamble: the wrong method is refused, and a deployment with no
// joined-Tower subsystem answers "unavailable" rather than dereferencing a nil.
func TestTheAdminTowerRoutesRefuseTheWrongMethodAndAMissingSubsystem(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"

	for _, path := range []string{"/tower/lifecycle", "/tower/lease/expire"} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		require.NoError(t, err)
		req.Header.Set("X-Roger-Admin", "admin-secret")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode, path)
	}

	// A broker built without joined Towers must say so. Every one of these routes reaches
	// through b.tower, and a nil there is a deployment choice, not a fault.
	b.tower = nil
	code, raw := adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"tw-1","state":"active"}`)
	require.Equal(t, http.StatusServiceUnavailable, code, raw)
	code, _ = adminTowerPost(t, srv, "/tower/lease/expire", `{"tower_id":"tw-1"}`)
	require.Equal(t, http.StatusServiceUnavailable, code)
}

// Ending a lease answers its bad inputs the same way: a malformed body is a 400 and an
// unknown Tower a 404, neither of them a 500.
func TestEndingALeaseAnswersItsBadInputsPlainly(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"

	code, _ := adminTowerPost(t, srv, "/tower/lease/expire", "{nope")
	require.Equal(t, http.StatusBadRequest, code)

	code, raw := adminTowerPost(t, srv, "/tower/lease/expire", `{"tower_id":"tw-nobody"}`)
	require.Equal(t, http.StatusNotFound, code, raw)
}

// UNSIGNED CALLS REACH NOTHING. Every link route authenticates the Tower itself, and the
// refusal must come before the route does any work - a session it would otherwise close, an
// inventory it would otherwise accept. Asserting it per-route rather than once is the point:
// these checks are copied per handler, so they are exactly the kind that goes missing from
// one of them.
func TestEveryLinkRouteRefusesAnUnsignedCaller(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	for _, path := range []string{
		"/tower/session", "/tower/session/heartbeat", "/tower/session/close",
		"/tower/inventory", "/tower/inventory/delta",
	} {
		resp, err := http.Post(srv.URL+path, "application/json",
			strings.NewReader(`{"tower_id":"`+lt.id+`","session_id":"s-1","network":"rogerai-public","version":1}`))
		require.NoError(t, err)
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "%s: %s", path, raw)
		require.Contains(t, string(raw), "signed request", path)
	}
}

// The invitation sweeper's LOOP, as opposed to the one-shot it calls. A stop signal has to
// end it: a goroutine that outlives its broker keeps a database handle alive for the life of
// the process.
func TestTheInviteSweepLoopStopsWhenTold(t *testing.T) {
	b, _ := towerTestBroker(t)
	stop := make(chan struct{})
	close(stop)
	done := make(chan struct{})
	go func() { b.towerInviteSweep(stop); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep loop ignored its stop signal")
	}
}

// --- the Station registry being DOWN ---------------------------------------
//
// Every Station route reads or writes the attachment registry, and each one has a branch for
// "the registry did not answer". Those branches are the fail-closed contract: an unreadable
// registry must never be treated as an empty one, because an empty registry says "this
// Station is not attached" and that is a different, wrong, and confidently-stated answer.
//
// They were all untested, which is the usual state of an error path nobody can trigger by
// hand. This store breaks on demand so they can be.

// brokenAttachStore fails every call that reaches storage.
type brokenAttachStore struct{ attach.Store }

var errRegistryDown = errors.New("the Station registry is unreachable")

func (brokenAttachStore) PutAuthorizationCapped(attach.Authorization, int) (bool, error) {
	return false, errRegistryDown
}
func (brokenAttachStore) Authorization(string) (attach.Authorization, bool, error) {
	return attach.Authorization{}, false, errRegistryDown
}
func (brokenAttachStore) ByStation(string) (attach.Attachment, bool, error) {
	return attach.Attachment{}, false, errRegistryDown
}
func (brokenAttachStore) SetState(string, string) (bool, error) { return false, errRegistryDown }
func (brokenAttachStore) CountLiveAttachments(string) (int, error) {
	return 0, errRegistryDown
}

func wrapLeaves(t *testing.T, lt linkTower, rev int64, prev string, leaves []json.RawMessage) []byte {
	t.Helper()
	now := time.Now()
	objs := make([]any, 0, len(leaves))
	for _, l := range leaves {
		var o any
		require.NoError(t, json.Unmarshal(l, &o))
		objs = append(objs, o)
	}
	invObj := map[string]any{
		"network": link.PublicNetwork, "tower_id": lt.id,
		"revision": towerobj.FormatInt(rev), "prev_hash": prev,
		"lease_head": "lease-1", "lifecycle_head": "life-1",
		"issued":  towerobj.FormatInt(now.Unix()),
		"expires": towerobj.FormatInt(now.Add(30 * time.Minute).Unix()),
		"leaves":  objs,
	}
	signed, err := towerobj.Sign(lt.priv, link.PublicNetwork, inv.TypeInventory,
		inv.Version, jsonOf(t, invObj), "sig")
	require.NoError(t, err)
	return signed
}

// attachReal attaches a REAL Station (its own keys, its own directory) under this account,
// and promotes it out of quarantine.
func attachReal(t *testing.T, b *broker, authID, towerID, owner string) *station.Station {
	t.Helper()
	stn, err := station.Init(filepath.Join(t.TempDir(), "st"))
	require.NoError(t, err)
	auth, secret, err := attach.NewInvite(attach.Authorization{
		ID: authID, Network: link.PublicNetwork, StationID: stn.StationID, Owner: owner,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: stn.Assertion, SessionKey: stn.Session,
	}, time.Hour, time.Now())
	require.NoError(t, err)
	require.NoError(t, b.tower.stationStore.PutAuthorization(auth))
	_, err = b.tower.stations.Admit(attach.Proof{
		AuthID: authID, Secret: secret, Network: link.PublicNetwork,
		StationID: stn.StationID, Owner: owner,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: towerID},
		AssertionKey: stn.Assertion, SessionKey: stn.Session,
	})
	require.NoError(t, err)
	// A Station is not eligible on arrival; these tests are about the signature path, and
	// the gate has its own.
	promoted, err := b.tower.stations.Promote(stn.StationID)
	require.NoError(t, err)
	require.True(t, promoted)
	return stn
}

func openLink(t *testing.T, srv *httptest.Server, lt linkTower) {
	t.Helper()
	code, raw := lt.call(t, srv, "/tower/session",
		jsonOf(t, link.Hello{
			Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
			Capabilities: mandatoryCaps(),
		}), nil)
	require.Equal(t, http.StatusOK, code, raw)
}

func TestAnOperatorCanDrainTheirOwnTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"active"}`)
	require.True(t, b.tower.registry.MayTakeWork(lt.id))

	code, raw := op.call(t, srv, http.MethodPost, "/tower/self/lifecycle",
		map[string]string{"tower_id": lt.id, "state": "draining"}, nil)
	require.Equal(t, http.StatusOK, code, raw)

	require.False(t, b.tower.registry.MayTakeWork(lt.id), "a draining Tower takes no new work")
	tw, ok := b.tower.registry.Get(lt.id)
	require.True(t, ok)
	require.True(t, towerMayHoldLink(tw), "and it keeps the link so in-flight work can finish")
}

// And can put it back. Draining is otherwise a one-way trip that needs an administrator to
// undo, which would make it useless for the thing it is for - swapping a disk.
func TestAnOperatorCanResumeTheirOwnDrainedTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	_, _ = adminTowerPost(t, srv, "/tower/lifecycle", `{"tower_id":"`+lt.id+`","state":"active"}`)

	code, _ := op.call(t, srv, http.MethodPost, "/tower/self/lifecycle",
		map[string]string{"tower_id": lt.id, "state": "draining"}, nil)
	require.Equal(t, http.StatusOK, code)

	code, raw := op.call(t, srv, http.MethodPost, "/tower/self/lifecycle",
		map[string]string{"tower_id": lt.id, "state": "active"}, nil)
	require.Equal(t, http.StatusOK, code, raw)
	require.True(t, b.tower.registry.MayTakeWork(lt.id))
}

// AN OPERATOR CANNOT PROMOTE THEMSELVES, and this is the test that caught it.
//
// The first version of the route allowed `active` as a DESTINATION, reasoning that the
// approved transition table would refuse it out of quarantine. It does not:
// quarantine->active is precisely the edge an administrator uses to promote, so it is legal,
// and an operator could leave quarantine in one call. The admission gate would have meant
// nothing at all.
//
// Refused as a PERMISSION (403) rather than a state conflict (409), because that is what it
// is: the move is legal for an administrator to make and is not this caller's to make.
// Resuming from DRAINING is returning a Tower to a state somebody already granted; leaving
// QUARANTINE is the grant. Only the from-and-to pair tells them apart.
func TestAnOperatorCannotUseSelfServiceToLeaveQuarantine(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	code, raw := op.call(t, srv, http.MethodPost, "/tower/self/lifecycle",
		map[string]string{"tower_id": lt.id, "state": "active"}, nil)
	require.Equal(t, http.StatusForbidden, code, raw)
	require.Contains(t, raw, "administrator")
	require.False(t, b.tower.registry.MayTakeWork(lt.id),
		"quarantine is the administrator's gate and stays that way")
}

// Retiring is terminal and is the operator's to make: it is their hardware.
func TestAnOperatorCanRetireTheirOwnTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	code, raw := op.call(t, srv, http.MethodPost, "/tower/self/lifecycle",
		map[string]string{"tower_id": lt.id, "state": "revoked"}, nil)
	require.Equal(t, http.StatusOK, code, raw)

	tw, ok := b.tower.registry.Get(lt.id)
	require.True(t, ok)
	require.Equal(t, admit.StateRevoked, tw.State)
	require.False(t, towerMayHoldLink(tw), "a retired Tower may not even hold the link")
}

// SOMEBODY ELSE'S TOWER IS NOT YOURS TO DRAIN. Answered as "no such Tower on this account",
// the same as one that does not exist, so this cannot be used to enumerate other people's.
func TestAnOperatorCannotDrainSomebodyElsesTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	owner := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, owner.login)
	stranger := signedInOperator(t, b, "hubot")

	code, raw := stranger.call(t, srv, http.MethodPost, "/tower/self/lifecycle",
		map[string]string{"tower_id": lt.id, "state": "draining"}, nil)
	require.Equal(t, http.StatusNotFound, code, raw)

	code, raw = stranger.call(t, srv, http.MethodPost, "/tower/self/lifecycle",
		map[string]string{"tower_id": "tw-nobody", "state": "draining"}, nil)
	require.Equal(t, http.StatusNotFound, code, raw)
}

// The states an operator may NOT set on themselves, and the other refusals.
func TestSelfServiceLifecycleRefusesWhatIsNotItsToDecide(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	// Suspension is a decision ABOUT an operator; letting them set it on themselves would
	// let a Tower under review clear itself by suspending and being reinstated.
	for _, state := range []string{"suspended", "quarantine", "expired", "pending"} {
		code, raw := op.call(t, srv, http.MethodPost, "/tower/self/lifecycle",
			map[string]string{"tower_id": lt.id, "state": state}, nil)
		require.Equal(t, http.StatusForbidden, code, "%s: %s", state, raw)
		require.Contains(t, raw, "administrator")
	}

	code, raw := op.call(t, srv, http.MethodPost, "/tower/self/lifecycle",
		map[string]string{"tower_id": lt.id, "state": "marvellous"}, nil)
	require.Equal(t, http.StatusForbidden, code, raw)

	code, _ = op.call(t, srv, http.MethodPost, "/tower/self/lifecycle",
		map[string]string{"state": "draining"}, nil)
	require.Equal(t, http.StatusBadRequest, code)

	// Signed out entirely.
	resp, err := http.Post(srv.URL+"/tower/self/lifecycle", "application/json",
		strings.NewReader(`{"tower_id":"`+lt.id+`","state":"draining"}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// And the wrong method.
	getResp, err := http.Get(srv.URL + "/tower/self/lifecycle")
	require.NoError(t, err)
	getResp.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, getResp.StatusCode)
}

// The promotion the route exists for (re-added after the invite-flow tests that carried it
// died with the invite flow): a QUARANTINED station is promoted to active by an admin, and
// only by an admin.
func TestPromoteMovesAQuarantinedStationToActive(t *testing.T) {
	b, srv := towerTestBroker(t)
	b.adminKey = "admin-secret"
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)
	owner := ownerPubkeyOf(t, b, op.login)

	// Admit WITHOUT promoting: quarantine is where admission leaves a station.
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sessionRaw := make([]byte, 32)
	copy(sessionRaw, "st-q")
	authObj, secret, err := attach.NewInvite(attach.Authorization{
		ID: "auth-st-q", Network: link.PublicNetwork, StationID: "st-q", Owner: owner,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: lt.id},
		AssertionKey: hexOf(pub), SessionKey: hexOf(ed25519.PublicKey(sessionRaw)),
	}, time.Hour, time.Now())
	require.NoError(t, err)
	require.NoError(t, b.tower.stationStore.PutAuthorization(authObj))
	_, err = b.tower.stations.Admit(attach.Proof{
		AuthID: "auth-st-q", Secret: secret, Network: link.PublicNetwork,
		StationID: "st-q", Owner: owner,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: lt.id},
		AssertionKey: hexOf(pub), SessionKey: hexOf(ed25519.PublicKey(sessionRaw)),
	})
	require.NoError(t, err)
	at, found, err := b.tower.stations.Station("st-q")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "quarantine", string(at.State), "admission leaves a station quarantined")

	// Not an admin: refused, nothing moves.
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/tower/station/promote",
		strings.NewReader(`{"station_id":"st-q"}`))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.NotEqual(t, http.StatusOK, resp.StatusCode, "promotion is an admin decision")
	at, _, _ = b.tower.stations.Station("st-q")
	require.Equal(t, "quarantine", string(at.State))

	// The admin promotes; the station is active.
	req, err = http.NewRequest(http.MethodPost, srv.URL+"/tower/station/promote",
		strings.NewReader(`{"station_id":"st-q"}`))
	require.NoError(t, err)
	req.Header.Set("X-Roger-Admin", "admin-secret")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, string(raw))
	require.Contains(t, string(raw), `"promoted":true`)
	at, _, _ = b.tower.stations.Station("st-q")
	require.Equal(t, "active", string(at.State))
}

// A LAPSED tower cannot use the link (re-added after the invite-flow rig that carried this
// died): the lease is what bounds what a Tower may do while nobody is watching, and an
// expired Tower's next hello is refused rather than quietly resuming.
func TestALapsedTowerCannotUseTheLink(t *testing.T) {
	b, srv := towerTestBroker(t)
	op := signedInOperator(t, b, "octocat")
	lt := enrolledTower(t, b, op.login)

	var acc link.Accepted
	code, raw := lt.call(t, srv, "/tower/session",
		jsonOf(t, link.Hello{
			Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
			Capabilities: mandatoryCaps(),
		}), &acc)
	require.Equal(t, http.StatusOK, code, raw, "a freshly admitted tower holds the link")

	// Backdate the LEASE itself: towerMayHoldLink refuses a lapsed lease even before any
	// state transition - the lease is the bound on unsupervised time.
	tw, found, err := b.tower.admitStore.TowerByID(lt.id)
	require.NoError(t, err)
	require.True(t, found)
	tw.LeaseExpires = time.Now().Add(-time.Minute)
	won, err := b.tower.admitStore.CASTower(tw)
	require.NoError(t, err)
	require.True(t, won)

	code, raw = lt.call(t, srv, "/tower/session",
		jsonOf(t, link.Hello{
			Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
			Capabilities: mandatoryCaps(),
		}), nil)
	require.NotEqual(t, http.StatusOK, code, "an expired tower's hello is refused: %s", raw)
}
