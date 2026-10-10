package localplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/joincode"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

func joinBody(t *testing.T, st *tower.State, priv ed25519.PrivateKey, code, nonce, kind, name string, models []string) []byte {
	t.Helper()
	fp, err := st.JoinFingerprint()
	require.NoError(t, err)
	pub := hexPub(priv.Public().(ed25519.PublicKey))
	b, _ := json.Marshal(JoinRequestBody{Kind: kind, Proof: joincode.Tower.Proof(code, nonce, pub, fp), Name: name, Models: models})
	return b
}

var (
	nonce_n1 = protocol.NewNonce()
	nonce_n2 = protocol.NewNonce()
	nonce_n3 = protocol.NewNonce()
)

func doJoin(srv *Server, priv ed25519.PrivateKey, body []byte, nonce string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, nonceReq(priv, http.MethodPost, "/local/join", body, nonce))
	return rec
}

// features/join/invite.feature: a client joins with one pasted invite; the Tower admits the
// presenting key on the code, and the key then passes the existing admission check.
func TestLocalJoinAdmitsTheKeyThatProvesTheCode(t *testing.T) {
	st := standaloneState(t)
	srv := New(st)
	_, code, err := st.CreateJoinInvitation(tower.JoinClient, "", nil, 15*time.Minute)
	require.NoError(t, err)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	rec := doJoin(srv, priv, joinBody(t, st, priv, code, nonce_n1, "client", "laptop-x", nil), nonce_n1)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got JoinAnswer
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "client", got.Joined)
	require.True(t, st.IsAdmitted(protocol.UserIDFromPubkey(hexPub(priv.Public().(ed25519.PublicKey)))))
}

// Every refusal is the uniform 401: a wrong code, a replay, an unsigned request.
func TestLocalJoinRefusalsAreTheUniform401(t *testing.T) {
	st := standaloneState(t)
	srv := New(st)
	_, code, _ := st.CreateJoinInvitation(tower.JoinClient, "", nil, 15*time.Minute)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	wrong := doJoin(srv, priv, joinBody(t, st, priv, joincode.Tower.NewCode(), nonce_n1, "client", "", nil), nonce_n1)
	require.Equal(t, http.StatusUnauthorized, wrong.Code)
	require.JSONEq(t, `{"error":"unauthorized"}`, wrong.Body.String())

	body := joinBody(t, st, priv, code, nonce_n2, "client", "", nil)
	require.Equal(t, http.StatusOK, doJoin(srv, priv, body, nonce_n2).Code)
	replay := doJoin(srv, priv, body, nonce_n2)
	require.Equal(t, http.StatusUnauthorized, replay.Code, "a replayed join is refused")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/local/join", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	get := httptest.NewRecorder()
	srv.Handler().ServeHTTP(get, nonceReq(priv, http.MethodGet, "/local/join", nil, nonce_n3))
	require.Equal(t, http.StatusMethodNotAllowed, get.Code)
}

// A right code for the wrong kind says which kind it is, and does not spend the invite.
func TestLocalJoinNamesTheWrongKind(t *testing.T) {
	st := standaloneState(t)
	srv := New(st)
	_, code, _ := st.CreateJoinInvitation(tower.JoinStation, "", nil, 15*time.Minute)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	rec := doJoin(srv, priv, joinBody(t, st, priv, code, nonce_n1, "client", "", nil), nonce_n1)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "this invite is for a station, not a client")
	ok := doJoin(srv, priv, joinBody(t, st, priv, code, nonce_n2, "station", "kitchen-pi", []string{"qwen3-8b"}), nonce_n2)
	require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
	var got JoinAnswer
	require.NoError(t, json.Unmarshal(ok.Body.Bytes(), &got))
	require.Equal(t, "station", got.Joined)
	require.Equal(t, []string{"qwen3-8b"}, got.Models)
}

// The replay guard alone refuses a replay: an already-admitted client's join answers without
// spending anything, so only the nonce record can refuse the same request a second time.
func TestLocalJoinReplayIsRefusedByTheNonceRecord(t *testing.T) {
	st := standaloneState(t)
	srv := New(st)
	_, c1, _ := st.CreateJoinInvitation(tower.JoinClient, "", nil, 15*time.Minute)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	n := protocol.NewNonce()
	require.Equal(t, http.StatusOK, doJoin(srv, priv, joinBody(t, st, priv, c1, n, "client", "", nil), n).Code)
	_, c2, _ := st.CreateJoinInvitation(tower.JoinClient, "", nil, 15*time.Minute)
	n2 := protocol.NewNonce()
	body := joinBody(t, st, priv, c2, n2, "client", "", nil)
	first := doJoin(srv, priv, body, n2)
	require.Equal(t, http.StatusOK, first.Code)
	require.Contains(t, first.Body.String(), `"already_member":true`)
	require.Equal(t, http.StatusUnauthorized, doJoin(srv, priv, body, n2).Code, "the replay is refused")
}
