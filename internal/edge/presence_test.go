package edge

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// presenceFixture builds a real fleet + a real authority and enrolls (issues a cert for) one node,
// returning everything a presence needs. No mocks: the crypto and the fleet are the production ones.
func presenceFixture(t *testing.T) (f *Fleet, a *cert.Authority, priv ed25519.PrivateKey, id, certPEM string) {
	t.Helper()
	f = NewFleet(store.NewMem(), "acct-1")
	var err error
	a, err = cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	pub, p, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	priv = p
	id = NodeID(pub)
	leaf, err := a.Issue(id, pub)
	require.NoError(t, err)
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}))
	return f, a, priv, id, certPEM
}

func validPresence(id, certPEM string) PresenceRequest {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	return PresenceRequest{
		NodeID: id, Cert: certPEM, Name: "pixel-8", Kind: "mobile",
		Caps: []string{"classify"}, Addr: "192.168.1.50:8791",
		Nonce: hex.EncodeToString(nonce), TS: time.Now().Unix(),
	}
}

func signPresence(p *PresenceRequest, priv ed25519.PrivateKey) {
	p.Sig = hex.EncodeToString(ed25519.Sign(priv, p.canonical()))
}

func doPresence(h http.Handler, p PresenceRequest) *httptest.ResponseRecorder {
	body, _ := json.Marshal(p)
	r := httptest.NewRequest(http.MethodPost, PresencePath, strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPresenceAcceptsAValidMember(t *testing.T) {
	f, a, priv, id, certPEM := presenceFixture(t)
	h := PresenceHandler(f, a, time.Now)

	p := validPresence(id, certPEM)
	signPresence(&p, priv)
	w := doPresence(h, p)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	n, ok, err := f.Get(id)
	require.NoError(t, err)
	require.True(t, ok, "the presented node is now in the fleet")
	require.Equal(t, string(PresenceVerified), n.Presence, "a pushed member is verified, like a dialed one")
	require.Equal(t, "pixel-8", n.Name)
	require.Equal(t, "mobile", n.Kind)
	require.Contains(t, capNames(n.Caps), "classify")
}

func TestPresenceRefreshesLastSeen(t *testing.T) {
	f, a, priv, id, certPEM := presenceFixture(t)
	now := time.Unix(1_000_000, 0)
	h := PresenceHandler(f, a, func() time.Time { return now })

	p := validPresence(id, certPEM)
	p.TS = now.Unix()
	signPresence(&p, priv)
	require.Equal(t, http.StatusOK, doPresence(h, p).Code)
	first, _, _ := f.Get(id)

	now = now.Add(30 * time.Second)
	p2 := validPresence(id, certPEM) // fresh nonce
	p2.TS = now.Unix()
	signPresence(&p2, priv)
	require.Equal(t, http.StatusOK, doPresence(h, p2).Code)
	second, _, _ := f.Get(id)
	require.GreaterOrEqual(t, second.LastSeen, first.LastSeen, "presenting again advances last-seen")
}

func TestPresenceRefusesForeignAuthority(t *testing.T) {
	f, _, priv, id, certPEM := presenceFixture(t)
	// A DIFFERENT authority verifies - the certificate does not chain to it.
	other, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	h := PresenceHandler(f, other, time.Now)

	p := validPresence(id, certPEM)
	signPresence(&p, priv)
	require.Equal(t, http.StatusForbidden, doPresence(h, p).Code)
	_, ok, _ := f.Get(id)
	require.False(t, ok, "nothing is written for a foreign certificate")
}

func TestPresenceRefusesIdentityMismatch(t *testing.T) {
	f, a, priv, _, certPEM := presenceFixture(t)
	h := PresenceHandler(f, a, time.Now)

	p := validPresence("n_somethingelse000000000000000000000000000000000000", certPEM)
	signPresence(&p, priv)
	require.Equal(t, http.StatusForbidden, doPresence(h, p).Code)
}

func TestPresenceRefusesBadSignature(t *testing.T) {
	f, a, _, id, certPEM := presenceFixture(t)
	h := PresenceHandler(f, a, time.Now)

	// Sign with a DIFFERENT key than the certificate's.
	_, wrong, _ := ed25519.GenerateKey(rand.Reader)
	p := validPresence(id, certPEM)
	signPresence(&p, wrong)
	require.Equal(t, http.StatusForbidden, doPresence(h, p).Code)
	_, ok, _ := f.Get(id)
	require.False(t, ok)
}

func TestPresenceRefusesStale(t *testing.T) {
	f, a, priv, id, certPEM := presenceFixture(t)
	h := PresenceHandler(f, a, time.Now)

	p := validPresence(id, certPEM)
	p.TS = time.Now().Add(-30 * time.Minute).Unix()
	signPresence(&p, priv)
	require.Equal(t, http.StatusForbidden, doPresence(h, p).Code)
}

func TestPresenceRefusesReplay(t *testing.T) {
	f, a, priv, id, certPEM := presenceFixture(t)
	h := PresenceHandler(f, a, time.Now)

	p := validPresence(id, certPEM)
	signPresence(&p, priv)
	require.Equal(t, http.StatusOK, doPresence(h, p).Code)
	// The EXACT same signed presence again is a replay.
	require.Equal(t, http.StatusForbidden, doPresence(h, p).Code)
}

func TestPresenceRefusesRevoked(t *testing.T) {
	f, a, priv, id, certPEM := presenceFixture(t)
	// Revoke the node's certificate serial, as `roger edge forget` would.
	block, _ := pem.Decode([]byte(certPEM))
	leaf, err := parseLeafPEM(certPEM)
	require.NoError(t, err)
	require.NotNil(t, block)
	require.NoError(t, a.Revoke(leaf.SerialNumber))
	h := PresenceHandler(f, a, time.Now)

	p := validPresence(id, certPEM)
	signPresence(&p, priv)
	require.Equal(t, http.StatusForbidden, doPresence(h, p).Code, "a revoked node cannot re-appear by pushing")
	_, ok, _ := f.Get(id)
	require.False(t, ok)
}

func TestPresenceRefusesNonPost(t *testing.T) {
	f, a, _, _, _ := presenceFixture(t)
	h := PresenceHandler(f, a, time.Now)
	r := httptest.NewRequest(http.MethodGet, PresencePath, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// capNames is a tiny helper for asserting on the recorded capabilities.
func capNames(caps []store.EdgeCap) []string {
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, c.Name)
	}
	return out
}
