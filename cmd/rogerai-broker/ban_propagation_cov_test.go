package main

// ban_propagation_cov_test.go covers the DEFENSIVE branches of the cross-instance ban
// propagation (bumpBanRev / syncBanRev) that the behavior spec
// (ban_propagation_bdd_test.go) does not naturally reach: no shared backend, a clean miss
// (no ban ever), an unchanged revision (the cheap common case), a Valkey bump error, and a
// store re-pull error (fail-safe: the rev is left unrecorded so the next tick retries). The
// happy path (a real ban propagating A->B) lives in the BDD suite.

import (
	"crypto/ed25519"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/store"
)

// TestSyncBanRevNoSharedBackend: with no shared store wired, both helpers are guarded no-ops
// (single-instance: the local map flip is already the whole truth).
func TestSyncBanRevNoSharedBackend(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	b := newMIBroker(t, priv, store.NewMem(), nil) // nil mr => shared==nil
	b.syncBanRev()                                 // must not panic / must be a no-op
	b.bumpBanRev()                                 // must not panic / must be a no-op
	if b.banRev != 0 {
		t.Fatalf("banRev moved to %v with no shared backend, want 0", b.banRev)
	}
}

// TestSyncBanRevCleanMissThenUnchanged: before any ban the counter is absent (clean miss ->
// no-op); after a ban + sync, a SECOND sync at the same revision takes the cheap unchanged
// no-op path (no re-pull).
func TestSyncBanRevCleanMissThenUnchanged(t *testing.T) {
	mr := miniredis.RunT(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, priv, db, mr)
	b := newMIBroker(t, priv, db, mr)

	// Clean miss: no ban has ever bumped the counter -> no-op, nothing applied.
	b.syncBanRev()
	if b.banRev != 0 || b.isBanned("n1") {
		t.Fatalf("clean-miss sync changed state: banRev=%v isBanned=%v", b.banRev, b.isBanned("n1"))
	}

	a.banNode("n1", "abuse")
	b.syncBanRev()
	if !b.isBanned("n1") {
		t.Fatal("after a real ban + sync, instance B should see n1 banned")
	}
	rev := b.banRev
	// Unchanged: a second sync at the same revision is a no-op (does not re-pull).
	b.syncBanRev()
	if b.banRev != rev || !b.isBanned("n1") {
		t.Fatalf("unchanged-rev sync mutated state: banRev %v->%v", rev, b.banRev)
	}
}

// TestBumpBanRevValkeyError: a dead Valkey makes counterIncr error; bumpBanRev must swallow
// it (best-effort) and never panic — the ban already persisted + flipped the local set.
func TestBumpBanRevValkeyError(t *testing.T) {
	mr := miniredis.RunT(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	b := newMIBroker(t, priv, store.NewMem(), mr)
	mr.Close()     // kill the backend: counterIncr now errors
	b.bumpBanRev() // must not panic; logs + returns
}

// TestSyncBanRevRePullErrorRetries: when the shared rev changed but the store re-pull errors,
// syncBanRev must leave banRev UNRECORDED (so the next tick retries) and NOT apply a partial
// state — fail-safe so a ban is never silently dropped. Covers both the node-set and the
// owner-set read-error legs.
func TestSyncBanRevRePullErrorRetries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failNodes  bool
		failOwners bool
	}{
		{"BannedNodes errors", true, false},
		{"BannedOwners errors", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mr := miniredis.RunT(t)
			_, priv, _ := ed25519.GenerateKey(nil)
			fs := &failStore{Store: store.NewMem()}
			b := newMIBroker(t, priv, fs, mr)

			// A peer banned a node + an owner (durable truth) and bumped the shared rev.
			_ = fs.Store.BanNode("n1", "abuse")
			_ = fs.Store.BanOwner("op-1", "abuse", "{}")
			if _, err := b.shared.counterIncr(banRevKey, 1); err != nil {
				t.Fatalf("seed rev: %v", err)
			}

			// Re-pull errors: nothing applied, rev left unrecorded for a retry.
			fs.failBannedNodes, fs.failBannedOwners = tc.failNodes, tc.failOwners
			b.syncBanRev()
			if b.banRev != 0 || b.isBanned("n1") || b.isOwnerBanned("op-1") {
				t.Fatalf("partial apply on a re-pull error: banRev=%v node=%v owner=%v", b.banRev, b.isBanned("n1"), b.isOwnerBanned("op-1"))
			}

			// Next tick, the store is healthy: the ban now applies (retry succeeded).
			fs.failBannedNodes, fs.failBannedOwners = false, false
			b.syncBanRev()
			if b.banRev == 0 || !b.isBanned("n1") || !b.isOwnerBanned("op-1") {
				t.Fatalf("retry did not apply the ban: banRev=%v node=%v owner=%v", b.banRev, b.isBanned("n1"), b.isOwnerBanned("op-1"))
			}
		})
	}
}
