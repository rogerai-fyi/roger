package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/store"
)

// A BAND MUTATION MUST PROVE POSSESSION OF THE KEY, NOT MERELY NAME IT.
//
// Found by the pre-push audit. requireOwner (auth.go) resolves an owner from the
// X-Roger-Pubkey header and NEVER verifies a signature, despite its own doc calling it "the
// signed pubkey". Both band mutations - DELETE (revoke) and the new PATCH (move) - are
// gated on it alone.
//
// A public key is PUBLIC. Treating it as a bearer credential means anyone who learns an
// owner's pubkey can burn their band's code or repoint it at a model they control. The
// pubkey is not on /discover or /market today, so this is not trivially harvestable - but
// "the attacker has to learn a public value first" is not an access control.
//
// The client has been signing these requests all along (internal/client/rc.go RevokeBand
// and MoveBand both go through signedDo), so the signature is already on the wire and the
// broker was simply ignoring it. Verifying it breaks no caller: it closes the gap between
// what the design documents and what the code enforces.
//
// Reads are deliberately NOT affected. requireOwnerRead still accepts a browser session
// cookie, because a browser holds no Ed25519 key - see band_web_auth_test.go.
//
// Spec: features/sharing/band_management.feature - "A band can only be moved by the owner
// who holds it".

// testOwnerKeys remembers the private key behind a test owner's pubkey.
//
// The shared band fixtures build requests from a pubkey STRING and never had the key, which
// is why every one of them was unsigned - and why the handler's missing signature check
// went unnoticed for so long: the whole suite was exercising the hole rather than the
// contract. Registering the key lets those builders produce genuinely signed requests
// without changing brokerWithOwner's signature, which has 56 callers.
//
// An UNKNOWN pubkey is deliberately left unsigned, so a test that means to send an
// unauthenticated request (like the two above) still can.
var testOwnerKeys sync.Map // pubkey hex -> ed25519.PrivateKey

func rememberTestOwnerKey(pubkey string, priv ed25519.PrivateKey) {
	testOwnerKeys.Store(pubkey, priv)
}

// signAsTestOwner signs r as the owner behind pubkey, when that key is known.
func signAsTestOwner(r *http.Request, pubkey string, body []byte) {
	v, ok := testOwnerKeys.Load(pubkey)
	if !ok {
		return
	}
	pub, ts, sig := protocol.SignRequest(v.(ed25519.PrivateKey), r.Method, r.URL.Path, body)
	r.Header.Set(protocol.HeaderPubkey, pub)
	r.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	r.Header.Set(protocol.HeaderSig, sig)
}

// bandOwnerWithKey is brokerWithOwner, except it KEEPS the owner's private key so a test
// can produce a genuinely signed request. The shared fixture throws the key away.
func bandOwnerWithKey(t *testing.T) (*broker, store.Owner, ed25519.PrivateKey) {
	t.Helper()
	mem := store.NewMem()
	_, bpriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	b := &broker{db: mem, priv: bpriv, pubOfUser: map[string]string{}}

	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	o := store.Owner{GitHubID: 9, Login: "bandowner", Pubkey: hex.EncodeToString(pub), Email: "b@x.com"}
	require.NoError(t, mem.BindOwner(o))
	require.NoError(t, mem.CreateBand(store.Band{
		ID: "band_1", Owner: o.Pubkey, CodeHash: "hash_1",
		CodeDisplay: "145.225 MHz · ••••-••••", NodeID: "roggentoo-model-a", CreatedAt: 1,
	}))
	return b, o, priv
}

func TestMovingABandNeedsASignatureNotJustTheOwnersPubkey(t *testing.T) {
	b, o, _ := bandOwnerWithKey(t)

	body := []byte(`{"node_id":"attacker-model-b"}`)
	r := httptest.NewRequest(http.MethodPatch, "/bands/band_1", bytes.NewReader(body))
	// Everything an attacker can obtain: the owner's PUBLIC key. No signature, no timestamp.
	r.Header.Set(protocol.HeaderPubkey, o.Pubkey)
	w := httptest.NewRecorder()
	b.bandsByID(w, r)

	require.NotEqual(t, http.StatusOK, w.Code,
		"naming a public key must not be enough to move somebody's band (got %s)", w.Body.String())

	// And the band must be exactly where it was - a refusal that still moved it is no refusal.
	got, ok, err := b.db.BandByNode("roggentoo-model-a")
	require.NoError(t, err)
	require.True(t, ok, "the band must still be on its original node")
	require.Equal(t, "band_1", got.ID)
	_, ok, err = b.db.BandByNode("attacker-model-b")
	require.NoError(t, err)
	require.False(t, ok, "the attacker's node must not have acquired the band")
}

func TestRevokingABandNeedsASignatureNotJustTheOwnersPubkey(t *testing.T) {
	b, o, _ := bandOwnerWithKey(t)

	r := httptest.NewRequest(http.MethodDelete, "/bands/band_1", nil)
	r.Header.Set(protocol.HeaderPubkey, o.Pubkey)
	w := httptest.NewRecorder()
	b.bandsByID(w, r)

	require.NotEqual(t, http.StatusOK, w.Code,
		"naming a public key must not be enough to burn somebody's band code (got %s)", w.Body.String())

	// Revocation is irreversible - the code can never be shown again - so an unauthorised
	// revoke is a permanent denial of service against the owner and everyone tuned in.
	got, ok, err := b.db.BandByCodeHash("hash_1")
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, got.Revoked, "an unsigned request must not have burnt the code")
}

// The other half: a properly signed request must still work. This is what proves the fix is
// a verification gap being closed and not a working path being broken - the CLI has been
// signing all along.
func TestAProperlySignedMoveStillSucceeds(t *testing.T) {
	b, o, priv := bandOwnerWithKey(t)

	body := []byte(`{"node_id":"roggentoo-model-b"}`)
	r := httptest.NewRequest(http.MethodPatch, "/bands/band_1", bytes.NewReader(body))
	pub, ts, sig := protocol.SignRequest(priv, http.MethodPatch, "/bands/band_1", body)
	r.Header.Set(protocol.HeaderPubkey, pub)
	r.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	r.Header.Set(protocol.HeaderSig, sig)

	w := httptest.NewRecorder()
	b.bandsByID(w, r)
	require.Equal(t, http.StatusOK, w.Code, "a signed move must succeed: %s", w.Body.String())

	got, ok, err := b.db.BandByNode("roggentoo-model-b")
	require.NoError(t, err)
	require.True(t, ok, "the band must be at the destination")
	require.Equal(t, "band_1", got.ID)
	require.Equal(t, o.Pubkey, got.Owner, "the move must not change who holds the band")
}

// A signature that verifies but belongs to SOMEBODY ELSE must not move the band either.
// Possession of a key proves who you are; it does not make you the owner of this band.
func TestAValidSignatureFromAnotherKeyCannotMoveTheBand(t *testing.T) {
	b, _, _ := bandOwnerWithKey(t)
	_, attacker, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	body := []byte(`{"node_id":"attacker-model-b"}`)
	r := httptest.NewRequest(http.MethodPatch, "/bands/band_1", bytes.NewReader(body))
	pub, ts, sig := protocol.SignRequest(attacker, http.MethodPatch, "/bands/band_1", body)
	r.Header.Set(protocol.HeaderPubkey, pub)
	r.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	r.Header.Set(protocol.HeaderSig, sig)

	w := httptest.NewRecorder()
	b.bandsByID(w, r)
	require.NotEqual(t, http.StatusOK, w.Code, "another owner's key must not move this band")

	got, ok, err := b.db.BandByNode("roggentoo-model-a")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "band_1", got.ID, "the band must not have moved")
}
