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
// miss/expired/garbage resolve returns the IDENTICAL 404 so there is no existence oracle.
//
// MULTI-INSTANCE: the blob store is SHARED-first (b.shared.putCapsule / takeCapsule, a Valkey
// SET + one-time GETDEL keyed on rogerai:cap:<lookup>), so a mint on instance A resolves on
// instance B and exactly one of N concurrent resolves wins (atomic single-use across
// instances). When no shared backend is wired (single-instance / no-Valkey), it falls back to
// the bounded, TTL-swept, per-instance capsuleStore map below. Either way it is ephemeral
// ciphertext, never persisted to the money DB.

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

// putCapsuleBlob stores a mint SHARED-first (so a mint on one instance resolves on another),
// falling back to the per-instance map when no shared backend is wired (single-instance /
// no-Valkey). A shared backend that is present but ERRORING sheds the mint (returns false ->
// 503) rather than writing it locally where a peer could never see it. errNoSharedStore (the
// inert memStore) routes to the local map. Content-blind: only {lookup, ciphertext} at rest.
func (b *broker) putCapsuleBlob(lookup string, blob []byte, now time.Time) bool {
	if b.shared != nil {
		err := b.shared.putCapsule(lookup, blob, capsuleTTL)
		if err == nil {
			return true
		}
		if err != errNoSharedStore {
			return false // real backend error: shed (retry), never split-brain to local
		}
		// errNoSharedStore: no shared backend (memStore) -> use the per-instance map.
	}
	return b.capsules.put(lookup, blob, now)
}

// takeCapsuleBlob consumes a blob SHARED-first (atomic one-time GETDEL across instances),
// falling back to the per-instance map when no shared backend is wired. A shared backend
// that is present but ERRORING yields a uniform miss (the handler 404s) rather than probing
// the local map for a blob a peer minted. errNoSharedStore routes to the local map.
func (b *broker) takeCapsuleBlob(lookup string, now time.Time) ([]byte, bool) {
	if b.shared != nil {
		blob, found, err := b.shared.takeCapsule(lookup)
		if err == nil {
			return blob, found // authoritative: a hit or a clean miss
		}
		if err != errNoSharedStore {
			return nil, false // real backend error: uniform miss
		}
		// errNoSharedStore: no shared backend -> use the per-instance map.
	}
	return b.capsules.take(lookup, now)
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
	if !b.putCapsuleBlob(req.Lookup, blob, now) {
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
	blob, ok := b.takeCapsuleBlob(req.Lookup, time.Now())
	if !ok {
		uniform()
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blob": base64.StdEncoding.EncodeToString(blob)})
}
