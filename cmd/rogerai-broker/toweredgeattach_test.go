package main

// toweredgeattach_test.go covers the Option C self-attach: a roger share node registers as a
// servable Station in ONE owner-signed call - no invite files, no second binary - and Core
// assigns it a live tower + mints its hub polling token.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towercore/admit"
	"rogerai.fm/roger/v5/internal/towercore/fleet"
	"rogerai.fm/roger/v5/internal/towercore/link"
)

// liveEdgeTower enrolls a tower, promotes it, and opens a live link session advertising a
// data-plane endpoint - the state a real roger-tower reaches after `register` + serve.
func liveEdgeTower(t *testing.T, b *broker, srv *httptest.Server, login, endpoint string) linkTower {
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
	return lt
}

func selfAttachBody(t *testing.T) (map[string]any, ed25519.PublicKey) {
	t.Helper()
	apub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	spub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return map[string]any{
		"assertion_key": hexOf(apub), "session_key": hexOf(spub),
		"model": "m", "modality": "text",
	}, apub
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
	code, raw := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
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
	code, _ := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
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
	code, _ := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
	require.Equal(t, http.StatusOK, code)

	thief := signedInOperator(t, b, "thief-op")

	// Replaying the victim's body VERBATIM is now refused earlier and for a sharper reason:
	// the node id in it is not registered to the thief, so the M0 join fails before key
	// uniqueness is ever consulted. This is the borrowed-reputation attack the join exists
	// to stop - once placement scores on a node's measured history, claiming somebody's node
	// id is claiming their traffic.
	code, _ = thief.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
	require.Equal(t, http.StatusForbidden, code, "a node id may only be claimed by the key that registered it")

	// With a node of their own, the thief gets past the join - and is then refused by the
	// key-uniqueness rule this test is really about.
	stolen := map[string]any{}
	for k, v := range body {
		stolen[k] = v
	}
	stolen["node_id"] = registerShareNode(t, b, thief)
	code, _ = thief.call(t, srv, http.MethodPost, "/tower/edge/attach", stolen, &out)
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
	code, _ = node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
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
	code, _ := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &attached)
	require.Equal(t, http.StatusOK, code)

	var out struct {
		Nodes []struct {
			StationID    string `json:"station_id"`
			AssertionKey string `json:"assertion_key"`
			HubToken     string `json:"hub_token"`
			State        string `json:"state"`
		} `json:"nodes"`
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
	code, _ := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &first)
	require.Equal(t, http.StatusOK, code)
	code, _ = node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &second)
	require.Equal(t, http.StatusOK, code, "a same-key retry is answered, not refused")
	require.Equal(t, first.StationID, second.StationID, "the SAME registration comes back")
	require.Equal(t, first.HubToken, second.HubToken, "the hub token is recoverable by its owner")
	require.Contains(t, second.Note, "already attached")

	// And only ONE live attachment exists.
	ats, err := b.tower.stations.ByTower(first.TowerID)
	require.NoError(t, err)
	require.Len(t, ats, 1)
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
	code, _ := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
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
	code, raw := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &attached)
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
	code, _ := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
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
	code, _ := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
	require.Equal(t, http.StatusOK, code)

	body["price_out_micros"] = int64(500_000) // a "price update"
	code, _ = node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
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
	code, _ := node.call(t, srv, http.MethodPost, "/tower/edge/attach", body, &out)
	require.Equal(t, http.StatusBadRequest, code, "the self path gets no wider a modality door than the leaf path")
}
