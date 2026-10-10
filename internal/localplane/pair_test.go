package localplane

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/joinsas"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/tower"
)

// pairClient plays the joining side of features/join/approve.feature against a real plane.
type pairClient struct {
	priv  ed25519.PrivateKey
	eph   *ecdh.PrivateKey
	nonce string
	id    string
	start PairStartAnswer
}

func newPairClient(t *testing.T) *pairClient {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	require.NoError(t, err)
	return &pairClient{priv: priv, eph: eph, nonce: joinsas.NewNonce()}
}

func (c *pairClient) pub() string { return hexPub(c.priv.Public().(ed25519.PublicKey)) }

func (c *pairClient) post(srv *Server, path string, body any, addr string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := nonceReq(c.priv, http.MethodPost, path, b, protocol.NewNonce())
	if addr != "" {
		r.RemoteAddr = addr
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

func (c *pairClient) begin(t *testing.T, srv *Server, kind, name string, models []string, addr string) *httptest.ResponseRecorder {
	rec := c.post(srv, "/local/pair/start", PairStartBody{Kind: kind, Name: name, Models: models,
		ClientEph: hex.EncodeToString(c.eph.PublicKey().Bytes()), Commit: joinsas.Commit(c.nonce)}, addr)
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c.start))
		c.id = c.start.ID
	}
	return rec
}

func (c *pairClient) reveal(srv *Server, nonce string) *httptest.ResponseRecorder {
	return c.post(srv, "/local/pair/reveal", PairRevealBody{ID: c.id, ClientNonce: nonce}, "")
}

// phrase is what the client shows, computed from ITS view of the Tower's key.
func (c *pairClient) phrase(towerKey string) string {
	return joinsas.Phrase(joinsas.Transcript{
		ClientKey: c.pub(), TowerKey: towerKey,
		ClientEph: hex.EncodeToString(c.eph.PublicKey().Bytes()), TowerEph: c.start.TowerEph,
		ClientNonce: c.nonce, TowerNonce: c.start.TowerNonce,
	})
}

func (c *pairClient) wait(srv *Server) PairWaitAnswer {
	rec := c.post(srv, "/local/pair/wait", PairWaitBody{ID: c.id}, "")
	var a PairWaitAnswer
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	return a
}

func pairServer(t *testing.T) (*Server, *tower.State, string) {
	st := standaloneState(t)
	srv := New(st)
	srv.SetLabel("lab")
	srv.pairWait = 50 * time.Millisecond
	fp, err := st.JoinFingerprint()
	require.NoError(t, err)
	return srv, st, fp
}

func TestPairingTheOwnerSeesTheSamePhraseAndApproves(t *testing.T) {
	srv, st, fp := pairServer(t)
	c := newPairClient(t)
	require.Equal(t, http.StatusOK, c.begin(t, srv, "client", "laptop-x", nil, "").Code)
	require.Empty(t, srv.PairRequests(), "nothing is shown to the owner before the reveal")
	require.Equal(t, http.StatusOK, c.reveal(srv, c.nonce).Code)

	reqs := srv.PairRequests()
	require.Len(t, reqs, 1)
	require.Equal(t, "laptop-x", reqs[0].Name)
	require.Equal(t, c.phrase(fp), reqs[0].Phrase, "both screens show the same phrase")
	require.Equal(t, "pending", c.wait(srv).Status)
	kh := protocol.UserIDFromPubkey(c.pub())
	require.False(t, st.IsAdmitted(kh), "nothing is admitted until the owner approves")

	require.NoError(t, srv.ApprovePair(reqs[0].ID))
	a := c.wait(srv)
	require.Equal(t, "approved", a.Status)
	require.Equal(t, "lab", a.Answer.Tower)
	require.True(t, st.IsAdmitted(kh))
	require.Empty(t, srv.PairRequests())
}

// A party in the middle presents its own key: the client's phrase differs from the owner's.
func TestPairingAMiddleKeyMakesThePhrasesDiffer(t *testing.T) {
	srv, _, fp := pairServer(t)
	c := newPairClient(t)
	c.begin(t, srv, "client", "laptop-x", nil, "")
	c.reveal(srv, c.nonce)
	middle := hex.EncodeToString(make([]byte, 32))
	require.NotEqual(t, srv.PairRequests()[0].Phrase, c.phrase(middle))
	require.Equal(t, srv.PairRequests()[0].Phrase, c.phrase(fp))
}

func TestPairingARevealThatBreaksTheCommitmentIsRefused(t *testing.T) {
	srv, _, _ := pairServer(t)
	c := newPairClient(t)
	c.begin(t, srv, "client", "laptop-x", nil, "")
	require.Equal(t, http.StatusUnauthorized, c.reveal(srv, joinsas.NewNonce()).Code)
	require.Empty(t, srv.PairRequests(), "a broken commitment is never shown to the owner")
}

func TestPairingRefusedRequestAdmitsNothing(t *testing.T) {
	srv, st, _ := pairServer(t)
	c := newPairClient(t)
	c.begin(t, srv, "client", "laptop-x", nil, "")
	c.reveal(srv, c.nonce)
	require.NoError(t, srv.RefusePair(srv.PairRequests()[0].ID))
	require.Equal(t, "refused", c.wait(srv).Status)
	require.False(t, st.IsAdmitted(protocol.UserIDFromPubkey(c.pub())))
}

func TestPairingAStationIsAttachedWithItsModels(t *testing.T) {
	srv, st, _ := pairServer(t)
	c := newPairClient(t)
	c.begin(t, srv, "station", "kitchen-pi", []string{"qwen3-8b"}, "")
	c.reveal(srv, c.nonce)
	require.Equal(t, []string{"qwen3-8b"}, srv.PairRequests()[0].Models)
	require.NoError(t, srv.ApprovePair(srv.PairRequests()[0].ID))
	sts, err := st.Stations()
	require.NoError(t, err)
	require.Len(t, sts, 1)
	require.Equal(t, []string{"qwen3-8b"}, sts[0].Models)
}

func TestPairingRequestsExpire(t *testing.T) {
	srv, _, _ := pairServer(t)
	now := time.Now()
	srv.pairNow = func() time.Time { return now }
	c := newPairClient(t)
	c.begin(t, srv, "client", "laptop-x", nil, "")
	c.reveal(srv, c.nonce)
	now = now.Add(PairLifetime + time.Second)
	require.Empty(t, srv.PairRequests(), "an expired request disappears from the owner's screen")
	require.Equal(t, "expired", c.wait(srv).Status)
	require.Error(t, srv.ApprovePair("anything"))
}

func TestPairingOnePendingPerClient(t *testing.T) {
	srv, _, _ := pairServer(t)
	c := newPairClient(t)
	c.begin(t, srv, "client", "laptop-x", nil, "")
	c.reveal(srv, c.nonce)
	first := srv.PairRequests()[0]
	c.nonce = joinsas.NewNonce()
	c.begin(t, srv, "client", "laptop-x", nil, "")
	c.reveal(srv, c.nonce)
	reqs := srv.PairRequests()
	require.Len(t, reqs, 1, "the earlier request is replaced, not added")
	require.NotEqual(t, first.Phrase, reqs[0].Phrase, "the new one gets a new phrase")
}

func TestPairingCapsPendingRequests(t *testing.T) {
	srv, _, _ := pairServer(t)
	for i := 0; i < PairPendingCap; i++ {
		c := newPairClient(t)
		require.Equal(t, http.StatusOK, c.begin(t, srv, "client", "d", nil, "10.0.0."+string(rune('1'+i))+":5").Code)
	}
	c := newPairClient(t)
	rec := c.begin(t, srv, "client", "ninth", nil, "10.0.1.1:5")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "lab is busy with other join requests, try again in a minute")
}

func TestPairingIsRateLimitedPerAddress(t *testing.T) {
	srv, _, _ := pairServer(t)
	for i := 0; i < PairPerAddressPerMinute; i++ {
		c := newPairClient(t)
		require.Equal(t, http.StatusOK, c.begin(t, srv, "client", "d", nil, "10.0.0.9:5").Code)
	}
	c := newPairClient(t)
	rec := c.begin(t, srv, "client", "d", nil, "10.0.0.9:5")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.NotEmpty(t, rec.Header().Get("Retry-After"))
}

func TestPairingNeedsASignedRequest(t *testing.T) {
	srv, _, _ := pairServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/local/pair/start", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}
