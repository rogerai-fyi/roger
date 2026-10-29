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

	"rogerai.fm/roger/v7/internal/store"
	"rogerai.fm/roger/v7/internal/towercore/cert"
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

// PresenceHandlerFunc reads the authority FRESH on every request, which is what lets a revocation
// made after startup take effect without a restart. Here the provider counts calls: one per request.
func TestPresenceReadsTrustPerRequest(t *testing.T) {
	f, a, priv, id, certPEM := presenceFixture(t)
	calls := 0
	h := PresenceHandlerFunc(f, func() *cert.Authority { calls++; return a }, time.Now)

	for i := 0; i < 3; i++ {
		p := validPresence(id, certPEM)
		signPresence(&p, priv)
		_ = doPresence(h, p)
	}
	require.Equal(t, 3, calls, "the authority is re-read once per presence, not captured once at startup")
}

// If the authority cannot be loaded (a failed reload), presence is REFUSED, never admitted on a
// stale snapshot - fail closed (audit 2026-09-23).
func TestPresenceFailsClosedWhenTrustUnavailable(t *testing.T) {
	f, _, priv, id, certPEM := presenceFixture(t)
	h := PresenceHandlerFunc(f, func() *cert.Authority { return nil }, time.Now)

	p := validPresence(id, certPEM)
	signPresence(&p, priv)
	w := doPresence(h, p)
	require.NotEqual(t, http.StatusOK, w.Code, "a presence must not be admitted when trust cannot be read")

	_, ok, err := f.Get(id)
	require.NoError(t, err)
	require.False(t, ok, "nothing is recorded when trust is unavailable")
}

// A presence with a near-future timestamp stays FRESH until TS+window, so its nonce must be
// remembered that long. Expiring the nonce at now+window (the old behaviour) pruned it while the
// same signed bytes were still acceptable, so a replay slipped through (audit 2026-09-24).
func TestPresenceNonceOutlivesAFutureDatedRequest(t *testing.T) {
	f, a, priv, id, certPEM := presenceFixture(t)
	base := time.Now()
	clock := base
	h := PresenceHandlerFunc(f, func() *cert.Authority { return a }, func() time.Time { return clock })

	// A presence dated 4 minutes in the future - within the +/-5m freshness window.
	p := validPresence(id, certPEM)
	p.TS = base.Add(4 * time.Minute).Unix()
	signPresence(&p, priv)
	require.Equal(t, http.StatusOK, doPresence(h, p).Code, "a near-future presence is accepted")

	// Six minutes later the request is STILL fresh (|6m-4m| = 2m < 5m) but its nonce would have been
	// pruned under a now+window expiry. Replaying the identical bytes must be refused.
	clock = base.Add(6 * time.Minute)
	require.NotEqual(t, http.StatusOK, doPresence(h, p).Code,
		"the same still-fresh presence must not be replayable after its original nonce window")
}

// features/edge/fleet_window.feature §8: a phone reports what a phone can honestly report - battery,
// charging, thermal state, whether the app is in front - SIGNED, in a v2 presence. A v1 presence is
// still accepted unchanged; vitals that are not signed are never believed.
func TestAPhonesVitalsArriveSignedAndAreRecorded(t *testing.T) {
	f, a, priv, id, certPEM := presenceFixture(t)
	h := PresenceHandler(f, a, time.Now)
	battery, charging, front := 64, true, false
	p := validPresence(id, certPEM)
	p.V = 2
	p.Vitals = &PhoneVitals{BatteryPct: &battery, Charging: &charging, Thermal: "fair", Foreground: &front}
	signPresence(&p, priv)
	require.Equal(t, http.StatusOK, doPresence(h, p).Code, "a signed v2 presence is accepted")
	n, _, _ := f.Get(id)
	require.NotNil(t, n.Phone)
	require.Equal(t, 64, *n.Phone.BatteryPct)
	require.True(t, *n.Phone.Charging)
	require.Equal(t, "fair", n.Phone.Thermal)
	require.False(t, *n.Phone.Foreground)
	require.NotZero(t, n.Phone.ReadAt)

	// The vitals are inside the signature: change one after signing and it is refused.
	p2 := validPresence(id, certPEM)
	p2.V = 2
	p2.Vitals = &PhoneVitals{BatteryPct: &battery}
	signPresence(&p2, priv)
	other := 5
	p2.Vitals.BatteryPct = &other
	require.Equal(t, http.StatusForbidden, doPresence(h, p2).Code)

	// Vitals on a v1 presence are not signed, so they are refused rather than believed.
	p3 := validPresence(id, certPEM)
	p3.Vitals = &PhoneVitals{BatteryPct: &battery}
	signPresence(&p3, priv)
	require.Equal(t, http.StatusForbidden, doPresence(h, p3).Code)

	// Nonsense is refused: a battery over 100, a thermal state that does not exist, a version from the future.
	for _, bad := range []func(*PresenceRequest){
		func(p *PresenceRequest) { b := 140; p.Vitals = &PhoneVitals{BatteryPct: &b} },
		func(p *PresenceRequest) { p.Vitals = &PhoneVitals{Thermal: "volcanic"} },
		func(p *PresenceRequest) { p.V = 4 },
	} {
		p := validPresence(id, certPEM)
		p.V = 2
		bad(&p)
		signPresence(&p, priv)
		require.Equal(t, http.StatusForbidden, doPresence(h, p).Code)
	}

	// A v1 presence with no vitals still works exactly as before, and does not erase what a v2 said.
	p4 := validPresence(id, certPEM)
	signPresence(&p4, priv)
	require.Equal(t, http.StatusOK, doPresence(h, p4).Code)
	n, _, _ = f.Get(id)
	require.NotNil(t, n.Phone, "an older app's presence does not wipe the last vitals")
}

// The v2 signed form, pinned byte for byte: this is what the Roger app must produce.
func TestTheV2PresenceCanonicalFormIsPinned(t *testing.T) {
	b, c, fg := 64, true, false
	p := PresenceRequest{NodeID: "n_x", Name: "nina", Kind: "mobile", Caps: []string{"classify"}, Addr: "",
		Nonce: "00", TS: 1700000000, V: 2, Vitals: &PhoneVitals{BatteryPct: &b, Charging: &c, Thermal: "fair", Foreground: &fg}}
	require.Equal(t, "rogerai-edge-present/v2\nn_x\nnina\nmobile\nclassify\n\n00\n1700000000\n64\n1\nfair\n0", string(p.canonical()))
	p.Vitals = &PhoneVitals{}
	require.Equal(t, "rogerai-edge-present/v2\nn_x\nnina\nmobile\nclassify\n\n00\n1700000000\n\n\n\n", string(p.canonical()),
		"an absent reading is an empty field, never a zero")
}

func TestOnlyAPhoneMayReportPhoneVitals(t *testing.T) {
	f, a, priv, id, certPEM := presenceFixture(t)
	h := PresenceHandler(f, a, time.Now)
	b := 50
	p := validPresence(id, certPEM)
	p.Kind, p.V, p.Vitals = "host", 2, &PhoneVitals{BatteryPct: &b}
	signPresence(&p, priv)
	require.Equal(t, http.StatusForbidden, doPresence(h, p).Code)
}
