package main

// THE AUTHORITY'S TLS LISTENER (docs/edge-authority-https.md; founder ruling 2026-10-13). The same
// authority mux the plain listener serves, on a second listener wrapped in TLS with a server
// certificate the Edge root issues for this machine's node identity and LAN addresses
// (cert.IssueServer): P-256, ServerAuth only, 24 hours, held in memory only and reissued at half its
// life or when the machine's LAN addresses change. A phone checks it against the root it already
// holds, so a phone's turns, presence and trouble reads are private on the LAN and its answers cannot
// be rewritten. The plain listener keeps what a phone needs before it holds a root (enroll, claim,
// root, revocations, pairing) and stops serving /edge/phone/* once this one is up.

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v7/internal/edgeauth"
	"rogerai.fm/roger/v7/internal/towercore/cert"
)

// The headers every plain-listener answer carries once the TLS listener is up, so a phone that
// enrolled or claimed over plain HTTP learns where to go next and which identity to expect there.
const (
	edgeTLSHeader       = "Roger-Edge-TLS"       // the TLS listener's port, decimal
	edgeAuthorityHeader = "Roger-Edge-Authority" // the authority's node id: the one URI SAN its server certificate names
)

const envAuthorityTLSBind = "ROGERAI_EDGE_AUTHORITY_TLS_BIND"

// edgeAuthorityTLSPortFile remembers the TLS port beside the plain one, so a restart binds the same.
func edgeAuthorityTLSPortFile() string { return filepath.Join(edgeAuthDir(), "authority-tls-port") }

func edgeAuthorityTLSBind() string {
	if v := os.Getenv(envAuthorityTLSBind); v != "" {
		return v
	}
	if b, err := os.ReadFile(edgeAuthorityTLSPortFile()); err == nil {
		if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p > 0 && p <= 65535 {
			return ":" + strconv.Itoa(p)
		}
	}
	return ":0"
}

// edgeServerCert issues and reissues the authority's server certificate.
type edgeServerCert struct {
	mu     sync.Mutex
	auth   *cert.Authority
	nodeID string
	ips    func() []string
	now    func() time.Time
	cur    *tls.Certificate
	issued time.Time
	ipsKey string
}

func ipsKey(ips []string) string {
	s := append([]string(nil), ips...)
	sort.Strings(s)
	return strings.Join(s, ",")
}

// get is tls.Config.GetCertificate: the current leaf, reissued at half its life or when the LAN
// addresses changed.
func (s *edgeServerCert) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	addrs := s.ips()
	if s.cur != nil && now.Sub(s.issued) < cert.ServerCertTTL/2 && ipsKey(addrs) == s.ipsKey {
		return s.cur, nil
	}
	var ips []net.IP
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil {
			ips = append(ips, ip)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("this machine has no LAN address to name in its server certificate")
	}
	leaf, key, err := s.auth.IssueServer(s.nodeID, ips, cert.ServerCertTTL)
	if err != nil {
		return nil, err
	}
	s.cur = &tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: key, Leaf: leaf}
	s.issued, s.ipsKey = now, ipsKey(addrs)
	return s.cur, nil
}

// edgeAuthorityTLSConfig is the TLS listener's configuration: no client certificates (a phone cannot
// present one; it signs each request instead).
func edgeAuthorityTLSConfig(sc *edgeServerCert) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: sc.get}
}

// edgePlainAuthority wraps the plain listener's mux: once the TLS listener is up every answer says
// where TLS is and which authority to expect there. The phone's paths are never served here (RULED
// 2026-10-13, over TLS only): 421 naming the port when TLS is up, 503 when there is no TLS listener.
func edgePlainAuthority(next http.Handler, tlsPort func() int, authorityID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		port := tlsPort()
		if port > 0 {
			w.Header().Set(edgeTLSHeader, strconv.Itoa(port))
			w.Header().Set(edgeAuthorityHeader, authorityID)
		}
		if strings.HasPrefix(r.URL.Path, "/edge/phone/") {
			w.Header().Set("Content-Type", "application/json")
			if port > 0 {
				w.WriteHeader(http.StatusMisdirectedRequest)
				_, _ = fmt.Fprintf(w, `{"error":{"message":"a phone's requests are served over TLS on port %d","type":"edge_error"}}`+"\n", port)
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"error":{"message":"a phone's requests are served only over TLS, and this authority has no TLS listener","type":"edge_error"}}`+"\n")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// startAuthorityTLS opens the TLS listener over mux. It returns the listener and server, or nils when
// this machine has no member identity to name (it then serves plain HTTP only, and says so in the log).
func startAuthorityTLS(local *edgeauth.Local, mux http.Handler) (net.Listener, *http.Server) {
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil || !ok {
		log.Println("edge: the authority has no node identity of its own, so it serves no TLS listener this run")
		return nil, nil
	}
	sc := &edgeServerCert{auth: local.Authority(), nodeID: id.NodeID, ips: edgeLANIPs, now: time.Now}
	raw, err := edgeListen(edgeAuthorityTLSBind())
	if err != nil {
		if raw, err = edgeListen(":0"); err != nil {
			log.Println("edge: could not open the authority's TLS listener:", err)
			return nil, nil
		}
	} else if tcp, ok := raw.Addr().(*net.TCPAddr); ok {
		_ = os.MkdirAll(edgeAuthDir(), 0o755)
		_ = os.WriteFile(edgeAuthorityTLSPortFile(), []byte(strconv.Itoa(tcp.Port)), 0o644)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(tls.NewListener(raw, edgeAuthorityTLSConfig(sc))) }()
	return raw, srv
}

// edgeAuthorityTLSPort is the TLS listener's port, or 0.
func edgeAuthorityTLSPort(ln net.Listener) int {
	if ln == nil {
		return 0
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		return tcp.Port
	}
	return 0
}
