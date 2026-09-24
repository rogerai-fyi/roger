package edge

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// A non-streamed reply that OVERFLOWS the body cap did not arrive complete, so its receipt must be
// voided rather than signed as a billable turn. The read cap+1 detection is what catches it (audit
// 2026-09-24).
func TestNonStreamInferVoidsReceiptOnOversizedBody(t *testing.T) {
	authority, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	verify, err := cert.NewVerifier(authority.Root(), nil)
	require.NoError(t, err)

	issue := func(name string) (tls.Certificate, ed25519.PrivateKey) {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		leaf, err := authority.Issue(name, pub)
		require.NoError(t, err)
		return tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: priv}, priv
	}

	// An upstream that returns a NON-stream body larger than the cap.
	huge := strings.Repeat("x", inferBodyCap+1024)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"`+huge+`"}}]}`)
	}))
	defer upstream.Close()

	serveCert, serveKey := issue("n_server")
	memberCert, _ := issue("n_member")

	srv := NewServer(Describe{NodeID: "n_server", Account: "acct-1", Kind: "host"}, serveCert)
	srv.SetServing(Serving{
		Trust:    verify,
		Member:   func(id string) bool { return true },
		Upstream: func(string) (string, string, string, bool) { return upstream.URL, "", "serve", true },
		NodeID:   "n_server",
		Key:      serveKey,
	})

	face := httptest.NewUnstartedServer(srv.Handler())
	face.TLS = srv.ListenerTLS()
	face.StartTLS()
	defer face.Close()

	cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12,
		Certificates: []tls.Certificate{memberCert}}
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}
	req, _ := http.NewRequest(http.MethodPost, face.URL+InferPath, strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	rec, err := protocol.DecodeReceipt(resp.Header.Get("X-RogerAI-Receipt"))
	require.NoError(t, err)
	require.NotEmpty(t, rec.VoidReason, "an oversized reply must void the receipt, not sign it as complete")
}
