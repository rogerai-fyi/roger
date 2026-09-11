package edge

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

// Describe is what a node says about itself OVER ITS OWN CERTIFICATE - the only account
// of a node that is worth anything, because the channel it arrives on proves the node
// holds the private key its identity is derived from.
//
// The advertisement carries a copy of some of this, and that copy is never believed:
// after a dial, the capabilities the fleet records are the ones that came back here.
type Describe struct {
	NodeID  string   `json:"node_id"`
	Account string   `json:"account"`
	Kind    string   `json:"kind"`
	Caps    []string `json:"caps"`
}

// DescribePath is the one endpoint the LAN face exposes at this stage. Discovery is
// discovery: it finds nodes and verifies who they are. Being asked to DO something is
// protocol.feature and control.feature, and neither is reachable from here.
const DescribePath = "/edge/describe"

// Server is a node's LAN face: an HTTPS listener whose certificate IS its identity.
type Server struct {
	desc Describe
	cert tls.Certificate
	fp   string
}

// NewServer builds the LAN face from the node's own certificate.
func NewServer(desc Describe, crt tls.Certificate) *Server {
	s := &Server{desc: desc, cert: crt}
	if len(crt.Certificate) > 0 {
		s.fp = Fingerprint(crt.Certificate[0])
	}
	return s
}

// Fingerprint is the SHA-256 of the certificate this node presents - the value it puts
// in its advertisement, and the value a peer checks the served certificate against.
func (s *Server) Fingerprint() string { return s.fp }

// TLSConfig is the listener's TLS configuration.
func (s *Server) TLSConfig() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12}
}

// Handler answers describe. It is a plain read of what this node declares: no argument
// is taken from the request, so there is nothing here for a caller to steer.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(DescribePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.desc)
	})
	return mux
}

// Advert is the advertisement this node broadcasts: its identity, what it declares, and
// the fingerprint of the certificate above. Built from the server so the advertised
// fingerprint cannot drift from the certificate actually served - they are the same
// value, read once.
func (s *Server) Advert(port int) Advert {
	return Advert{
		NodeID: s.desc.NodeID, Account: s.desc.Account, Kind: s.desc.Kind,
		Caps: s.desc.Caps, Port: port, Fingerprint: s.fp,
	}
}

// Fingerprint hashes a DER certificate the way the advertisement and the pin do.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// FingerprintOf is Fingerprint for a parsed certificate.
func FingerprintOf(c *x509.Certificate) string {
	if c == nil {
		return ""
	}
	return Fingerprint(c.Raw)
}
