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
