package main

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/store"
)

// TestBuildBrokerMultiInstanceFlagWithoutBackend locks the fail-SAFE branch: with
// ROGERAI_MULTI_INSTANCE set but no ROGERAI_REDIS_URL, buildBroker stays single-instance
// in-memory (no shared store, multiInstance off) rather than half-enabling a broken bus.
func TestBuildBrokerMultiInstanceFlagWithoutBackend(t *testing.T) {
	t.Setenv("ROGERAI_REDIS_URL", "")
	t.Setenv("ROGERAI_MULTI_INSTANCE", "1")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	if b.shared != nil {
		t.Error("no REDIS_URL must leave b.shared nil")
	}
	if b.multiInstance {
		t.Error("multi-instance must stay OFF without a shared backend")
	}
}

// TestBuildBrokerSharedAndMultiInstance locks the wired-Valkey path: with a reachable
// ROGERAI_REDIS_URL and the multi-instance flag, buildBroker attaches the shared store,
// names the safe limiters' shared buckets, turns multi-instance ON, and assigns an
// instance id.
func TestBuildBrokerSharedAndMultiInstance(t *testing.T) {
	mr := miniredis.RunT(t)
	t.Setenv("ROGERAI_REDIS_URL", "redis://"+mr.Addr())
	t.Setenv("ROGERAI_MULTI_INSTANCE", "1")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	if b.shared == nil {
		t.Fatal("a reachable REDIS_URL must attach the shared store")
	}
	t.Cleanup(func() { _ = b.shared.Close() })
	if !b.multiInstance || b.instanceID == "" {
		t.Errorf("multi-instance must be ON with an instance id, got mi=%v id=%q", b.multiInstance, b.instanceID)
	}
	if b.anonRL.name != "anon" || b.anonRL.shared == nil {
		t.Errorf("anon limiter must get a named shared bucket, got name=%q shared=%v", b.anonRL.name, b.anonRL.shared != nil)
	}
}

// TestBuildBrokerSharedNoMultiInstance locks the shared-but-single path: a wired Valkey
// WITHOUT the multi-instance flag shares the safe rate limiters + liveness, but keeps the
// in-memory single-instance request path (multiInstance off).
func TestBuildBrokerSharedNoMultiInstance(t *testing.T) {
	mr := miniredis.RunT(t)
	t.Setenv("ROGERAI_REDIS_URL", "redis://"+mr.Addr())
	t.Setenv("ROGERAI_MULTI_INSTANCE", "")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	if b.shared == nil {
		t.Fatal("a reachable REDIS_URL must attach the shared store")
	}
	t.Cleanup(func() { _ = b.shared.Close() })
	if b.multiInstance {
		t.Error("multi-instance must stay OFF when the flag is unset, even with a shared backend")
	}
}
