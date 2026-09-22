package edge

// DIAL-OUT PRESENCE (features/edge/presence.feature). A member that cannot serve an inbound face -
// a phone, most of all - dials the authority and says "I am here". The authority records it as the
// SAME verified member discovery would have observed by dialing it, so a pushed node and a pulled
// node are indistinguishable on the map.
//
// The trust is identical to the pull side (verify.go): the presence is authenticated by the node's
// authority-ISSUED certificate. Only the direction of the dial changes; nothing new is trusted.

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// PresencePath is where a node pushes its presence.
const PresencePath = "/edge/present"

// presencePrefix domain-separates a presence signature from every other thing a node key signs.
const presencePrefix = "rogerai-edge-present/v1"

// presenceFreshness bounds how far from now a presence timestamp may be - the same window
// enrollment uses. A signed presence that never goes stale is one somebody can keep and replay.
const presenceFreshness = 5 * time.Minute

// presenceMaxBody bounds what an unauthenticated caller may make the authority hold: a presence is
// a certificate (a few KB) plus a few small fields.
const presenceMaxBody = 64 << 10

// PresenceRequest is one node telling the authority it is present. Everything here is public: the
// certificate is public by nature, and the signature binds the claim to one node key and one
// moment - a captured request is useless after its freshness window and its nonce is spent.
type PresenceRequest struct {
	NodeID string   `json:"node_id"`
	Cert   string   `json:"cert"`  // PEM leaf certificate the authority issued this node
	Name   string   `json:"name"`  // the node's chosen name, used only when it is new to the fleet
	Kind   string   `json:"kind"`  // host | board | mobile
	Caps   []string `json:"caps"`  // the capabilities the node reports (self-reported, like describe)
	Addr   string   `json:"addr"`  // its LAN address, or "" for a node that only ever dials out
	Nonce  string   `json:"nonce"` // hex, 16 bytes
	TS     int64    `json:"ts"`
	Sig    string   `json:"sig"` // hex ed25519 signature over canonical(), by the node key
}

// canonical is the exact byte string a presence is signed over. Every field that decides what is
// recorded is in it; the certificate is bound separately, by Authenticate deriving the same id and
// the signature being checked against the certificate's own key.
func (p PresenceRequest) canonical() []byte {
	return []byte(strings.Join([]string{
		presencePrefix, p.NodeID, p.Name, p.Kind, strings.Join(p.Caps, ","), p.Addr, p.Nonce,
		timeString(p.TS),
	}, "\n"))
}

func timeString(ts int64) string { return strconv.FormatInt(ts, 10) }

// PresenceHandler serves /edge/present for one authority. It writes to the SAME fleet discovery
// writes to, so a pushed member and a dialed member are one kind of thing. `now` is injected for
// tests. Every refusal is a 403 with a short reason, the same uniform "no" enrollment gives.
func PresenceHandler(fleet *Fleet, auth *cert.Authority, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	seen := &nonceCache{seen: map[string]int64{}}
	mux := http.NewServeMux()
	mux.HandleFunc(PresencePath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, presenceMaxBody))
		if err != nil {
			http.Error(w, "could not read the presence", http.StatusBadRequest)
			return
		}
		var req PresenceRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "that presence could not be read", http.StatusBadRequest)
			return
		}

		// 1. The certificate must parse.
		leaf, err := parseLeafPEM(req.Cert)
		if err != nil {
			refusePresence(w, "that presence carries no usable certificate")
			return
		}
		// 2. It must chain to THIS authority's root, be in date, and not be revoked. Authenticate
		//    is the one check the pull side trusts too (verify.go), so push and pull agree.
		id, err := auth.Authenticate(leaf)
		if err != nil {
			refusePresence(w, "that certificate did not come from this authority")
			return
		}
		// 3. The id it names must be the one it claims, and the one its KEY derives - the misissue
		//    check the pull side makes (verify.go step 5).
		pub, ok := leaf.PublicKey.(ed25519.PublicKey)
		if !ok || id != req.NodeID || NodeID(pub) != req.NodeID {
			refusePresence(w, "that presence's identity does not match its certificate")
			return
		}
		// 4. Fresh: not far from the authority's own clock.
		if d := now().Sub(time.Unix(req.TS, 0)); d > presenceFreshness || d < -presenceFreshness {
			refusePresence(w, "that presence is stale")
			return
		}
		// 5. Signed by the certificate's key, over exactly these fields.
		sig, err := hex.DecodeString(req.Sig)
		if err != nil || !ed25519.Verify(pub, req.canonical(), sig) {
			refusePresence(w, "that presence's signature does not check out")
			return
		}
		// 6. Not a replay of one already accepted.
		if !seen.admit(req.Nonce, now().Add(presenceFreshness).Unix(), now().Unix()) {
			refusePresence(w, "that presence was already seen")
			return
		}

		// Record it as a verified member - the same Observe discovery calls after a successful dial.
		caps := make([]Capability, 0, len(req.Caps))
		for _, c := range req.Caps {
			if cap := Capability(c); cap.Valid() {
				caps = append(caps, cap)
			}
		}
		if _, err := fleet.Observe(req.NodeID, Observation{
			Name: req.Name, Kind: req.Kind, Caps: caps,
			Addr: req.Addr, Fingerprint: Fingerprint(leaf.Raw),
		}); err != nil {
			http.Error(w, "the fleet could not be written", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	return mux
}

// parseLeafPEM decodes a single PEM CERTIFICATE block to an x509 certificate.
func parseLeafPEM(pemStr string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errNoCert
	}
	return x509.ParseCertificate(block.Bytes)
}

var errNoCert = &presenceError{"no certificate"}

type presenceError struct{ s string }

func (e *presenceError) Error() string { return e.s }

func refusePresence(w http.ResponseWriter, reason string) {
	http.Error(w, reason, http.StatusForbidden)
}

// nonceCache is a small, self-pruning set of presence nonces already spent, so a captured presence
// cannot be replayed within its freshness window. Bounded implicitly: only enrolled nodes get this
// far, and each nonce expires.
type nonceCache struct {
	mu   sync.Mutex
	seen map[string]int64 // nonce -> unix expiry
}

// admit returns true the FIRST time a nonce is seen, false on a repeat. It prunes expired entries
// as it goes so the map cannot grow without bound.
func (n *nonceCache) admit(nonce string, expiry, nowUnix int64) bool {
	if nonce == "" {
		return false // an unnonced presence is replayable by definition
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for k, exp := range n.seen {
		if exp < nowUnix {
			delete(n.seen, k)
		}
	}
	if _, ok := n.seen[nonce]; ok {
		return false
	}
	n.seen[nonce] = expiry
	return true
}
