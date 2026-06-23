package client

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/bownux/rogerai/internal/protocol"
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

// SignedUserID is the stable wallet id the broker derives from the local pubkey.
func SignedUserID() string {
	return protocol.UserIDFromPubkey(UserPubHex())
}

// SignRequest is the exported request signer for callers outside this package
// (e.g. the TUI) that build their own broker requests. body must be exactly what
// is sent as the request body (nil for GET).
func SignRequest(req *http.Request, body []byte) { signRequest(req, body) }

// signRequest attaches the X-Roger-Pubkey / X-Roger-TS / X-Roger-Sig headers to
// req, signing over the canonical (method, path, ts, body) string with the local
// user key. body must be exactly what is sent as the request body (nil for GET).
func signRequest(req *http.Request, body []byte) {
	priv := LoadOrCreateUserKey()
	pubHex, ts, sigHex := protocol.SignRequest(priv, req.Method, req.URL.Path, body)
	req.Header.Set(protocol.HeaderPubkey, pubHex)
	req.Header.Set(protocol.HeaderTS, itoa(ts))
	req.Header.Set(protocol.HeaderSig, sigHex)
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
