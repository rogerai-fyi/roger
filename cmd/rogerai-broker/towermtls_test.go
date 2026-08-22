package main

// towermtls_test.go proves the mutual-TLS channel binding: a Tower connecting over TLS with a
// client certificate has that certificate verified against its admitted identity, on top of
// the signed-request auth. Verify-if-presented, so a plain-HTTP Tower still authenticates by
// signature alone.
//
// Contract: features/tower/job_and_settlement.feature (outer channel authentication).

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/towercore/admit"
	"rogerai.fm/roger/v6/internal/towercore/link"
)

// mtlsServer stands up the real tower routes over TLS, requesting (but not itself requiring)
// a client certificate - the broker verifies it in towerCaller. Returns the server and a
// function that builds a client presenting a given leaf.
func mtlsServer(t *testing.T, b *broker) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	b.registerTowerRoutes(mux)
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{ClientAuth: tls.RequestClientCert, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// issueTowerClientCert issues a client certificate for a Tower from the broker's CA and gives
// it a matching key.
func issueTowerClientCert(t *testing.T, b *broker, towerID string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leaf, err := b.tower.ca.Issue(towerID, &key.PublicKey)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: key, Leaf: leaf}
}

// callOverMTLS makes lt's signed request while presenting clientCert on the TLS connection.
func callOverMTLS(t *testing.T, srv *httptest.Server, lt linkTower, clientCert *tls.Certificate, path string, body []byte) int {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tlsCfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	if clientCert != nil {
		tlsCfg.Certificates = []tls.Certificate{*clientCert}
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	defer client.CloseIdleConnections()

	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(string(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	pub, ts, sig := protocolSignForTest(lt, path, body)
	req.Header.Set("X-Roger-Pubkey", pub)
	req.Header.Set("X-Roger-TS", ts)
	req.Header.Set("X-Roger-Sig", sig)
	resp, err := client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	return resp.StatusCode
}

// A Tower presenting the RIGHT client certificate is accepted - the channel is bound to its
// identity in addition to the signature.
func TestAMatchingClientCertificateIsAccepted(t *testing.T) {
	b := towerTestBrokerNoServer(t)
	srv := mtlsServer(t, b)
	lt := enrolledTower(t, b, "owner-1")

	cert := issueTowerClientCert(t, b, lt.id)
	body := renewChallengeBody(t, lt.id)
	code := callOverMTLS(t, srv, lt, &cert, "/tower/renew/challenge", body)
	require.Equal(t, http.StatusOK, code, "a matching client cert binds the channel")
}

// A Tower presenting a certificate for a DIFFERENT Tower is refused, even though its signature
// is valid - a stolen signature replayed over somebody else's channel does not authenticate.
func TestAClientCertificateForAnotherTowerIsRefused(t *testing.T) {
	b := towerTestBrokerNoServer(t)
	srv := mtlsServer(t, b)
	lt := enrolledTower(t, b, "owner-1")
	other := enrolledTower(t, b, "owner-2")

	wrongCert := issueTowerClientCert(t, b, other.id) // cert names the wrong Tower
	body := renewChallengeBody(t, lt.id)
	code := callOverMTLS(t, srv, lt, &wrongCert, "/tower/renew/challenge", body)
	require.Equal(t, http.StatusForbidden, code, "the channel cert must name the signing Tower")
}

// No client certificate at all falls back to signature-only auth - verify-if-presented, so a
// Tower or harness that does not present a cert still works.
func TestNoClientCertificateFallsBackToSignatureAuth(t *testing.T) {
	b := towerTestBrokerNoServer(t)
	srv := mtlsServer(t, b)
	lt := enrolledTower(t, b, "owner-1")

	body := renewChallengeBody(t, lt.id)
	code := callOverMTLS(t, srv, lt, nil, "/tower/renew/challenge", body)
	require.Equal(t, http.StatusOK, code, "no cert presented means signature-only, as before")
}

// A REVOKED client certificate is refused at the channel too - the handshake-time complement
// to the serial check.
func TestARevokedClientCertificateIsRefusedAtTheChannel(t *testing.T) {
	b := towerTestBrokerNoServer(t)
	srv := mtlsServer(t, b)
	lt := enrolledTower(t, b, "owner-1")
	cert := issueTowerClientCert(t, b, lt.id)
	require.NoError(t, b.tower.ca.Revoke(cert.Leaf.SerialNumber))

	body := renewChallengeBody(t, lt.id)
	code := callOverMTLS(t, srv, lt, &cert, "/tower/renew/challenge", body)
	require.Equal(t, http.StatusForbidden, code, "a revoked certificate is refused at the channel")
}

func renewChallengeBody(t *testing.T, towerID string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"tower_id": towerID})
	require.NoError(t, err)
	return body
}

func protocolSignForTest(lt linkTower, path string, body []byte) (pub, ts, sig string) {
	p, t, s := protocol.SignRequest(lt.priv, http.MethodPost, path, body)
	return p, itoa64(t), s
}

var _ = link.PublicNetwork
var _ = admit.StateActive
