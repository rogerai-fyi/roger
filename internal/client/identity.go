package client

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"rogerai.fm/roger/v7/internal/keyfile"
	"rogerai.fm/roger/v7/internal/lanmode"
	"rogerai.fm/roger/v7/internal/protocol"
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
// UserKeyPath is where this machine's signing identity (user.key) lives.
func UserKeyPath() string { return userKeyPath() }

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
	userKeyOnce = keyfile.MustLoadOrCreate(userKeyPath())
	return userKeyOnce
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
// address: see lanmode.WantsNonce), it binds a fresh per-request NONCE into the signature and sends
// it in X-Roger-Nonce, so the Tower's replay guard can refuse a captured request resent within
// the freshness window. Everywhere else it signs the plain way.
func signWithKey(req *http.Request, body []byte, priv ed25519.PrivateKey) {
	if lanmode.WantsNonce(req.URL, lanTowerHostList()) {
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

// lanTowerHosts lists the standalone Towers the CLI knows by address (host:port): the ones the
// owner added by hand (`roger lan add`) and the broker setting when it is a standalone Tower.
// nil = none. The CLI sets it once per process.
var lanTowerHosts atomic.Pointer[func() []string]

// SetLANTowerHosts sets where the hand-added LAN Tower addresses are read from.
func SetLANTowerHosts(fn func() []string) {
	if fn == nil {
		lanTowerHosts.Store(nil)
		return
	}
	lanTowerHosts.Store(&fn)
}

// lanTowerHostList is the known standalone Towers, for the nonce decision.
func lanTowerHostList() []string {
	if fn := lanTowerHosts.Load(); fn != nil {
		return (*fn)()
	}
	return nil
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
