package main

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// TestValkeyInstancePresence: markInstance records a per-instance presence heartbeat that
// liveInstances counts as one DISTINCT live instance; a presence key that expires past
// instanceTTL is no longer counted (a crashed instance ages out of the fleet).
func TestValkeyInstancePresence(t *testing.T) {
	mr := miniredis.RunT(t)
	v, err := newValkeyStore("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("newValkeyStore: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })

	now := time.Now()
	// Empty fleet -> 0 live.
	if n, err := v.liveInstances(); err != nil || n != 0 {
		t.Fatalf("liveInstances(empty) = %d, %v; want 0, nil", n, err)
	}

	if err := v.markInstance("a", now); err != nil {
		t.Fatalf("markInstance a: %v", err)
	}
	if err := v.markInstance("b", now); err != nil {
		t.Fatalf("markInstance b: %v", err)
	}
	// Re-marking the same id is idempotent (still one distinct instance).
	if err := v.markInstance("a", now); err != nil {
		t.Fatalf("markInstance a (again): %v", err)
	}

	if n, err := v.liveInstances(); err != nil || n != 2 {
		t.Fatalf("liveInstances = %d, %v; want 2, nil", n, err)
	}

	// Age a's presence past instanceTTL: it drops out, b remains (refreshed).
	mr.FastForward(instanceTTL + time.Second)
	if err := v.markInstance("b", time.Now()); err != nil {
		t.Fatalf("markInstance b (refresh): %v", err)
	}
	if n, err := v.liveInstances(); err != nil || n != 1 {
		t.Fatalf("liveInstances(after expiry) = %d, %v; want 1, nil", n, err)
	}
}

// TestValkeyInstancePresenceBackendDown: with the backend gone, both presence ops return an
// error and never panic (the caller degrades to a self-only count).
func TestValkeyInstancePresenceBackendDown(t *testing.T) {
	mr := miniredis.RunT(t)
	v, err := newValkeyStore("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("newValkeyStore: %v", err)
	}
	mr.Close()

	if err := v.markInstance("a", time.Now()); err == nil {
		t.Error("markInstance must error when the backend is down")
	}
	if _, err := v.liveInstances(); err == nil {
		t.Error("liveInstances must error when the backend is down")
	}
}

// TestMemStoreInstancePresence: the inert default store no-ops both presence primitives so the
// in-memory single-instance path never touches a backend.
func TestMemStoreInstancePresence(t *testing.T) {
	m := newMemStore()
	if err := m.markInstance("a", time.Now()); err == nil {
		t.Error("memStore.markInstance must return the no-shared-store sentinel")
	}
	if n, err := m.liveInstances(); err == nil || n != 0 {
		t.Errorf("memStore.liveInstances = %d, %v; want 0, err", n, err)
	}
}
