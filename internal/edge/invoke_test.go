package edge

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

type invokeRig struct {
	face    *httptest.Server
	member  tls.Certificate
	authErr error // what the injected Authorize returns
	ran     bool  // whether the injected Invoke ran
}

func newInvokeRig(t *testing.T) *invokeRig {
	t.Helper()
	authority, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	verify, err := cert.NewVerifier(authority.Root(), nil)
	require.NoError(t, err)
	issue := func(name string) tls.Certificate {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		leaf, err := authority.Issue(name, pub)
		require.NoError(t, err)
		return tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: priv}
	}
	serveCert := issue("n_callee")
	_, servePriv, _ := ed25519.GenerateKey(rand.Reader)

	rig := &invokeRig{member: issue("n_caller")}
	srv := NewServer(Describe{NodeID: "n_callee", Account: "acct-1", Kind: "host"}, serveCert)
	srv.SetServing(Serving{
		Trust: verify, Member: func(string) bool { return true },
		NodeID: "n_callee", Key: servePriv, Now: time.Now,
		Authorize: func(g store.Grant, callerID string, need Capability) error { return rig.authErr },
		Invoke: func(callerID string, req InvokeRequest) (InvokeResult, error) {
			rig.ran = true
			return InvokeResult{Output: json.RawMessage(`{"ok":true}`)}, nil
		},
	})
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.TLS = srv.ListenerTLS()
	ts.StartTLS()
	t.Cleanup(ts.Close)
	rig.face = ts
	return rig
}

func (r *invokeRig) post(t *testing.T, req InvokeRequest) *http.Response {
	t.Helper()
	body, _ := json.Marshal(req)
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{r.member}}
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}
	hr, _ := http.NewRequest(http.MethodPost, r.face.URL+InvokePath, bytes.NewReader(body))
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("X-Roger-Request", "inv-1")
	resp, err := c.Do(hr)
	require.NoError(t, err)
	return resp
}

func freshInvoke() InvokeRequest {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	return InvokeRequest{Grant: store.Grant{ID: "g", Owner: "acct-1", MaxHops: 0},
		Capability: "operate", Task: json.RawMessage(`{"prompt":"hi"}`),
		Nonce: hex.EncodeToString(nonce), TS: time.Now().Unix()}
}

func TestInvokeRunsWhenAuthorized(t *testing.T) {
	rig := newInvokeRig(t)
	resp := rig.post(t, freshInvoke())
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, rig.ran, "an authorized invoke runs the task")
	require.NotEmpty(t, resp.Header.Get("X-RogerAI-Receipt"), "a receipt is issued")
}

func TestInvokeRefusesAndReceiptsEveryGuard(t *testing.T) {
	// Stale.
	t.Run("stale", func(t *testing.T) {
		rig := newInvokeRig(t)
		req := freshInvoke()
		req.TS = time.Now().Add(-30 * time.Minute).Unix()
		resp := rig.post(t, req)
		defer resp.Body.Close()
		require.NotEqual(t, http.StatusOK, resp.StatusCode)
		require.False(t, rig.ran)
		require.NotEmpty(t, resp.Header.Get("X-RogerAI-Receipt"), "a refusal is receipted")
	})
	// Replay: same nonce twice.
	t.Run("replay", func(t *testing.T) {
		rig := newInvokeRig(t)
		req := freshInvoke()
		r1 := rig.post(t, req)
		r1.Body.Close()
		require.Equal(t, http.StatusOK, r1.StatusCode)
		r2 := rig.post(t, req) // same nonce
		defer r2.Body.Close()
		require.Equal(t, http.StatusForbidden, r2.StatusCode, "a replayed nonce is refused")
	})
	// Loop: this node already in the chain.
	t.Run("loop", func(t *testing.T) {
		rig := newInvokeRig(t)
		req := freshInvoke()
		req.Origin = []string{"n_callee"} // the callee itself
		resp := rig.post(t, req)
		defer resp.Body.Close()
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		require.False(t, rig.ran)
	})
	// Hop limit: more hops than the grant allows (MaxHops 0).
	t.Run("hop limit", func(t *testing.T) {
		rig := newInvokeRig(t)
		req := freshInvoke()
		req.Origin = []string{"n_other"} // one hop, grant allows 0
		resp := rig.post(t, req)
		defer resp.Body.Close()
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		require.False(t, rig.ran)
	})
	// Authorization refuses.
	t.Run("authorize refuses", func(t *testing.T) {
		rig := newInvokeRig(t)
		rig.authErr = fmt.Errorf("that grant does not address this node")
		resp := rig.post(t, freshInvoke())
		defer resp.Body.Close()
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		require.False(t, rig.ran)
		require.NotEmpty(t, resp.Header.Get("X-RogerAI-Receipt"))
	})
}

func TestInvokeRefusesANonMemberCaller(t *testing.T) {
	rig := newInvokeRig(t)
	// dial with NO client cert.
	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}
	body, _ := json.Marshal(freshInvoke())
	hr, _ := http.NewRequest(http.MethodPost, rig.face.URL+InvokePath, bytes.NewReader(body))
	resp, err := c.Do(hr)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.False(t, rig.ran)
}
