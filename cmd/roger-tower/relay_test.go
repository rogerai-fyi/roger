package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"rogerai.fm/roger/v5/internal/tower"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRelayRoutesAreParsedFromIDEqualsHostPort(t *testing.T) {
	routes, err := parseRelayRoutes([]string{"st-a=127.0.0.1:9001", "st-b=[::1]:9002"})
	require.NoError(t, err)
	require.Equal(t, relayRoutes{"st-a": "127.0.0.1:9001", "st-b": "[::1]:9002"}, routes)
}

func TestAMalformedRelayStationSaysWhatItWanted(t *testing.T) {
	for _, bad := range []string{"st-a", "=127.0.0.1:1", "st-a=", "st-a=nowhere"} {
		_, err := parseRelayRoutes([]string{bad})
		require.Error(t, err, bad)
		require.Contains(t, err.Error(), "--relay-station")
	}
}

func TestTheStationIsTakenFromTheLeftmostLabel(t *testing.T) {
	routes := relayRoutes{"st-a": "127.0.0.1:9001"}
	up, ok := routes.Upstream("st-a.relay.example")
	require.True(t, ok)
	require.Equal(t, "127.0.0.1:9001", up)
}

// A name this Tower does not carry is REFUSED rather than guessed at. A relay that resolved
// unknown names would forward to wherever the name pointed - an open proxy under a Tower's
// identity and lease.
func TestANameThisTowerDoesNotCarryIsRefused(t *testing.T) {
	routes := relayRoutes{"st-a": "127.0.0.1:9001"}
	for _, name := range []string{"st-b.relay.example", "", "st-a", ".relay.example"} {
		_, ok := routes.Upstream(name)
		require.False(t, ok, name)
	}
}

func TestTheRelayNeedsAtLeastOneStationToRouteTo(t *testing.T) {
	var b bytes.Buffer
	err := run([]string{"serve", "--dir", t.TempDir(), "--relay", ":0"}, &b)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--relay-station")
}

func TestABadRelayStationStopsServeBeforeItStarts(t *testing.T) {
	var b bytes.Buffer
	err := run([]string{"serve", "--dir", t.TempDir(), "--relay-station", "oops"}, &b)
	require.Error(t, err)
	require.Contains(t, err.Error(), "--relay-station")
}

// THE POINT OF THE WHOLE PACKAGE: bytes cross the Tower and the Tower cannot read them. The
// consumer's TLS session runs to the Station, and what this process observes is a stream of
// ciphertext it holds no key for.
func TestTheTowerCarriesASessionItCannotRead(t *testing.T) {
	secret := "the completion nobody else may read"
	station := tlsEchoStation(t, secret)

	routes := relayRoutes{"st-a": station}
	stop := make(chan struct{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	var mu sync.Mutex
	var out bytes.Buffer
	wait := runRelayInBackground(addr, routes, &lockedWriter{mu: &mu, w: &out}, stop)

	var conn net.Conn
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return false
		}
		conn = c
		return true
	}, 5*time.Second, 20*time.Millisecond)

	tc := tls.Client(conn, &tls.Config{ServerName: "st-a.relay.example", InsecureSkipVerify: true})
	require.NoError(t, tc.Handshake())
	_, err = tc.Write([]byte("ping"))
	require.NoError(t, err)
	got := make([]byte, len(secret))
	_, err = io.ReadFull(tc, got)
	require.NoError(t, err)
	require.Equal(t, secret, string(got))
	require.NoError(t, tc.Close())

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(out.String(), "relay st-a.relay.example")
	}, 5*time.Second, 20*time.Millisecond)

	close(stop)
	wait()

	mu.Lock()
	logged := out.String()
	mu.Unlock()
	// Metadata is logged. Content is not, and could not be.
	require.Contains(t, logged, "cannot read what it relays")
	require.NotContains(t, logged, secret)
	require.NotContains(t, logged, "ping")
}

func TestTheRelayReportsAPortItCannotHave(t *testing.T) {
	var b bytes.Buffer
	err := runRelay("127.0.0.1:-1", relayRoutes{"st-a": "127.0.0.1:1"}, &b, make(chan struct{}))
	require.Error(t, err)
}

// A relay that dies must SAY so. A data plane that stopped silently is a Tower that looks
// healthy on its link while carrying nothing.
func TestARelayThatCannotStartIsReported(t *testing.T) {
	var mu sync.Mutex
	var b bytes.Buffer
	wait := runRelayInBackground("127.0.0.1:-1", relayRoutes{"st-a": "127.0.0.1:1"},
		&lockedWriter{mu: &mu, w: &b}, make(chan struct{}))
	wait()
	mu.Lock()
	defer mu.Unlock()
	require.Contains(t, b.String(), "the relay stopped")
}

// tlsEchoStation stands a real TLS server in for a Station and returns its address.
func tlsEchoStation(t *testing.T, reply string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "st-a.relay.example"},
		DNSNames:     []string{"st-a.relay.example"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				_, _ = c.Write([]byte(reply))
			}()
		}
	}()
	return ln.Addr().String()
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// A Station this Tower cannot reach is logged as a fault, with the metadata an operator needs
// to find it - and still no content, because there never was any to leak.
func TestAnUnreachableStationIsLoggedAsAFault(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	deadAddr := dead.Addr().String()
	require.NoError(t, dead.Close())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	var mu sync.Mutex
	var out bytes.Buffer
	stop := make(chan struct{})
	wait := runRelayInBackground(addr, relayRoutes{"st-a": deadAddr},
		&lockedWriter{mu: &mu, w: &out}, stop)

	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return false
		}
		defer c.Close()
		tc := tls.Client(c, &tls.Config{ServerName: "st-a.relay.example", InsecureSkipVerify: true})
		_ = tc.Handshake()
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(out.String(), "relay st-a.relay.example:")
	}, 5*time.Second, 50*time.Millisecond)

	close(stop)
	wait()
}

// A relay declared in the CONFIG must be honoured. This is the whole audit finding in one
// test: the rest of the schema - limits, listeners, metrics, payout - was decoded,
// validated, echoed back by `doctor`, and then ignored, and nothing failed when that was
// true. Something has to fail when it becomes true again.
func TestARelayDeclaredInTheConfigIsUsed(t *testing.T) {
	routes, err := relayRoutesFrom(map[string]string{"st-a": "127.0.0.1:9001"})
	require.NoError(t, err)
	require.Equal(t, relayRoutes{"st-a": "127.0.0.1:9001"}, routes)
}

// A config cannot express a route the command line would have refused: both go through the
// same parser, so there is no second, laxer way in.
func TestAConfigRouteIsValidatedExactlyAsAFlagIs(t *testing.T) {
	_, err := relayRoutesFrom(map[string]string{"st-a": "nowhere"})
	require.ErrorContains(t, err, "--relay-station")
}

func TestNoRoutesMeansNoRoutes(t *testing.T) {
	routes, err := relayRoutesFrom(nil)
	require.NoError(t, err)
	require.Empty(t, routes)
}

// Every startup mistake around the public endpoint is refused at the flag stage, by name,
// before the data directory is touched.
func TestRelayPublicRefusalsHappenBeforeAnythingElse(t *testing.T) {
	var b bytes.Buffer
	// Not host:port.
	err := run([]string{"serve", "--dir", t.TempDir(), "--relay", ":0",
		"--relay-station", "st-a=127.0.0.1:1", "--relay-public", "not-an-endpoint"}, &b)
	require.ErrorContains(t, err, "--relay-public")
	// Advertising a data plane nobody is serving.
	err = run([]string{"serve", "--dir", t.TempDir(),
		"--relay-station", "st-a=127.0.0.1:1", "--relay-public", "203.0.113.7:8443"}, &b)
	require.ErrorContains(t, err, "neither --relay nor --hub is serving one")
}

// A config-declared relay carries its public endpoint too - the file and the flags describe
// the same machine, so both must be able to say where consumers arrive.
func TestTheConfigCanDeclareThePublicEndpoint(t *testing.T) {
	c, err := tower.ParseConfig([]byte(minimalRelayYAML))
	require.NoError(t, err)
	require.Equal(t, "203.0.113.7:8443", c.Relay.Public)
}

const minimalRelayYAML = `apiVersion: tower.rogerai.fm/v1alpha1
kind: Tower
mode: standalone
relay:
  address: 127.0.0.1:8443
  public: 203.0.113.7:8443
  stations:
    st-a: 127.0.0.1:9001
`

// The whole config-merge path in cmdServe: a file declaring the relay, its public address
// and its stations, honoured without a single relay flag. The serve then fails at the link
// (a standalone Tower refuses), which is fine - what is being pinned is that the file was
// READ and MERGED, not that the network works.
func TestServeMergesTheRelayDeclarationFromTheConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tw")
	var b bytes.Buffer
	require.NoError(t, run([]string{"init", "--dir", dir, "--mode", "standalone"}, &b))
	cfgPath := filepath.Join(t.TempDir(), "tower.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(minimalRelayYAML), 0o644))

	b.Reset()
	err := cmdServe([]string{"--dir", dir, "--config", cfgPath}, &b)
	require.Error(t, err, "a standalone Tower refuses the joined link")
	require.Contains(t, err.Error(), "standalone",
		"it must get PAST the relay merge and fail at the link, not before")

	// And a config whose public endpoint has no address to serve is refused by the same
	// rule as the flags - one rule, wherever the declaration came from.
	bad := strings.Replace(minimalRelayYAML, "  address: 127.0.0.1:8443\n", "", 1)
	require.NoError(t, os.WriteFile(cfgPath, []byte(bad), 0o644))
	err = cmdServe([]string{"--dir", dir, "--config", cfgPath}, &b)
	require.Error(t, err)
}
