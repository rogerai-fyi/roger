package main

import (
	"testing"
	"time"
)

// fakeShared is a single-instance memStore with two cross-instance reads overridden, so
// the multi-instance sync workers can be driven deterministically. It fakes only the
// shared-state boundary (Valkey); every other primitive keeps memStore's no-op contract.
type fakeShared struct {
	*memStore
	live     map[string]time.Time
	inflight map[string]int
}

func (f *fakeShared) liveness() (map[string]time.Time, error)       { return f.live, nil }
func (f *fakeShared) inflightByNode(string) (map[string]int, error) { return f.inflight, nil }

// TestSyncLivenessOnce covers the shared-liveness merge: a peer's newer last_seen is
// adopted; an older one does not regress the local value.
func TestSyncLivenessOnce(t *testing.T) {
	now := time.Now()
	b := &broker{
		lastSeen: map[string]time.Time{"n-old": now.Add(-time.Hour), "n-fresh": now},
		shared: &fakeShared{memStore: newMemStore(), live: map[string]time.Time{
			"n-old":   now,                   // newer than local -> adopt
			"n-fresh": now.Add(-time.Minute), // older than local -> ignored
			"n-new":   now,                   // unknown locally -> adopt
		}},
	}
	b.syncLivenessOnce()
	if !b.lastSeen["n-old"].Equal(now) {
		t.Error("a newer peer last_seen should be adopted")
	}
	if !b.lastSeen["n-fresh"].Equal(now) {
		t.Error("an older peer last_seen must not regress the local value")
	}
	if _, ok := b.lastSeen["n-new"]; !ok {
		t.Error("an unknown peer node should be added")
	}
}

// TestMergeSharedInflight covers the peer-inflight refresh: it copies the shared snapshot
// into peerInflight, and is a safe no-op when there is no shared store.
func TestMergeSharedInflight(t *testing.T) {
	b := &broker{
		instanceID: "inst-1",
		shared:     &fakeShared{memStore: newMemStore(), inflight: map[string]int{"node-a": 3}},
	}
	b.mergeSharedInflight()
	if b.peerInflight["node-a"] != 3 {
		t.Errorf("peerInflight = %v, want node-a:3", b.peerInflight)
	}
	// No shared store -> no panic, no change.
	(&broker{}).mergeSharedInflight()
}

// TestExpireStaleAttestations covers the re-attestation lapse sweep: a node whose last
// attestation is older than the TTL loses confidential status; a fresh one keeps it.
func TestExpireStaleAttestations(t *testing.T) {
	now := time.Now()
	b := &broker{
		attestedAt:   map[string]time.Time{"stale": now.Add(-2 * time.Hour), "fresh": now},
		confidential: map[string]bool{"stale": true, "fresh": true},
	}
	b.expireStaleAttestations(now, time.Hour)
	if b.confidential["stale"] {
		t.Error("a node past the re-attest TTL should lose confidential status")
	}
	if _, ok := b.attestedAt["stale"]; ok {
		t.Error("a lapsed node's attestation timestamp should be dropped")
	}
	if !b.confidential["fresh"] {
		t.Error("a freshly-attested node must keep confidential status")
	}
}
