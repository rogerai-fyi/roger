package main

// capsule.go is the CONTENT-BLIND one-time-code transport for context-capsule handoff (the app +
// CLI cross-agent handoff, roger.context.v1). The CLIENT generates a one-time code, derives an
// encryption key from it, encrypts the (already-redacted, signed) capsule, and stores ONLY the
// ciphertext here keyed by sha256(code); it shares the code out-of-band. The receiver sends the
// same sha256(code) to resolve, gets the ciphertext once, and decrypts with its own key(code).
//
// The broker never sees the code, the key, or the plaintext - it stores and returns an opaque,
// expiring, one-time blob. Mirrors the RC link-code posture (rcAttach): the mint is signed (for
// attribution / rate-limiting), the resolve is authed only by possession of the lookup, and any
// miss/expired/garbage resolve returns the IDENTICAL 404 so there is no existence oracle. Like the
// RC live hubs (not the durable roster), the store is per-instance + ephemeral; it is never
// persisted to the DB.

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

const (
	capsuleTTL         = store.RCCodeTTL    // 10 minutes, the same attach window as an RC link code
	capsuleMaxBlob     = 1 << 20            // 1 MB hard cap on the stored ciphertext
	capsuleMaxEntries  = 10000              // bound the in-memory store so a flood of mints cannot grow it unbounded
	capsuleReadLimit   = capsuleMaxBlob * 2 // base64 expands ~4/3, so allow a just-over-max blob to reach the 413 check
	capsuleResolveRead = 1 << 14
)

type capsuleBlob struct {
	blob    []byte
	expires int64 // unix seconds
}

// capsuleStore is a bounded, TTL-swept, per-instance map of lookup-hash -> ciphertext. It holds
// opaque bytes only (the broker cannot read them), and a blob is consumed on the first successful
// resolve (one-time).
type capsuleStore struct {
	mu sync.Mutex
	m  map[string]capsuleBlob
}

func newCapsuleStore() *capsuleStore { return &capsuleStore{m: map[string]capsuleBlob{}} }

// put stores a blob under lookup with a fresh TTL. Returns false when the store is at capacity
// (shed load rather than grow unbounded). Sweeps expired entries first so capacity self-heals.
func (c *capsuleStore) put(lookup string, blob []byte, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sweepLocked(now)
	if _, exists := c.m[lookup]; !exists && len(c.m) >= capsuleMaxEntries {
		return false
	}
	c.m[lookup] = capsuleBlob{blob: blob, expires: now.Add(capsuleTTL).Unix()}
	return true
}

// take returns the blob and REMOVES it (one-time), or false for absent/expired. The work is the
// same for every outcome so the caller can return a uniform error with no timing/existence oracle.
func (c *capsuleStore) take(lookup string, now time.Time) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[lookup]
	if ok {
		delete(c.m, lookup) // consumed whether live or expired: a resolve never leaves it behind
		if now.Unix() < v.expires {
			return v.blob, true
		}
	}
	return nil, false
}

func (c *capsuleStore) sweepLocked(now time.Time) {
	for k, v := range c.m {
		if now.Unix() >= v.expires {
			delete(c.m, k)
		}
	}
}

// capsuleMint handles POST /capsule: store an opaque encrypted blob keyed by the client-supplied
// lookup (sha256 of the client's one-time code). Requires a VERIFIED signature (any device/owner
// key) so a mint is attributable and rate-limitable - the plaintext/code/key never reach us.
func (b *broker) capsuleMint(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, capsuleReadLimit))
	if _, authed, _ := b.identityOf(r, body); !authed {
		jsonErr(w, http.StatusUnauthorized, "capsule mint requires a signed request")
		return
	}
	var req struct {
		Lookup string `json:"lookup"`
		Blob   string `json:"blob"`
	}
	if json.Unmarshal(body, &req) != nil || req.Lookup == "" || req.Blob == "" {
		jsonErr(w, http.StatusBadRequest, "lookup and blob required")
		return
	}
	blob, err := base64.StdEncoding.DecodeString(req.Blob)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "blob is not base64")
		return
	}
	if len(blob) == 0 || len(blob) > capsuleMaxBlob {
		jsonErr(w, http.StatusRequestEntityTooLarge, "capsule blob too large")
		return
	}
	now := time.Now()
	if !b.capsules.put(req.Lookup, blob, now) {
		jsonErr(w, http.StatusServiceUnavailable, "capsule store is full, retry shortly")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "expires": now.Add(capsuleTTL).Unix()})
}

// capsuleResolve handles POST /capsule/resolve: return the opaque blob ONCE (delete-on-read).
// Possession of the lookup is the authorization (no signature). Every miss/expired/garbage returns
// the IDENTICAL 404 so an attacker cannot probe which lookups exist.
func (b *broker) capsuleResolve(w http.ResponseWriter, r *http.Request) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	uniform := func() { writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such capsule"}) }
	body, _ := io.ReadAll(io.LimitReader(r.Body, capsuleResolveRead))
	var req struct {
		Lookup string `json:"lookup"`
	}
	_ = json.Unmarshal(body, &req)
	// Always attempt the take (constant work), even for empty/garbage input.
	blob, ok := b.capsules.take(req.Lookup, time.Now())
	if !ok {
		uniform()
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blob": base64.StdEncoding.EncodeToString(blob)})
}
