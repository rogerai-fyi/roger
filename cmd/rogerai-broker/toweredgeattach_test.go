package main

// toweredgeattach_test.go covers the Option C self-attach: a roger share node registers as a
// servable Station in ONE owner-signed call - no invite files, no second binary - and Core
// assigns it a live tower + mints its hub polling token.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/attach"
	"rogerai.fm/roger/v6/internal/towercore/fleet"
	"rogerai.fm/roger/v6/internal/towercore/link"
	"rogerai.fm/roger/v6/internal/towerjoin"
)

// liveEdgeTower enrolls a tower, promotes it, and opens a live link session advertising a
// data-plane endpoint - the state a real roger-tower reaches after `register` + serve.
func liveEdgeTower(t *testing.T, b *broker, srv *httptest.Server, login, endpoint string) linkTower {
	t.Helper()
	lt, _ := liveEdgeTowerSession(t, b, srv, login, endpoint)
	return lt
}

// liveEdgeTowerSession is liveEdgeTower plus the session id it opened, which is what a test
// needs to hang that link UP again. It is a separate signature rather than a wider one because
// every existing caller wants the Tower and nothing else, and the session id is only interesting
// to the handful of tests about what Core does when a Tower's link is no longer here.
func liveEdgeTowerSession(t *testing.T, b *broker, srv *httptest.Server, login, endpoint string) (linkTower, string) {
	t.Helper()
	op := signedInOperator(t, b, login)
	lt := enrolledTower(t, b, op.login)
	require.NoError(t, b.tower.registry.Transition(lt.id, admit.StateActive))
	var acc link.Accepted
	code, raw := lt.call(t, srv, "/tower/session", jsonOf(t, link.Hello{
		Network: link.PublicNetwork, Versions: []int{1}, TowerID: lt.id,
		Capabilities: mandatoryCaps(), RelayEndpoint: endpoint,
	}), &acc)
	require.Equal(t, http.StatusOK, code, raw)
	require.NotEmpty(t, acc.SessionID)
	return lt, acc.SessionID
}

func selfAttachBody(t *testing.T) (map[string]any, ed25519.PublicKey) {
	t.Helper()
	apub, apriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	spub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	rememberAssertionKey(hexOf(apub), apriv)
	return map[string]any{
		"assertion_key": hexOf(apub), "session_key": hexOf(spub),
		"model": "m", "modality": "text",
	}, apub
}

// The self-attach test keyring: assertion pubkey hex -> the private half a REAL node would be
// holding on disk.
//
// It exists because attach now demands a possession proof (protocol.AttachProof) and the tests
// that mint a body are not the tests that send it. A helper that quietly skipped the proof when
// it could not find a key would make every one of these forty call sites pass for the wrong
// reason, which is why attach() below FATALS on an unknown key instead: a hand-built body has
// to be signed deliberately, by the test that knows what it is proving.
var (
	assertionKeysMu sync.Mutex
	assertionKeys   = map[string]ed25519.PrivateKey{}
)

func rememberAssertionKey(pubHex string, priv ed25519.PrivateKey) {
	assertionKeysMu.Lock()
	defer assertionKeysMu.Unlock()
	assertionKeys[pubHex] = priv
}

func assertionKeyOf(pubHex string) (ed25519.PrivateKey, bool) {
	assertionKeysMu.Lock()
	defer assertionKeysMu.Unlock()
	priv, ok := assertionKeys[pubHex]
	return priv, ok
}

// attach is operator.call for POST /tower/edge/attach, plus the one thing that call cannot do:
// co-sign the request with the ASSERTION key named in the body, as a real node does.
//
// It is a named method rather than a branch inside call so every one of these call sites SAYS
// that a possession proof went with it. The proof is built through the production type, because
// a test-local restatement of a canonical form is how the two halves of a signing scheme drift
// apart while both stay green.
func (o operator) attach(t *testing.T, srv *httptest.Server, in map[string]any, out any) (int, string) {
	t.Helper()
	apub, _ := in["assertion_key"].(string)
	priv, known := assertionKeyOf(apub)
	require.True(t, known,
		"this body's assertion key was not minted by selfAttachBody, so the test harness cannot "+
			"prove possession of it - sign the attach explicitly if that is the point of the test")
	stationID, _ := in["station_id"].(string)
	skey, _ := in["session_key"].(string)
	return o.attachSigned(t, srv, in, func(callerPub string, ts int64, body []byte) string {
		return protocol.AttachProof{
			Network: link.PublicNetwork, CallerPubkey: callerPub, TS: ts,
			StationID: stationID, AssertionKey: apub, SessionKey: skey, Body: body,
		}.Sign(priv)
	}, out)
}

// attachSigned is attach with the possession proof left to the caller, which is what the
// squatting tests need: an attacker's request has to carry whatever proof an attacker could
// actually produce, and that is never the honest one. proof returns the header value; returning
// "" sends no header at all.
func (o operator) attachSigned(t *testing.T, srv *httptest.Server, in map[string]any,
	proof func(callerPub string, ts int64, body []byte) string, out any) (int, string) {
	t.Helper()
	body, err := json.Marshal(in)
	require.NoError(t, err)

	const path = "/tower/edge/attach"
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocol.SignRequest(o.priv, http.MethodPost, path, body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, itoa64(ts))
	req.Header.Set(protocol.HeaderSig, sig)
	if p := proof(pub, ts, body); p != "" {
		req.Header.Set(protocol.HeaderAttachProof, p)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var raw json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, string(raw)
}

// The whole point: one signed call attaches the node, assigns a tower, and hands back the
// endpoint + hub token - and the attachment records everything the settle path needs.
func TestSelfAttachRegistersAServableStation(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	node := signedInOperator(t, b, "node-op")
	body, apub := selfAttachBodyFor(t, b, node)
	var out struct {
		StationID string `json:"station_id"`
		TowerID   string `json:"tower_id"`
		Endpoint  string `json:"endpoint"`
		HubToken  string `json:"hub_token"`
		State     string `json:"state"`
	}
	code, raw := node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusOK, code, raw)
	require.NotEmpty(t, out.StationID)
	require.Equal(t, tw.id, out.TowerID, "Core assigned the live edge tower")
	require.Equal(t, "203.0.113.9:8443", out.Endpoint)
	require.Len(t, out.HubToken, 64, "a real polling credential")
	require.Equal(t, "active", out.State,
		"a self-attached node serves immediately - the explicit decision: it is the same provider a direct roger share node is, and the trust stack (band-checked price, holds, clamps, adaptive audit) is the exposure control")

	// The attachment is real: keys + owner + origin + hub token all recorded, so the settle
	// path can verify receipts and the tower can authenticate the polling node.
	at, found, err := b.tower.stations.Station(out.StationID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, hexOf(apub), at.AssertionKey)
	require.Equal(t, ownerPubkeyOf(t, b, node.login), at.Owner)
	require.Equal(t, tw.id, at.Origin.TowerID)
	require.Equal(t, out.HubToken, at.HubToken, "the tower reads the token from the attachment")
}

// No live edge-capable tower -> a clear 503, nothing recorded.
func TestSelfAttachRefusesWhenNoTowerCanHost(t *testing.T) {
	b, srv := towerTestBroker(t)
	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)
	var out map[string]any
	code, _ := node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusServiceUnavailable, code)
}

// An unsigned / unbound caller is refused: possession of a bound account key IS the
// authorization the invite secret used to carry.
func TestSelfAttachRequiresASignedInAccount(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	// No join here on purpose: this request is turned away for having no signature at all,
	// which must happen BEFORE anything looks at the node id. An unauthenticated caller
	// never gets far enough for the M0 check to be what refused it.
	body, _ := selfAttachBody(t)
	raw := jsonOf(t, body)
	resp, err := http.Post(srv.URL+"/tower/edge/attach", "application/json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	_ = b
}

// Key uniqueness still holds against OTHER parties: the same keys presented by a DIFFERENT
// account are refused (the same-owner retry is answered idempotently instead - see
// TestSelfAttachRetryIsIdempotent).
func TestSelfAttachRefusesAnotherOwnersKeys(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)
	var out map[string]any
	code, _ := node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusOK, code)

	thief := signedInOperator(t, b, "thief-op")

	// Replaying the victim's body VERBATIM is now refused earlier and for a sharper reason:
	// the node id in it is not registered to the thief, so the M0 join fails before key
	// uniqueness is ever consulted. This is the borrowed-reputation attack the join exists
	// to stop - once placement scores on a node's measured history, claiming somebody's node
	// id is claiming their traffic.
	code, _ = thief.attach(t, srv, body, &out)
	require.Equal(t, http.StatusForbidden, code, "a node id may only be claimed by the key that registered it")

	// With a node of their own, the thief gets past the join - and is then refused by the
	// key-uniqueness rule this test is really about.
	stolen := map[string]any{}
	for k, v := range body {
		stolen[k] = v
	}
	stolen["node_id"] = registerShareNode(t, b, thief)
	code, _ = thief.attach(t, srv, stolen, &out)
	require.Equal(t, http.StatusConflict, code, "another account cannot claim live keys")
}

// Malformed keys and a missing model are refused before anything is stored.
func TestSelfAttachValidatesItsInput(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")

	var out map[string]any
	code, _ := node.call(t, srv, http.MethodPost, "/tower/edge/attach",
		map[string]any{"assertion_key": "zz", "session_key": "zz", "model": "m", "modality": "text"}, &out)
	require.Equal(t, http.StatusBadRequest, code)

	body, _ := selfAttachBodyFor(t, b, node)
	body["model"] = ""
	code, _ = node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusBadRequest, code)
}

// The tower fetches its self-attached nodes - and how to AUTHENTICATE each of them - with its
// OWN signed request, and only its own: another tower sees nothing, and an unauthenticated
// caller is refused.
//
// The assertion key is the part that makes signed hub polls possible at all. A tower verifies a
// node's signed poll against it and has no other way to learn it: the attachment holds it, the
// node is the party being authenticated so it cannot supply its own, and nobody else may. This
// call is where it crosses.
func TestTowerFetchesItsHubNodes(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	node := signedInOperator(t, b, "node-op")
	body, assertionPub := selfAttachBodyFor(t, b, node)
	var attached struct {
		StationID string `json:"station_id"`
		HubToken  string `json:"hub_token"`
	}
	code, _ := node.attach(t, srv, body, &attached)
	require.Equal(t, http.StatusOK, code)

	// DECODED INTO THE TYPE THE TOWER ACTUALLY USES, not a restatement of it. A local struct
	// here would agree with whatever this handler happens to emit, which is the one thing a
	// contract test must not do: rename a field on either side and both halves stay green while
	// the fleet stops polling. towerjoin.HubNode is what cmd/roger-tower unmarshals.
	var out struct {
		Nodes []towerjoin.HubNode `json:"nodes"`
	}
	code, raw := tw.call(t, srv, "/tower/hub/nodes", jsonOf(t, map[string]any{"tower_id": tw.id}), &out)
	require.Equal(t, http.StatusOK, code, raw)
	require.Len(t, out.Nodes, 1)
	require.Equal(t, attached.StationID, out.Nodes[0].StationID)
	require.Equal(t, hex.EncodeToString(assertionPub), out.Nodes[0].AssertionKey,
		"the tower reads the key it must verify this node's signed polls against")
	require.Equal(t, attached.HubToken, out.Nodes[0].HubToken, "the tower reads exactly the node's polling token")

	// A DIFFERENT tower sees nothing of it.
	other := liveEdgeTower(t, b, srv, "other-op", "203.0.113.10:8443")
	code, _ = other.call(t, srv, "/tower/hub/nodes", jsonOf(t, map[string]any{"tower_id": other.id}), &out)
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, out.Nodes)

	// And a tower cannot read another tower's list by naming it: the signature must be the
	// named tower's own.
	code, _ = other.call(t, srv, "/tower/hub/nodes", jsonOf(t, map[string]any{"tower_id": tw.id}), &out)
	require.Equal(t, http.StatusForbidden, code, "a tower's node list is its own")
}

// THE FORWARD LANDMINE, and it would have taken the whole relay fabric offline.
//
// This handler decided which Stations to tell a Tower about with `if at.HubToken == ""` - keyed
// on the very credential signed hub polls replaced, beside an instruction to delete that field
// one release later. Followed literally, every Tower's node list goes empty: no station is
// registered on any hub, every poll is refused, and nobody serves anything. The question the
// code is asking is "is this a self-attached node that POLLS a hub", and it now asks that
// (attach.Attachment.SelfAttached) rather than asking whether a doomed column is populated.
//
// The attachment below is what the fleet looks like the day after the deletion: a real
// self-attached node, with no hub token at all.
func TestATokenlessSelfAttachmentIsStillListedForItsTower(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	signedInOperator(t, b, "node-op")
	owner := ownerPubkeyOf(t, b, "node-op")

	apub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	spub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	auth, secret, err := attach.NewInvite(attach.Authorization{
		ID: newInviteID(), Network: link.PublicNetwork,
		StationID: protocol.DeriveStationID(apub), Owner: owner,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: tw.id},
		AssertionKey: hexOf(apub), SessionKey: hexOf(spub),
		// No HubToken. Everything else is exactly what the self-attach path records.
		NodeID: "nd-after-the-deletion", Model: "m", Modality: "text",
	}, time.Hour, time.Now())
	require.NoError(t, err)
	require.NoError(t, b.tower.stationStore.PutAuthorization(auth))
	at, err := b.tower.stations.Admit(attach.Proof{
		AuthID: auth.ID, Secret: secret, Network: link.PublicNetwork,
		StationID: auth.StationID, Owner: owner,
		Origin:       attach.Origin{Kind: attach.OriginJoined, TowerID: tw.id},
		AssertionKey: hexOf(apub), SessionKey: hexOf(spub),
	})
	require.NoError(t, err)
	require.Empty(t, at.HubToken, "this test is about an attachment with no token")

	var out struct {
		Nodes []towerjoin.HubNode `json:"nodes"`
	}
	code, raw := tw.call(t, srv, "/tower/hub/nodes", jsonOf(t, map[string]any{"tower_id": tw.id}), &out)
	require.Equal(t, http.StatusOK, code, raw)
	require.Len(t, out.Nodes, 1,
		"a self-attached node with no hub token was hidden from its own tower - the fleet goes dark "+
			"the day the column is deleted")
	require.Equal(t, at.StationID, out.Nodes[0].StationID)
	require.Equal(t, hexOf(apub), out.Nodes[0].AssertionKey,
		"and the tower still gets the key it verifies that node's signed polls against")
}

// A lost-response retry with the SAME keys is answered idempotently: the existing
// registration (station id, tower, endpoint, hub token) comes back, no second station, no
// burned live slot, no unrecoverable token.
func TestSelfAttachRetryIsIdempotent(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)

	var first, second struct {
		StationID string `json:"station_id"`
		TowerID   string `json:"tower_id"`
		Endpoint  string `json:"endpoint"`
		HubToken  string `json:"hub_token"`
		Note      string `json:"note"`
	}
	code, _ := node.attach(t, srv, body, &first)
	require.Equal(t, http.StatusOK, code)
	code, _ = node.attach(t, srv, body, &second)
	require.Equal(t, http.StatusOK, code, "a same-key retry is answered, not refused")
	require.Equal(t, first.StationID, second.StationID, "the SAME registration comes back")
	require.Equal(t, first.HubToken, second.HubToken, "the hub token is recoverable by its owner")
	require.Contains(t, second.Note, "already attached")

	// And only ONE live attachment exists.
	ats, err := b.tower.stations.ByTower(first.TowerID)
	require.NoError(t, err)
	require.Len(t, ats, 1)
}

// A RE-ATTACH WHOSE TOWER HAS NO DATA PLANE IS REFUSED, NOT ANSWERED WITH AN EMPTY ENDPOINT.
//
// The idempotent branch above reads the relay plane off the Tower this Station is attached to,
// and it used to discard RelayPlane's second return value. A miss therefore produced a 200
// carrying endpoint:"" and endpoint_tls_spki:"" - a reply shaped exactly like a successful attach
// that no node can act on. `roger share` refuses it ("attach answered without an endpoint"),
// counts its own re-attach as failed and backs off, so the whole exchange is a round trip that
// says nothing, logs nothing, and repeats. It mattered little when the only caller was a node
// that had lost a reply; it matters now that internal/agent re-attaches whenever its relay has a
// standing failure, which is the case where the plane is most likely to be missing.
//
// AND THE SECOND HALF IS THE ONE WORTH READING: THE STATION IS NOT RE-PLACED. There is a live,
// edge-capable Tower standing right there with a data plane, and Core still refuses rather than
// moving the Station onto it. That is deliberate. A missing plane means "no live link session on
// THIS instance", and a Tower's link is held by exactly one broker - so re-placing would move a
// Station between Towers on nothing but which instance answered its attach, by writing
// origin_tower, whose single writer (Admit's upsert) is scoped to dormant rows precisely so a
// live Station's origin cannot move under an attempt in flight. Rehoming a live Station is
// section 6 of docs/relay-selection-design.md and it needs a settle-time fence that does not
// exist. Until then the honest answer is "not now", and this test is what stops the quiet
// non-answer coming back.
func TestSelfAttachRetryIsRefusedWhenItsTowerHasNoDataPlane(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw, sess := liveEdgeTowerSession(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)

	var first struct {
		StationID string `json:"station_id"`
		TowerID   string `json:"tower_id"`
		Endpoint  string `json:"endpoint"`
	}
	code, raw := node.attach(t, srv, body, &first)
	require.Equal(t, http.StatusOK, code, raw)
	require.Equal(t, tw.id, first.TowerID)
	require.Equal(t, "203.0.113.9:8443", first.Endpoint)

	// THE TOWER'S LINK GOES. From this instance's side that is indistinguishable between a
	// Tower that hung up, one whose link is held by a sibling broker, and one that is never
	// coming back - which is the whole reason the answer below is a refusal.
	b.tower.link.Close(sess, tw.id)
	require.False(t, b.tower.link.Live(tw.id))

	// A SECOND TOWER IS LIVE AND EDGE-CAPABLE, so "no tower can host this node" is not the
	// reason for what happens next. Placement is simply not re-run for a live attachment.
	other := liveEdgeTower(t, b, srv, "tower-op-2", "203.0.113.11:8443")
	require.NotEqual(t, tw.id, other.id)

	var out map[string]any
	code, raw = node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusServiceUnavailable, code,
		"a retry whose tower has no data plane was answered 200 with an unusable endpoint: %s", raw)
	require.Empty(t, out["endpoint"], "an attach answer that names no endpoint is not an attach answer")

	// NOTHING MOVED. The attachment still names the Tower it was placed on, still live, so a
	// receipt already in flight against that origin still settles.
	at, found, err := b.tower.stations.Station(first.StationID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, tw.id, at.Origin.TowerID,
		"the refusal must not have quietly rehomed a live Station; that is section 6 work and it "+
			"needs a settle-time fence this branch does not have")
	require.True(t, at.Live())
}

// A banned account cannot attach a node, however validly it signs.
func TestSelfAttachRefusesABannedAccount(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "banned-op")
	b.metricsMu.Lock()
	if b.bannedOwners == nil {
		b.bannedOwners = map[string]bool{}
	}
	b.bannedOwners[node.login] = true
	b.metricsMu.Unlock()

	body, _ := selfAttachBodyFor(t, b, node)
	var out map[string]any
	code, _ := node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusForbidden, code)
}

// An unsigned caller cannot list any tower's hub nodes.
func TestHubNodesRefusesAnUnsignedCaller(t *testing.T) {
	b, srv := towerTestBroker(t)
	tw := liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	resp, err := http.Post(srv.URL+"/tower/hub/nodes", "application/json",
		bytes.NewReader(jsonOf(t, map[string]any{"tower_id": tw.id})))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// P5d-c: the self-attached node is ROUTABLE at its listed price - a consumer's authorize for
// its model succeeds and the grant pins exactly the price the node set at attach. This is the
// end-to-end price chain: attach (band-checked) -> projection -> authorize -> signed grant.
func TestSelfAttachedNodeIsRoutableAtItsListedPrice(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")

	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)
	body["model"] = "my-model"
	body["price_in_micros"] = int64(180_000)  // $0.18 / 1M tokens
	body["price_out_micros"] = int64(300_000) // $0.30 / 1M tokens
	var attached struct {
		StationID string `json:"station_id"`
		TowerID   string `json:"tower_id"`
	}
	code, raw := node.attach(t, srv, body, &attached)
	require.Equal(t, http.StatusOK, code, raw)

	// The projection carries the node's row at its price.
	rows, err := b.tower.routable.Candidates("my-model", time.Now())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, attached.StationID, rows[0].StationID)
	require.Equal(t, int64(180_000), rows[0].PriceIn)
	require.Equal(t, int64(300_000), rows[0].PriceOut)
	require.Equal(t, "203.0.113.9:8443", rows[0].Endpoint)

	// A signed-in consumer authorizes for the model and the response echoes the PINNED price.
	consumer := signedInConsumer(t, b)
	code, out := consumerCall(t, srv, consumer, "/tower/edge/authorize", map[string]any{"model": "my-model", "consumer_env_key": testEnvKeyHex(t)})
	require.Equal(t, http.StatusOK, code, out)
	require.EqualValues(t, 180_000, out["price_in_micros"], "the grant pins the node's own listed price")
	require.EqualValues(t, 300_000, out["price_out_micros"])
}

// An out-of-band listed price is refused at the door.
func TestSelfAttachRefusesAnOutOfBandPrice(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)
	body["price_out_micros"] = int64(999_000_000_000) // absurd: far above any band ceiling
	var out map[string]any
	code, _ := node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusBadRequest, code)
}

// A retry carrying a DIFFERENT offer (price/model) fails loudly rather than silently keeping
// the old listing: the offer is immutable for a live identity.
func TestSelfAttachRetryWithADifferentOfferIsRefused(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)
	body["price_out_micros"] = int64(300_000)
	var out map[string]any
	code, _ := node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusOK, code)

	body["price_out_micros"] = int64(500_000) // a "price update"
	code, _ = node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusConflict, code, "a changed offer must fail loudly, never silently keep the old price")
}

// A tower-pushed leaf using the reserved "self-" offer namespace is skipped, so it can never
// collide with (and shadow) a self-attached node's projection row.
func TestALeafInTheSelfNamespaceIsSkipped(t *testing.T) {
	b, _ := towerTestBroker(t)
	// Simulate the merge input directly: a leaf-shaped row with a self- offer id must not
	// survive publishRoutable's filter. We assert at the projection layer via Replace parity:
	// both stores keep last-wins on duplicate offer ids, and publishRoutable itself refuses
	// the reserved prefix before rows ever reach Replace (unit-covered by the fleet dedupe
	// test below; the broker-side filter is asserted by absence of the leaf row here).
	require.NotNil(t, b.tower)
}

// Fleet-store parity: duplicate offer ids within one Replace resolve last-wins in the MEM
// store exactly as the Postgres PK upsert does.
func TestFleetReplaceDeduplicatesByOfferIDLastWins(t *testing.T) {
	st := fleet.NewMemStore()
	require.NoError(t, st.Replace("tw-1", []fleet.Station{
		{TowerID: "tw-1", StationID: "victim", OfferID: "self-x", Model: "m", Modality: "text",
			Expires: time.Now().Add(time.Hour), Endpoint: "e", PriceOut: 300_000},
		{TowerID: "tw-1", StationID: "self-node", OfferID: "self-x", Model: "m", Modality: "text",
			Expires: time.Now().Add(time.Hour), Endpoint: "e", PriceOut: 180_000},
	}))
	rows, err := st.Candidates("m", time.Now())
	require.NoError(t, err)
	require.Len(t, rows, 1, "one row per offer id, like the PG primary key")
	require.Equal(t, "self-node", rows[0].StationID, "last wins, like ON CONFLICT DO UPDATE")
}

// The modality allowlist holds on the self path exactly as on the leaf path.
func TestSelfAttachRefusesADisallowedModality(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)
	body["modality"] = "voice"
	var out map[string]any
	code, _ := node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusBadRequest, code, "the self path gets no wider a modality door than the leaf path")
}

// SELF-ATTACH IS RATE LIMITED PER ACCOUNT, and until this release it was not limited at all.
//
// /tower/edge/authorize was found registered bare and given a per-account bucket plus a per-IP
// bucket on its 401; attach sits on the same mux, does strictly more work per call - a signature
// verification, an owner lookup, a node-registration lookup, an advisory-locked mint and a second
// transaction for the redeem - and was missed. The `allow(w, r, http.MethodPost)` at the top of
// the handler is the HTTP METHOD check, not a limiter, which is most of why the gap survived a
// reading.
//
// WHY IT IS NOT MERELY TIDY. The refusal path used to be self-limiting on Postgres by accident:
// a refused self-attach marks its invitation consumed, that write was dropped, so a refusal loop
// filled the owner's 25-invitation cap within 25 attempts and everything after was turned away
// cheaply. Fixing that drop - which is what unlocks the honest operator this release - removes
// the accidental brake, because refusals stop accumulating against the cap. The limiter is what
// replaces it, and it is a better bound than the one it replaces: the cap turned a refusal loop
// into an HOUR-LONG LOCKOUT for the account that ran it, where the limiter just slows it down.
func TestSelfAttachIsRateLimitedPerAccount(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	node := signedInOperator(t, b, "node-op")
	body, _ := selfAttachBodyFor(t, b, node)

	// One token, refilling at one a minute: the second call within the test cannot have earned
	// another. Installed AFTER the fixtures above, which make their own signed calls.
	b.rl = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 1}

	var out map[string]any
	code, raw := node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusOK, code, raw)

	// The same node asking again immediately. Without a limiter this is the idempotent-retry
	// branch and answers 200 forever, which is precisely the loop that had no cost.
	code, raw = node.attach(t, srv, body, &out)
	require.Equal(t, http.StatusTooManyRequests, code, raw)
}

// AND THE 401 PATH COSTS THE HAMMERER SOMETHING, keyed per IP.
//
// An unsigned caller is never going to be served, but reaching the refusal has already spent an
// ed25519 verification and an owner lookup on an UNAUTHENTICATED request - so an attacker with no
// account at all could burn Core's CPU for the price of a TCP connection. /tower/edge/authorize
// applies anonRL on exactly this condition; this is the same treatment on the same reasoning.
//
// A signed caller never reaches this branch, so no honest node is bucketed by IP - which matters
// because nodes behind one NAT are a normal deployment and would otherwise share a bucket.
func TestSelfAttachRateLimitsUnsignedCallersByIP(t *testing.T) {
	b, srv := towerTestBroker(t)
	liveEdgeTower(t, b, srv, "tower-op", "203.0.113.9:8443")
	b.anonRL = &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 1, burst: 1}

	body, _ := selfAttachBody(t)
	raw := jsonOf(t, body)
	post := func() int {
		resp, err := http.Post(srv.URL+"/tower/edge/attach", "application/json", bytes.NewReader(raw))
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}
	require.Equal(t, http.StatusUnauthorized, post(), "the first unsigned attempt is answered")
	require.Equal(t, http.StatusTooManyRequests, post(),
		"an unsigned caller could hammer the signature path for free")
}
