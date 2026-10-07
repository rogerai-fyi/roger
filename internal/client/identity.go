package client

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"net"
	"net/netip"
	"net/url"
	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/protocol"
	"strings"
	"sync/atomic"
)

// The consumer's signing identity: an Ed25519 keypair at
// $UserConfigDir/rogerai/user.key (0600), mirroring agent.loadOrCreateKey for the
// node key. The local proxy signs every broker request with this key so the
// broker can verify who is spending - a header alone (X-Roger-User) can no longer
// drain someone else's wallet.

var (
	userKeyMu   sync.Mutex
	userKeyOnce ed25519.PrivateKey
)

// userKeyPath is $UserConfigDir/rogerai/user.key.
func userKeyPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "rogerai", "user.key")
}

// LoadOrCreateUserKey returns the consumer's stable Ed25519 signing key, creating
// it (0600) on first use. Mirrors agent.loadOrCreateKey. Cached per process.
func LoadOrCreateUserKey() ed25519.PrivateKey {
	userKeyMu.Lock()
	defer userKeyMu.Unlock()
	if userKeyOnce != nil {
		return userKeyOnce
	}
	path := userKeyPath()
	if data, err := os.ReadFile(path); err == nil {
		if raw, err := hex.DecodeString(string(bytes.TrimSpace(data))); err == nil && len(raw) == ed25519.PrivateKeySize {
			userKeyOnce = ed25519.PrivateKey(raw)
			return userKeyOnce
		}
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, []byte(hex.EncodeToString(priv)), 0600)
	userKeyOnce = priv
	return priv
}

// UserPubHex is the hex public key for the local signing identity.
func UserPubHex() string {
	priv := LoadOrCreateUserKey()
	return hex.EncodeToString(priv.Public().(ed25519.PublicKey))
}

// SignRequest is the exported request signer for callers outside this package
// (e.g. the TUI) that build their own broker requests. body must be exactly what
// is sent as the request body (nil for GET).
func SignRequest(req *http.Request, body []byte) { signRequest(req, body) }

// SignRequestWith signs as a SPECIFIC key rather than this machine's identity. It exists
// for tests that must act as a second, genuinely different device: the process-wide key
// cache means pointing the package at another config dir does NOT yield another key.
func SignRequestWith(req *http.Request, body []byte, priv ed25519.PrivateKey) {
	signWithKey(req, body, priv)
}

// signRequest attaches the X-Roger-Pubkey / X-Roger-TS / X-Roger-Sig headers to
// req, signing over the canonical (method, path, ts, body) string with the local
// user key. body must be exactly what is sent as the request body (nil for GET).
func signRequest(req *http.Request, body []byte) {
	signWithKey(req, body, LoadOrCreateUserKey())
}

// signWithKey signs req with priv. When the target is a LAN Tower (plaintext http to a local
// address: see targetsLANTower), it binds a fresh per-request NONCE into the signature and sends
// it in X-Roger-Nonce, so the Tower's replay guard can refuse a captured request resent within
// the freshness window. Everywhere else it signs the plain way.
func signWithKey(req *http.Request, body []byte, priv ed25519.PrivateKey) {
	if targetsLANTower(req.URL) {
		nonce := protocol.NewNonce()
		pubHex, ts, sigHex := protocol.SignRequestNonce(priv, req.Method, req.URL.Path, body, nonce)
		req.Header.Set(protocol.HeaderPubkey, pubHex)
		req.Header.Set(protocol.HeaderTS, itoa(ts))
		req.Header.Set(protocol.HeaderSig, sigHex)
		req.Header.Set(protocol.HeaderNonce, nonce)
		return
	}
	pubHex, ts, sigHex := protocol.SignRequest(priv, req.Method, req.URL.Path, body)
	req.Header.Set(protocol.HeaderPubkey, pubHex)
	req.Header.Set(protocol.HeaderTS, itoa(ts))
	req.Header.Set(protocol.HeaderSig, sigHex)
}

// lanTowerHosts lists the Tower addresses the owner added by hand (`roger lan add`); nil = none.
// The CLI sets it once per process, so a hand-added Tower (a CGNAT address, or a name) gets the
// nonce like any other LAN Tower.
var lanTowerHosts atomic.Pointer[func() []string]

// SetLANTowerHosts sets where the hand-added LAN Tower addresses are read from.
func SetLANTowerHosts(fn func() []string) {
	if fn == nil {
		lanTowerHosts.Store(nil)
		return
	}
	lanTowerHosts.Store(&fn)
}

// targetsLANTower reports whether a request goes to a LAN Tower and so carries a replay nonce:
// plaintext http to an address the LAN check calls local (lanmode.IsLocalIP: RFC 1918, ULA and
// link-local, zoned IPv6 included), or to an address the owner added by hand with `roger lan add`
// (which is how a CGNAT address, or a name, becomes a Tower). It is the same rule `roger lan add`
// and the LAN dial use, so no Tower they accept goes without the nonce.
//
// LOOPBACK is deliberately excluded: traffic to it never leaves the host, so there is no wire to
// capture a replay on, and it is what in-process test servers use. https (the public broker) and
// any public host are excluded too, so no nonce ever reaches them.
func targetsLANTower(u *url.URL) bool {
	if u == nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if ip.IsLoopback() {
			return false
		}
		if lanmode.IsLocalIP(net.IP(ip.WithZone("").AsSlice())) {
			return true
		}
	}
	if fn := lanTowerHosts.Load(); fn != nil {
		for _, a := range (*fn)() {
			if t, err := lanmode.ParseAddr(a); err == nil && strings.EqualFold(t.Host, host) {
				return true
			}
		}
	}
	return false
}

// itoa is a tiny helper (avoid importing strconv just for one call site here).
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
