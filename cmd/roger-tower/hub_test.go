package main

// hub_test.go covers the ONE PRODUCTION PATH from Roger Core's JSON to a hub registration.
//
// It had none. The e2e test in cmd/rogerai-broker reimplements this wiring in a helper of its
// own (hubAuthOf) and registers the node itself, so it proves the ATTACHMENT carries a hex
// assertion key and proves nothing whatever about the code that reads one. A field name that
// did not match, a key Core sent base64 while this read hex, a length check that let a short
// key through - any of those would have shipped green and shown up as a fleet that silently
// stopped serving.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/towerhub"
	"rogerai.fm/roger/v5/internal/towerjoin"
)

const hubTestTowerID = "tw-hub-test"

// coreHubNodesBody is Roger Core's answer to /tower/hub/nodes, written out in the shape the
// broker's towerHubNodes handler emits (cmd/rogerai-broker/toweredgeattach.go). It is a literal
// rather than a call into the broker because these are two separate programs that only ever
// meet over this JSON - which is exactly why the field names are worth pinning from this side
// as well as from the producer's.
const coreHubNodesBody = `{"nodes":[
  {"station_id":"st-signing","assertion_key":"%SIGNING%","hub_token":"tok-signing","state":"active"},
  {"station_id":"st-unusable","assertion_key":"not-hex","hub_token":"tok-unusable","state":"active"},
  {"station_id":"st-short","assertion_key":"aabbcc","hub_token":"tok-short","state":"active"}
]}`

// THE WHOLE PATH: Core's bytes in, a hub that will verify this node's signature out.
func TestHubNodeRegistrationReadsCoresAssertionKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var answer struct {
		Nodes []towerjoin.HubNode `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(
		[]byte(strings.ReplaceAll(coreHubNodesBody, "%SIGNING%", hex.EncodeToString(pub))), &answer))
	require.Len(t, answer.Nodes, 3, "Core's field names and towerjoin.HubNode's tags no longer agree")

	server := towerhub.NewServer(towerhub.New(),
		func(grant []byte) (string, string, error) { return "att", "st-signing", nil },
		towerhub.ServerOptions{TowerID: hubTestTowerID, PollTTL: 100 * time.Millisecond,
			AllowLegacyBearer: true})
	var warnings strings.Builder
	seen := registerHubNodes(server, answer.Nodes, &warnings)
	require.Equal(t, map[string]bool{"st-signing": true, "st-unusable": true, "st-short": true}, seen,
		"every Station Core listed is registered, so the refresher does not unregister it a second later")

	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathPoll, server.Poll)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// THE PROOF: a real signed poll, made exactly as a node makes one, is accepted - which can
	// only be true if the key Core sent as hex arrived at RegisterNode as 32 raw bytes.
	client := &towerhub.Client{BaseURL: srv.URL, TowerID: hubTestTowerID,
		// The fingerprint Core would have handed the node at attach: without it a client
		// refuses to adopt the epoch this hub names, because on the real link that value
		// arrives on an unauthenticated 401.
		TowerKeyHash: server.EpochKeyHash(),
		Sign:         towerhub.SignWith(priv), HTTP: &http.Client{Timeout: 5 * time.Second}}
	_, ok, perr := client.PollJob(t.Context(), "st-signing")
	require.NoError(t, perr, "the hub refused a poll signed with the key Core sent it")
	require.False(t, ok, "an idle queue answers empty")

	// A KEY THAT WILL NOT DECODE IS DROPPED, NOT REGISTERED SHORT, and the operator is told
	// once. Registering a truncated key would refuse that node's every poll forever, and the
	// operator would see only a station that never earns.
	require.Contains(t, warnings.String(), "st-unusable")
	require.Contains(t, warnings.String(), "st-short")
	require.NotContains(t, warnings.String(), "st-signing")
	for _, station := range []string{"st-unusable", "st-short"} {
		resp := pollWithSignature(t, srv.URL, station, priv)
		require.Equal(t, http.StatusUnauthorized, resp,
			"%s was registered with a key Core sent unusably", station)
	}

	// AND NEITHER OF THEM HOLDS ITS LEGACY TOKEN EITHER, which is a correction to what this
	// test used to assert.
	//
	// It pinned the opposite - that the bearer survives an unusable key - on the reading that
	// "no usable assertion key" describes a node too old to sign. It does not. An EMPTY key
	// describes that node, and that case still registers the token (see the empty-key leg
	// below). A key that is PRESENT and will not decode describes corruption on a Station that
	// definitely has a good key at Core: every self-attached Station is admitted with a hex
	// assertion key, and checkBindings makes it immutable for the life of the Station ID.
	//
	// Honouring the bearer there is the worst available answer. It opens that Station's queue,
	// over a plaintext link, to a string any on-path observer already holds - for a Station
	// that can no longer be authenticated any other way, so nothing will ever flip the latch
	// that would close it again. It also used to UNLATCH a Station that had been signing for
	// days, because the changed key cleared the "this Station signs" flag (see
	// internal/towerhub: TestAnUndecodableKeyDoesNotUnlatchAStationThatSigns).
	for _, station := range []string{"st-unusable", "st-short"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+towerhub.PathPoll+"?station="+station, nil)
		req.Header.Set("Authorization", "Bearer tok-"+strings.TrimPrefix(station, "st-"))
		resp, rerr := http.DefaultClient.Do(req)
		require.NoError(t, rerr)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"%s: a bearer was registered against a key this tower cannot check", station)
	}
}

// THE EMPTY KEY IS THE CASE THE TOLERANCE IS ACTUALLY FOR, and it is still served.
//
// A Station Core sends no assertion key for is one whose Core predates signed polls, or whose
// attachment does - the genuine "this node is older than the change" population the transition
// promise was made to. Its bearer is registered, it keeps earning, and its own first signature
// would end the tolerance for it if it ever made one. The distinction between this and a
// mangled key is the whole of the fix above, so it is pinned from both sides.
func TestAStationWithNoAssertionKeyKeepsItsLegacyBearer(t *testing.T) {
	server := towerhub.NewServer(towerhub.New(),
		func(grant []byte) (string, string, error) { return "att", "st-old", nil },
		towerhub.ServerOptions{TowerID: hubTestTowerID, PollTTL: 100 * time.Millisecond,
			AllowLegacyBearer: true})
	var warnings strings.Builder
	seen := registerHubNodes(server, []towerjoin.HubNode{
		{StationID: "st-old", AssertionKey: "", HubToken: "tok-old"},
	}, &warnings)
	require.Equal(t, map[string]bool{"st-old": true}, seen)
	require.Empty(t, warnings.String(), "an absent key is the expected state of an old node, not a fault")

	mux := http.NewServeMux()
	mux.HandleFunc(towerhub.PathPoll, server.Poll)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+towerhub.PathPoll+"?station=st-old", nil)
	req.Header.Set("Authorization", "Bearer tok-old")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode,
		"the transition tolerance no longer reaches the nodes it was written for")
}

// pollWithSignature signs a poll for `station` with priv and returns the status. It builds the
// request by hand because towerhub.Client mints its own nonce per call and this needs to name a
// Station the caller is not the registered node for.
func pollWithSignature(t *testing.T, base, station string, priv ed25519.PrivateKey) int {
	t.Helper()
	q := url.Values{"station": {station}, "tower": {hubTestTowerID},
		"nonce": {"00112233445566778899aabbccddeeff"}}
	target := towerhub.PathPoll + "?" + q.Encode()
	pub, ts, sig := protocol.SignRequest(priv, http.MethodGet, target, nil)
	req, err := http.NewRequest(http.MethodGet, base+target, nil)
	require.NoError(t, err)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}
