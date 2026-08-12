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
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/relay"
	"rogerai.fm/roger/v5/internal/station"
	"rogerai.fm/roger/v5/internal/towercore/dispatch"
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
	go func() {
		done <- serveStationSplit(station.Executor{Station: s}, station.EdgeExecutor{Station: s}, station.EdgeExecutor{Station: s}.Outbox, ln, nil, &sb, stop)
	}()

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

// THE WHOLE EDGE PATH, end to end, with nothing stubbed but the model itself: an ordinary
// HTTP client presents a real Core-signed grant, reaches a Station THROUGH a real relay, and
// gets the model's own bytes back. The relay's tap holds neither the prompt nor the
// completion, and the consumer's acknowledgement reconciles against the Station's receipt.
//
// Contract: features/tower/edge_dispatch.feature.
func TestAConsumerIsServedThroughARelayThatSeesNothing(t *testing.T) {
	const name = "st-a.relay.example"
	now := time.Now()

	// A Station with a Core-issued certificate for its relay name.
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

	// Roger Core, authorizing without ever seeing the request.
	corePub, corePriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	reg := dispatch.NewWithStore(dispatch.Config{
		Network: "roger-public", Signer: corePriv, Lifetime: time.Minute,
		Now: func() time.Time { return now },
	}, nil)
	grant, err := reg.MintEdge(dispatch.EdgeTarget{
		TowerID: "tw-1", StationID: s.StationID, Model: "m", Modality: "text",
		RelayName: name, MaxIn: 4096, MaxOut: 4096, AssertionKey: s.AssertionPub(), ConsumerKey: edgeConsumerKey(),
	})
	require.NoError(t, err)

	// The model.
	const prompt = `{"prompt":"the secret question"}`
	const answer = `{"choices":[{"text":"the secret answer"}]}`
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		require.JSONEq(t, prompt, string(got))
		_, _ = w.Write([]byte(answer))
	}))
	defer model.Close()

	// The Station, terminating the consumer's TLS itself.
	cert, err := s.TLSCertificate()
	require.NoError(t, err)
	rawLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	stationAddr := rawLn.Addr().String()
	ln := tls.NewListener(rawLn, &tls.Config{
		Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12,
	})
	stop := make(chan struct{})
	done := make(chan error, 1)
	var sb bytes.Buffer
	edge := station.EdgeExecutor{
		Station: s, CoreKey: corePub, Network: "roger-public",
		Upstream: station.HTTPUpstream{URL: model.URL},
		Now:      func() time.Time { return now },
	}
	go func() {
		done <- serveStationSplit(station.Executor{Station: s}, edge, edge.Outbox, ln, nil, &sb, stop)
	}()

	// A Tower in front of it, with a tap on every byte it carries.
	var mu sync.Mutex
	var carried bytes.Buffer
	relayLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	relayAddr := relayLn.Addr().String()
	r := &relay.Relay{
		Router: staticRoute{name: name, addr: stationAddr}, PeekTimeout: 5 * time.Second,
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

	// An ORDINARY HTTP client. It knows the grant header and nothing else about Towers.
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
	var receipt string
	require.Eventually(t, func() bool {
		req, rerr := http.NewRequest(http.MethodPost, "https://"+name+"/v1/chat/completions",
			strings.NewReader(prompt))
		if rerr != nil {
			return false
		}
		req.Header.Set(station.GrantHeader, base64.StdEncoding.EncodeToString(grant.Signed))
		resp, gerr := client.Do(req)
		if gerr != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ = io.ReadAll(resp.Body)
		receipt = resp.Header.Get(station.ReceiptHeader)
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 25*time.Millisecond)

	// The consumer got the model's own bytes, and the evidence alongside.
	require.JSONEq(t, answer, string(body))
	require.NotEmpty(t, receipt, "a served attempt must come with something to settle on")

	// THE RELAY SAW NONE OF IT.
	client.CloseIdleConnections()
	mu.Lock()
	seen := carried.String()
	mu.Unlock()
	require.NotEmpty(t, seen, "the relay must actually have carried this session")
	require.NotContains(t, seen, "the secret question")
	require.NotContains(t, seen, "the secret answer")
	require.NotContains(t, seen, grant.AttemptID, "not even which attempt this was")
	require.NotContains(t, seen, "v1/chat/completions")

	// And the two ends reconcile: the receipt that rode back through the relay verifies
	// against the Station's real assertion key, carries the Station's own signed usage
	// claim, and agrees with the consumer's acknowledgement about the bytes.
	rawReceipt, err := base64.StdEncoding.DecodeString(receipt)
	require.NoError(t, err)
	rec, err := dispatch.ParseReceipt(rawReceipt, s.AssertionPub(), "roger-public",
		grant.AttemptID, s.StationID)
	require.NoError(t, err)
	require.Equal(t, int64(len(body)), rec.Usage.Out,
		"the Station's claim must be the bytes it actually returned")

	_, consumerPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	// The consumer observed LESS output than the Station claims - a short read, say.
	ack, err := dispatch.SignAck(consumerPriv, "roger-public", grant.AttemptID, body,
		dispatch.Usage{In: rec.Usage.In, Out: rec.Usage.Out - 2}, now, now)
	require.NoError(t, err)
	settled, err := dispatch.Reconcile(rec, &ack)
	require.NoError(t, err)
	require.True(t, settled.Corroborated)
	require.Equal(t, rec.Usage.Out-2, settled.Billable.Out,
		"the Station must not be paid on its own count alone")

	cancel()
	_ = relayLn.Close()
	<-relayDone
	close(stop)
	require.NoError(t, <-done)
}

// edgeConsumerKey is a valid consumer public key for a grant fixture. An edge grant is now
// issued to a consumer, so a target that named none would be refused.
func edgeConsumerKey() ed25519.PublicKey {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return pub
}
