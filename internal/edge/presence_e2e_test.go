package edge_test

// A LIVE end-to-end of the phone's whole Edge flow, over a real authority HTTP server: the same two
// endpoints edgehost mounts (/edge/enroll + /edge/present), a real local authority, real crypto,
// real fleet. It walks exactly what the iOS app does - allow the account key, enroll the node, then
// PRESENT it - and asserts the node lands in the fleet as a verified member. This is the "does the
// running server actually do it" check the handler unit tests cannot give.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
	"rogerai.fm/roger/v6/internal/store"
)

func TestPhoneEnrollThenPresentAppearsAsVerifiedMember(t *testing.T) {
	// The authority, exactly as edgehost.startAuthority assembles it: one server answering both
	// enrollment and presence over the same fleet + authority.
	local, err := edgeauth.Designate(t.TempDir(), "RogGentooEdged")
	require.NoError(t, err)
	fleet := edge.NewFleet(store.NewMem(), edgeauth.LocalAccount)

	mux := http.NewServeMux()
	mux.Handle(edge.PresencePath, edge.PresenceHandler(fleet, local.Authority(), nil))
	mux.Handle("/", enrollhttp.Handler(local))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// The phone has TWO keys: the account (user) key the owner authorizes, and the node key.
	_, userPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	nodePub, nodePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	nodeID := edge.NodeID(nodePub)

	// 1. The owner authorizes the phone's account key: `roger edge authority allow <user key>`.
	require.NoError(t, local.Allow(hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))))

	// 2. The phone enrolls (POST /edge/enroll), signed by the account key, and gets a certificate.
	req := edgeauth.NewRequest("", "pixel-8", "mobile", nodePub, time.Now())
	req.Sign(userPriv)
	resp, err := enrollhttp.Enroll(context.Background(), srv.URL, req)
	require.NoError(t, err)
	require.Equal(t, nodeID, resp.NodeID)
	require.Equal(t, edgeauth.LocalAccount, resp.Account)
	require.Contains(t, resp.Cert, "BEGIN CERTIFICATE")

	// Before presenting, the phone is NOT in the fleet - enrollment issues a cert, it does not
	// place the node on the map. This is the gap presence closes.
	_, known, err := fleet.Get(nodeID)
	require.NoError(t, err)
	require.False(t, known, "enrollment alone does not put the node on the map")

	// 3. The phone PRESENTS (POST /edge/present), signed by the NODE key, carrying its certificate.
	present := edge.PresenceRequest{
		NodeID: nodeID, Cert: resp.Cert, Name: "pixel-8", Kind: "mobile",
		Caps: []string{"classify"}, Addr: "", Nonce: randHex(t, 16), TS: time.Now().Unix(),
	}
	present.Sig = hex.EncodeToString(ed25519.Sign(nodePriv, presenceCanonical(present)))
	code, body := postJSON(t, srv.URL+edge.PresencePath, present)
	require.Equal(t, http.StatusOK, code, body)

	// 4. The node is now a VERIFIED member on the authority's map, with its kind and capabilities.
	n, known, err := fleet.Get(nodeID)
	require.NoError(t, err)
	require.True(t, known, "after presenting, the phone is on the map")
	require.Equal(t, string(edge.PresenceVerified), n.Presence)
	require.Equal(t, "pixel-8", n.Name)
	require.Equal(t, "mobile", n.Kind)

	// A replay of the exact same presence is refused (the server's replay guard, live).
	code, _ = postJSON(t, srv.URL+edge.PresencePath, present)
	require.Equal(t, http.StatusForbidden, code, "the running server refuses a replay")
}

// presenceCanonical rebuilds the presence signed-bytes the way an external caller (the phone) must,
// since the field is unexported. It must equal edge.PresenceRequest.canonical().
func presenceCanonical(p edge.PresenceRequest) []byte {
	return []byte(strings.Join([]string{
		"rogerai-edge-present/v1", p.NodeID, p.Name, p.Kind, strings.Join(p.Caps, ","), p.Addr,
		p.Nonce, itoa64(p.TS),
	}, "\n"))
}

func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func postJSON(t *testing.T, url string, v any) (int, string) {
	t.Helper()
	body, err := json.Marshal(v)
	require.NoError(t, err)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(body)))
	require.NoError(t, err)
	defer resp.Body.Close()
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, string(buf[:n])
}
