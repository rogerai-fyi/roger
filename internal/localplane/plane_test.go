package localplane

// Contract: features/tower/standalone_consumer_plane.feature
//
// The consumer plane's authentication and discovery. Every request is signed with roger's own
// Ed25519 key; the plane maps that key to an admitted client by the one canonical rule
// (protocol.UserIDFromPubkey) admission recorded, and answers /discover with THIS Tower's own
// stations - to admitted clients only. Every authentication failure is one byte-identical 401.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

func standaloneState(t *testing.T) *tower.State {
	t.Helper()
	dir := t.TempDir()
	_, err := tower.Init(dir, tower.ModeStandalone)
	require.NoError(t, err)
	st, err := tower.Open(dir)
	require.NoError(t, err)
	return st
}

// admitClient generates a client key, admits its canonical id, and returns the private key.
func admitClient(t *testing.T, st *tower.State) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyHash := protocol.UserIDFromPubkey(hexPub(pub))
	inv, code, err := st.CreateInvitation(keyHash, time.Hour, 5)
	require.NoError(t, err)
	_, err = st.ConsumeInvitation(inv.ID, code, keyHash)
	require.NoError(t, err)
	return priv
}

func hexPub(pub ed25519.PublicKey) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, len(pub)*2)
	for i, b := range pub {
		out[i*2] = hexd[b>>4]
		out[i*2+1] = hexd[b&0xf]
	}
	return string(out)
}

// signedReq builds a request signed by priv, the way roger signs every request.
func signedReq(t *testing.T, priv ed25519.PrivateKey, method, path string, body []byte) *http.Request {
	t.Helper()
	pubHex, ts, sig := protocol.SignRequest(priv, method, path, body)
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set(protocol.HeaderPubkey, pubHex)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	return req
}

func TestDiscoverListsOwnStationsForAnAdmittedClient(t *testing.T) {
	st := standaloneState(t)
	priv := admitClient(t, st)
	_, err := st.AttachStation("st-1", "sk-1", []string{"llama-8b", "qwen"})
	require.NoError(t, err)
	_, err = st.AttachStation("st-2", "sk-2", []string{"mistral"})
	require.NoError(t, err)

	srv := New(st)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, signedReq(t, priv, http.MethodGet, "/discover", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Offers []localOffer `json:"offers"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Offers, 3, "one offer per station-model, sorted")
	models := map[string]bool{}
	for _, o := range resp.Offers {
		models[o.Model] = true
		require.True(t, o.Local, "every offer is marked local")
		require.True(t, o.FreeNow, "every local offer is free")
		require.Zero(t, o.PriceIn)
		require.Zero(t, o.PriceOut)
	}
	require.True(t, models["llama-8b"] && models["qwen"] && models["mistral"])
	// Nothing about a public market or another network leaks in.
	require.NotContains(t, rec.Body.String(), "account")
	require.NotContains(t, rec.Body.String(), "wallet")
}

// Every authentication failure is ONE byte-identical 401: an unsigned request, a bad
// signature, a valid signature from a never-admitted key, and a revoked client all get the
// same bytes - none reveals whether the key verified, whether a client exists, or any model.
func TestAllAuthFailuresAreOneUniform401(t *testing.T) {
	st := standaloneState(t)
	good := admitClient(t, st)
	revoked := admitClient(t, st)
	// Revoke the second client.
	revPub := revoked.Public().(ed25519.PublicKey)
	require.NoError(t, st.RevokeClient(protocol.UserIDFromPubkey(hexPub(revPub))))
	_, unadmittedPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	srv := New(st)
	bodyOf := func(req *http.Request) (int, string) {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// A sanity anchor: the good client succeeds, so the refusals below are real refusals.
	okCode, _ := bodyOf(signedReq(t, good, http.MethodGet, "/discover", nil))
	require.Equal(t, http.StatusOK, okCode)

	// Four distinct failure modes.
	unsigned := httptest.NewRequest(http.MethodGet, "/discover", nil)
	badSig := signedReq(t, good, http.MethodGet, "/discover", nil)
	badSig.Header.Set(protocol.HeaderSig, "00"+badSig.Header.Get(protocol.HeaderSig)[2:]) // corrupt one byte
	unadmitted := signedReq(t, unadmittedPriv, http.MethodGet, "/discover", nil)
	revokedReq := signedReq(t, revoked, http.MethodGet, "/discover", nil)

	var codes []int
	var bodies []string
	for _, req := range []*http.Request{unsigned, badSig, unadmitted, revokedReq} {
		c, b := bodyOf(req)
		codes = append(codes, c)
		bodies = append(bodies, b)
	}
	for i := range codes {
		require.Equal(t, http.StatusUnauthorized, codes[i], "failure %d must be a 401", i)
		require.Equal(t, bodies[0], bodies[i], "every 401 body must be byte-identical (no oracle)")
	}
}

func TestDiscoverRefusesNonGet(t *testing.T) {
	st := standaloneState(t)
	priv := admitClient(t, st)
	srv := New(st)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, signedReq(t, priv, http.MethodPost, "/discover", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
