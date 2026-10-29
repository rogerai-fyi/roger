package main

// The authority's TLS listener (edgeauthtls.go, ruled 2026-10-13): a real handshake a client checks
// against the Edge root, the leaf reissued at half its life or when the LAN addresses change, and the
// plain listener naming the TLS port and refusing the phone's paths once TLS is up.

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edgeauth"
	"rogerai.fm/roger/v7/internal/towercore/cert"
)

func TestTheAuthorityServesItsMuxOverTLSUnderTheEdgeRoot(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	const node = "n_21fe31dfa154a261626bf854046fd2271b7bed4b6abe45aa"
	now := time.Now()
	sc := &edgeServerCert{auth: local.Authority(), nodeID: node, ips: func() []string { return []string{"127.0.0.1"} },
		now: func() time.Time { return now }}

	mux := http.NewServeMux()
	mux.HandleFunc("/edge/phone/bands", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "over tls") })
	// Served exactly as startAuthorityTLS serves it (httptest's StartTLS would put its own cert in).
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	hs := &http.Server{Handler: mux}
	go func() { _ = hs.Serve(tls.NewListener(raw, edgeAuthorityTLSConfig(sc))) }()
	defer hs.Close()
	srv := struct{ URL string }{"https://" + raw.Addr().String()}

	pool := x509.NewCertPool()
	pool.AddCert(local.Authority().Root())
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	resp, err := client.Get(srv.URL + "/edge/phone/bands")
	require.NoError(t, err, "a client anchored ONLY at the Edge root completes the handshake")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, "over tls", string(body))
	leaf := resp.TLS.PeerCertificates[0]
	require.Len(t, resp.TLS.PeerCertificates, 1, "the chain is leaf -> root; the root is not sent")
	require.NoError(t, cert.VerifyServer(local.Authority().Root(), leaf, node, net.ParseIP("127.0.0.1"), now))

	// Another Edge's root refuses the handshake.
	other, _ := edgeauth.Designate(t.TempDir(), "barn")
	otherPool := x509.NewCertPool()
	otherPool.AddCert(other.Authority().Root())
	_, err = (&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: otherPool}}}).Get(srv.URL + "/edge/phone/bands")
	require.Error(t, err)

	// The same leaf until half its life, then a fresh one; and a fresh one when the addresses change.
	first, err := sc.get(nil)
	require.NoError(t, err)
	again, _ := sc.get(nil)
	require.Same(t, first, again)
	now = now.Add(cert.ServerCertTTL/2 + time.Minute)
	later, _ := sc.get(nil)
	require.NotSame(t, first, later)
	sc.ips = func() []string { return []string{"127.0.0.1", "192.168.1.69"} }
	moved, _ := sc.get(nil)
	require.NotSame(t, later, moved)
	require.Len(t, moved.Leaf.IPAddresses, 2)

	sc.ips = func() []string { return nil }
	sc.cur = nil
	_, err = sc.get(nil)
	require.Error(t, err, "no LAN address: no certificate, never one naming nothing")
}

func TestThePlainListenerNamesTLSAndRefusesThePhonesPaths(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "served "+r.URL.Path) })
	port := 0
	h := edgePlainAuthority(next, func() int { return port }, "n_auth")

	// Before TLS is up (no node identity, or the TLS bind failed): the enrolment paths pass with
	// nothing added, but a phone's turn is still refused. "Over TLS only" (RULED 2026-10-13) does not
	// lapse when there is no TLS listener; a band list in clear would show the fleet to the LAN.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/edge/present", nil))
	require.Equal(t, "served /edge/present", w.Body.String())
	require.Empty(t, w.Header().Get(edgeTLSHeader))
	for _, p := range []string{"/edge/phone/bands", "/edge/phone/infer", "/edge/phone/speech"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}")))
		require.Equal(t, http.StatusServiceUnavailable, w.Code, p)
		require.NotContains(t, w.Body.String(), "served "+p, "nothing reaches the phone handler")
		require.Contains(t, w.Body.String(), "only over TLS")
	}

	port = 39443
	for _, p := range []string{"/edge/enroll", "/edge/claim", "/edge/root", "/edge/present"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, p, nil))
		require.Equal(t, "served "+p, w.Body.String())
		require.Equal(t, "39443", w.Header().Get("Roger-Edge-TLS"))
		require.Equal(t, "n_auth", w.Header().Get("Roger-Edge-Authority"))
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/edge/phone/infer", strings.NewReader("{}")))
	require.Equal(t, http.StatusMisdirectedRequest, w.Code)
	require.Contains(t, w.Body.String(), "served over TLS on port 39443")
}
