// Package towerpin is how a client knows it is talking to its own Tower
// (features/join/tower_identity.feature): the Tower serves TLS with a certificate self-signed by
// its own tls.key, and a client trusts exactly the key whose SHA-256 it pinned from an invite or an
// approved pairing. No CA, hostname or expiry check takes the pin's place.
//
// For one release a Tower also answers plain http on the same port (Sniff), so a client that has
// not joined by invite yet keeps working, marked unverified.
//
// It imports only the standard library, so the Core-free serve path may use it.
package towerpin

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrPinMismatch is a Tower whose key is not the one pinned.
var ErrPinMismatch = errors.New("the Tower's key is not the one you joined")

// Fingerprint is the pin of a Tower key: SHA-256 of the raw Ed25519 public key, 64 hex.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// Parse reads a full fingerprint, with or without "sha256:". A short one is never a pin.
func Parse(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) != 64 || strings.Trim(s, "0123456789abcdef") != "" {
		return "", false
	}
	return s, true
}

// Short is the read-aloud form: the first 16 hex characters in four groups. Reassurance only.
func Short(fp string) string {
	if len(fp) < 16 {
		return fp
	}
	return fp[:4] + "-" + fp[4:8] + "-" + fp[8:12] + "-" + fp[12:16]
}

// ServerTLS is the Tower's TLS configuration: one certificate, self-signed by the Tower's key.
func ServerTLS(priv ed25519.PrivateKey) (*tls.Config, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "roger tower"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(100 * 365 * 24 * time.Hour), // trust is the pin, not the date
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, priv.Public(), priv)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv}},
	}, nil
}

// ClientTLS trusts exactly the Tower key that hashes to pin.
func ClientTLS(pin string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// The chain is not checked against any CA: VerifyConnection checks the pin instead.
		InsecureSkipVerify: true, //nolint:gosec // pinned below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return ErrPinMismatch
			}
			pub, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
			if !ok || Fingerprint(pub) != pin {
				return ErrPinMismatch
			}
			return nil
		},
	}
}

// Client is an HTTP client pinned to one Tower key. It never follows a redirect and never falls
// back to plain http.
func Client(pin string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		Transport:     &http.Transport{TLSClientConfig: ClientTLS(pin), ForceAttemptHTTP2: false},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Sniff serves TLS and plain http on one listener: a connection that opens with a TLS handshake
// record (0x16) is served over TLS with cfg, anything else as plain http. The first byte is read
// off the accept path, under a deadline, so a slow or silent caller never blocks other accepts.
func Sniff(ln net.Listener, cfg *tls.Config) net.Listener {
	s := &sniffer{Listener: ln, cfg: cfg, conns: make(chan net.Conn), done: make(chan struct{})}
	go s.loop()
	return s
}

// sniffDeadline bounds how long a new connection may take to send its first byte.
var sniffDeadline = 10 * time.Second

type sniffer struct {
	net.Listener
	cfg   *tls.Config
	conns chan net.Conn
	done  chan struct{}
	err   error
}

func (s *sniffer) loop() {
	for {
		c, err := s.Listener.Accept()
		if err != nil {
			s.err = err
			close(s.done)
			return
		}
		go s.classify(c)
	}
}

func (s *sniffer) classify(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(sniffDeadline))
	r := bufio.NewReader(c)
	b, err := r.Peek(1)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return
	}
	var out net.Conn = &peekedConn{Conn: c, r: r}
	if b[0] == 0x16 {
		out = tls.Server(out, s.cfg)
	}
	select {
	case s.conns <- out:
	case <-s.done:
		_ = c.Close()
	}
}

func (s *sniffer) Accept() (net.Conn, error) {
	select {
	case c := <-s.conns:
		return c, nil
	case <-s.done:
		return nil, s.err
	}
}

type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (p *peekedConn) Read(b []byte) (int, error) { return p.r.Read(b) }
