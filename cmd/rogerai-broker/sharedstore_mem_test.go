package main

import (
	"context"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/store"
)

// TestMemStoreNoOps locks the single-instance memStore contract: every shared-state
// primitive is an explicit "no shared backend" no-op (errNoSharedStore / false / nil),
// so each money/seed/rendezvous fast-path cleanly falls back to its authoritative
// computation. This is the default broker backing when ROGERAI_MULTI_INSTANCE is off.
func TestMemStoreNoOps(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()

	if m.healthy() {
		t.Error("memStore.healthy() should be false (no backend)")
	}
	if err := m.Close(); err != nil {
		t.Errorf("memStore.Close() = %v, want nil", err)
	}

	// rate / seen / liveness
	if ok, _, err := m.rateAllow("k", 1, 1, time.Now()); !ok || err != errNoSharedStore {
		t.Errorf("rateAllow = %v/%v, want true/errNoSharedStore", ok, err)
	}
	if err := m.markSeen("k", time.Now()); err != errNoSharedStore {
		t.Errorf("markSeen = %v", err)
	}
	if _, err := m.liveness(); err != errNoSharedStore {
		t.Errorf("liveness = %v", err)
	}

	// cache
	if _, ok, err := m.cacheGet("k"); ok || err != errNoSharedStore {
		t.Errorf("cacheGet = %v/%v", ok, err)
	}
	_ = m.cacheSet("k", []byte("v"), time.Second)
	if err := m.cacheDel("k"); err != errNoSharedStore {
		t.Errorf("cacheDel = %v", err)
	}

	// counters / setIfAbsent
	if _, ok, err := m.counterGet("k"); ok || err != errNoSharedStore {
		t.Errorf("counterGet = %v/%v", ok, err)
	}
	if err := m.counterSet("k", 1, time.Second); err != errNoSharedStore {
		t.Errorf("counterSet = %v", err)
	}
	if _, err := m.counterIncr("k", 1); err != errNoSharedStore {
		t.Errorf("counterIncr = %v", err)
	}
	if ok, err := m.setIfAbsent("k", "v", time.Second); ok || err != errNoSharedStore {
		t.Errorf("setIfAbsent = %v/%v", ok, err)
	}

	// inflight
	if err := m.markInflight("n", "i", 1, time.Now()); err != errNoSharedStore {
		t.Errorf("markInflight = %v", err)
	}
	if _, err := m.inflightByNode("n"); err != errNoSharedStore {
		t.Errorf("inflightByNode = %v", err)
	}

	// rendezvous bus
	if _, err := m.busPublishJob("ch", []byte("j")); err != errNoSharedStore {
		t.Errorf("busPublishJob = %v", err)
	}
	if _, cancel, err := m.busSubscribeJobs(ctx, "ch"); err != errNoSharedStore {
		t.Errorf("busSubscribeJobs = %v", err)
	} else {
		cancel()
	}
	if err := m.busPublishResult("ch", []byte("r")); err != errNoSharedStore {
		t.Errorf("busPublishResult = %v", err)
	}
	if _, cancel, err := m.busSubscribeResult(ctx, "ch"); err != errNoSharedStore {
		t.Errorf("busSubscribeResult = %v", err)
	} else {
		cancel()
	}
	if err := m.busPublishStreamChunk("ch", []byte("c")); err != errNoSharedStore {
		t.Errorf("busPublishStreamChunk = %v", err)
	}
	if err := m.busPublishStreamDone("ch"); err != errNoSharedStore {
		t.Errorf("busPublishStreamDone = %v", err)
	}
	if _, cancel, err := m.busSubscribeStream(ctx, "ch"); err != errNoSharedStore {
		t.Errorf("busSubscribeStream = %v", err)
	} else {
		cancel()
	}

	// registry mirror
	if err := m.putNode("n", []byte("x"), time.Second); err != errNoSharedStore {
		t.Errorf("putNode = %v", err)
	}
	if _, ok, err := m.getNode("n"); ok || err != errNoSharedStore {
		t.Errorf("getNode = %v/%v", ok, err)
	}
	if _, err := m.allNodes(); err != errNoSharedStore {
		t.Errorf("allNodes = %v", err)
	}
	// private band registry mirror (separate namespace)
	if err := m.putPrivateNode("p", []byte("x"), time.Second); err != errNoSharedStore {
		t.Errorf("putPrivateNode = %v", err)
	}
	if _, ok, err := m.getPrivateNode("p"); ok || err != errNoSharedStore {
		t.Errorf("getPrivateNode = %v/%v", ok, err)
	}
	if _, err := m.allPrivateNodes(); err != errNoSharedStore {
		t.Errorf("allPrivateNodes = %v", err)
	}
	if err := m.dropSharedNode("p"); err != errNoSharedStore {
		t.Errorf("dropSharedNode = %v", err)
	}
}

// TestConfigLoaders covers the env-backed config loaders return a populated struct with
// the documented defaults (and that an env override flows through).
func TestConfigLoaders(t *testing.T) {
	t.Setenv("ROGERAI_REQUIRE_MODERATION", "1")
	mod := loadModeration()
	if !mod.require || len(mod.csamCats) == 0 {
		t.Errorf("loadModeration = %+v, want require + a CSAM category set", mod)
	}

	rc := loadRecount()
	if rc.client == nil || rc.tolerance <= 0 {
		t.Errorf("loadRecount = %+v, want a client + positive tolerance", rc)
	}

	rl := loadAnonRateLimiter()
	if rl == nil || rl.buckets == nil || rl.rpm <= 0 {
		t.Errorf("loadAnonRateLimiter = %+v, want a usable limiter", rl)
	}

	ml := loadMailer()
	if ml == nil || ml.from == "" || ml.sentCaps == nil {
		t.Errorf("loadMailer = %+v, want a usable mailer", ml)
	}
}

// TestRehydrateBans covers startup ban rehydration from the store into the in-memory
// ban caches (node bans + owner bans), reading through the real Mem store.
func TestRehydrateBans(t *testing.T) {
	mem := store.NewMem()
	_ = mem.BanNode("n-bad", "report threshold")
	_ = mem.BanOwner("pk-bad", "fraud", "{}")
	b := &broker{db: mem, banned: map[string]bool{}, bannedOwners: map[string]bool{}}

	b.rehydrateBans()
	b.rehydrateOwnerBans()

	if !b.banned["n-bad"] {
		t.Error("rehydrateBans should load the node ban into the cache")
	}
	if !b.bannedOwners["pk-bad"] {
		t.Error("rehydrateOwnerBans should load the owner ban into the cache")
	}
}

// TestMemStoreNoOpsForTheNewerPrimitives extends TestMemStoreNoOps to the methods added
// AFTER it was written - the tool-verdict mirror, the batch inflight marks, the edge
// inflight pair, and the RC bus - all of which measured 0.0% while the older primitives
// were covered. That gap is the predictable failure mode of a contract test that lists its
// subjects by hand: each new interface method ships with the contract assumed and never
// asserted. (An earlier version of this change OVERWROTE this whole file instead of
// extending it, which would have deleted two unrelated tests; the diff's deletion count is
// what caught it.)
func TestMemStoreNoOpsForTheNewerPrimitives(t *testing.T) {
	m := newMemStore()
	ctx := context.Background()
	now := time.Now()

	if err := m.markToolsVerified("n", "m", time.Minute); err != errNoSharedStore {
		t.Errorf("markToolsVerified = %v", err)
	}
	if err := m.clearToolsVerified("n", "m"); err != errNoSharedStore {
		t.Errorf("clearToolsVerified = %v", err)
	}
	if err := m.markInflightBatch("i", nil, now); err != errNoSharedStore {
		t.Errorf("markInflightBatch = %v", err)
	}
	if err := m.markEdgeInflight("i", "n", 1, now); err != errNoSharedStore {
		t.Errorf("markEdgeInflight = %v", err)
	}
	if err := m.markEdgeInflightBatch("i", nil, now); err != errNoSharedStore {
		t.Errorf("markEdgeInflightBatch = %v", err)
	}
	if _, err := m.edgeInflightByNode("i"); err != errNoSharedStore {
		t.Errorf("edgeInflightByNode = %v", err)
	}
	if _, err := m.busClaimJob("j"); err != errNoSharedStore {
		t.Errorf("busClaimJob = %v", err)
	}
	if err := m.busPublishRCIn("s", nil); err != errNoSharedStore {
		t.Errorf("busPublishRCIn = %v", err)
	}
	if err := m.busPublishRCOut("s", nil); err != errNoSharedStore {
		t.Errorf("busPublishRCOut = %v", err)
	}
	if _, cancel, err := m.busSubscribeRCIn(ctx, "s"); err != errNoSharedStore {
		t.Errorf("busSubscribeRCIn = %v", err)
	} else {
		cancel()
	}
	if _, cancel, err := m.busSubscribeRCOut(ctx, "s"); err != errNoSharedStore {
		t.Errorf("busSubscribeRCOut = %v", err)
	} else {
		cancel()
	}
	if _, err := m.busNextRCSeq("s"); err != errNoSharedStore {
		t.Errorf("busNextRCSeq = %v", err)
	}
	if _, _, err := m.busPopRCIn("s"); err != errNoSharedStore {
		t.Errorf("busPopRCIn = %v", err)
	}
	if _, err := m.allPrivateNodes(); err != errNoSharedStore {
		t.Errorf("allPrivateNodes = %v", err)
	}
	if err := m.dropSharedNode("n"); err != errNoSharedStore {
		t.Errorf("dropSharedNode = %v", err)
	}
}
