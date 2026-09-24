package edge_test

// A LIVE end-to-end of the UniFi-style join (features/edge/claim.feature): a phone advertises as a
// candidate, the owner ADOPTS it on the authority, the phone CLAIMS its certificate, then PRESENTS
// and is drawn as a verified member - no address typed on the phone, no manual allow. Real
// authority HTTP server (the same /edge/enroll + /edge/claim + /edge/present mux edgehost mounts),
// real crypto, real fleet.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/edgeauth/enrollhttp"
	"rogerai.fm/roger/v6/internal/store"
)

func TestAdoptClaimPresentMakesAMemberWithNoTyping(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "RogGentooEdged")
	require.NoError(t, err)
	fleet := edge.NewFleet(store.NewMem(), edgeauth.LocalAccount)

	mux := http.NewServeMux()
	mux.Handle(edge.PresencePath, edge.PresenceHandler(fleet, local.Authority(), nil))
	mux.Handle("/", enrollhttp.Handler(local)) // serves /edge/enroll AND /edge/claim
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// The phone's node key. It advertised as a candidate carrying this node id (acct empty, no cert).
	nodePub, nodePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	nodeID := edge.NodeID(nodePub)

	// A claim BEFORE the owner adopts is refused - nothing is granted.
	pre := edgeauth.NewClaimRequest("pixel-8", "mobile", nodePub, time.Now())
	pre.Sign(nodePriv)
	_, err = enrollhttp.Claim(context.Background(), srv.URL, pre)
	require.Error(t, err, "a claim for an un-adopted node must be refused")

	// The owner ADOPTS the candidate on the authority (what `roger edge adopt <id>` / the [3] adopt
	// key does): it grants the claim.
	require.NoError(t, local.GrantClaim(nodeID, "pixel-8"))

	// The phone CLAIMS and receives its certificate - no address typed, no manual allow.
	req := edgeauth.NewClaimRequest("pixel-8", "mobile", nodePub, time.Now())
	req.Sign(nodePriv)
	resp, err := enrollhttp.Claim(context.Background(), srv.URL, req)
	require.NoError(t, err)
	require.Equal(t, nodeID, resp.NodeID)
	require.Equal(t, edgeauth.LocalAccount, resp.Account)
	require.Contains(t, resp.Cert, "BEGIN CERTIFICATE")

	// It is not yet on the map - claiming issues the cert; presence puts it on the map.
	_, known, err := fleet.Get(nodeID)
	require.NoError(t, err)
	require.False(t, known)

	// The phone PRESENTS with its new certificate and becomes a verified member.
	present := edge.PresenceRequest{
		NodeID: nodeID, Cert: resp.Cert, Name: "pixel-8", Kind: "mobile",
		Caps: []string{"classify"}, Nonce: hex.EncodeToString(mustRand(16)), TS: time.Now().Unix(),
	}
	present.Sig = hex.EncodeToString(ed25519.Sign(nodePriv, presenceCanonical(present)))
	code, body := postJSON(t, srv.URL+edge.PresencePath, present)
	require.Equal(t, http.StatusOK, code, body)

	n, known, err := fleet.Get(nodeID)
	require.NoError(t, err)
	require.True(t, known, "after adopt -> claim -> present, the phone is a member")
	require.Equal(t, string(edge.PresenceVerified), n.Presence)
	require.Equal(t, "pixel-8", n.Name)

	// The grant was consumed by the claim: a second claim is refused.
	again := edgeauth.NewClaimRequest("pixel-8", "mobile", nodePub, time.Now())
	again.Sign(nodePriv)
	_, err = enrollhttp.Claim(context.Background(), srv.URL, again)
	require.Error(t, err, "the grant is one-time")
}

func mustRand(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}
