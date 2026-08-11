package main

// edge_test.go is the whole edge path in one test: a consumer speaks HTTPS to a Station
// THROUGH a relay, and the relay holds nothing but ciphertext.
//
// It exists because the two halves are convincing separately and only meaningful together.
// internal/relay proves a relay cannot read what it splices; internal/station proves a
// Station can terminate a session. Neither proves the key stayed on the Station once the
// pieces are assembled, and that is the entire claim being made to a customer.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/relay"
	"rogerai.fm/roger/v5/internal/station"
)

func TestAConsumerReachesTheStationThroughARelayThatCannotRead(t *testing.T) {
	const name = "st-a.relay.example"

	dir := initDir(t)
	var b bytes.Buffer
	require.NoError(t, run([]string{"csr", "--dir", dir, "--name", name}, &b))
	chain := certForStationKey(t, dir)
	certPath := filepath.Join(t.TempDir(), "chain.pem")
	require.NoError(t, os.WriteFile(certPath, chain, 0o644))
	b.Reset()
	require.NoError(t, run([]string{"install-cert", "--dir", dir, "--cert", certPath}, &b))

	s, err := station.Open(dir)
	require.NoError(t, err)
	cert, err := s.TLSCertificate()
	require.NoError(t, err)

	// The Station, terminating the CONSUMER's session itself.
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	stationAddr := raw.Addr().String()
	ln := tls.NewListener(raw, &tls.Config{
		Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12,
	})
	stop := make(chan struct{})
	done := make(chan error, 1)
	var sb bytes.Buffer
	go func() { done <- serveStation(station.Executor{Station: s}, ln, &sb, stop) }()

	// A relay in front of it, with a tap on every byte it carries.
	var mu sync.Mutex
	var carried bytes.Buffer
	relayLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	relayAddr := relayLn.Addr().String()
	r := &relay.Relay{
		Router:      staticRoute{name: name, addr: stationAddr},
		PeekTimeout: 5 * time.Second,
		IdleTimeout: 30 * time.Second,
		Dial: func(_ context.Context, addr string) (net.Conn, error) {
			c, derr := net.Dial("tcp", addr)
			if derr != nil {
				return nil, derr
			}
			return &tappedConn{Conn: c, mu: &mu, tap: &carried}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	relayDone := make(chan struct{})
	go func() { defer close(relayDone); _ = r.Serve(ctx, relayLn) }()

	// The consumer trusts the Station's issuer and dials the RELAY. It never learns the
	// Station's address, which is the reachability a Tower is providing.
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(chain))
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(dctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(dctx, "tcp", relayAddr)
		},
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: name},
	}}
	defer client.CloseIdleConnections()

	var body []byte
	require.Eventually(t, func() bool {
		resp, gerr := client.Get("https://" + name + "/id")
		if gerr != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ = io.ReadAll(resp.Body)
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 25*time.Millisecond)

	// The consumer got the real answer, verified against the real certificate.
	require.Contains(t, string(body), s.StationID)

	// AND THE RELAY SAW NONE OF IT. The Station id is in the response the consumer read; it
	// is nowhere in the bytes that crossed the relay, because those are ciphertext under a
	// key that never left the Station.
	client.CloseIdleConnections()
	mu.Lock()
	seen := carried.Bytes()
	mu.Unlock()
	require.NotEmpty(t, seen, "the relay must actually have carried this session")
	require.NotContains(t, string(seen), s.StationID)
	require.NotContains(t, string(seen), "/id")
	require.NotContains(t, string(seen), "station_id")
	// Nor is the private key anywhere near the wire.
	keyPEM, err := os.ReadFile(s.TLSKeyPath())
	require.NoError(t, err)
	block, _ := pem.Decode(keyPEM)
	require.NotNil(t, block)
	require.NotContains(t, string(seen), string(block.Bytes))

	cancel()
	_ = relayLn.Close()
	<-relayDone
	close(stop)
	require.NoError(t, <-done)
}

type staticRoute struct{ name, addr string }

func (s staticRoute) Upstream(name string) (string, bool) {
	if name != s.name {
		return "", false
	}
	return s.addr, true
}

// tappedConn records everything the relay sends and receives on the Station side - standing
// in for a dishonest operator capturing its own traffic.
type tappedConn struct {
	net.Conn
	mu  *sync.Mutex
	tap *bytes.Buffer
}

func (c *tappedConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.tap.Write(p)
	c.mu.Unlock()
	return c.Conn.Write(p)
}

func (c *tappedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.mu.Lock()
		c.tap.Write(p[:n])
		c.mu.Unlock()
	}
	return n, err
}
