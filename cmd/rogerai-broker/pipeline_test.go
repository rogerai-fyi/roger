package main

import (
	"strconv"
	"testing"
	"time"
)

// pipeline_test.go locks the correctness of the PIPELINED sync-loop reads
// (liveness/allNodes/inflightByNode): a multi-key batch must return EXACTLY what the old
// per-key sequential loop did, and - critically - must SKIP a node whose value key
// expired AFTER the SMEMBERS index listing (the per-command redis.Nil), never surfacing a
// fatal error or a zero entry for it.

// TestLivenessPipelineSkipsExpired: a node still in the index set but whose liveness hash
// expired since the listing is skipped, and the rest are returned intact.
func TestLivenessPipelineSkipsExpired(t *testing.T) {
	vs, mr := newTestValkey(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		if err := vs.markSeen("node-"+strconv.Itoa(i), now); err != nil {
			t.Fatalf("markSeen: %v", err)
		}
	}
	// Simulate "expired since the SMEMBERS listing": the id stays in the index set but its
	// per-node liveness value key is gone.
	mr.Del(livenessKey("node-2"))

	snap, err := vs.liveness()
	if err != nil {
		t.Fatalf("liveness: %v", err)
	}
	if len(snap) != 4 {
		t.Fatalf("liveness returned %d nodes, want 4 (node-2 skipped)", len(snap))
	}
	if _, present := snap["node-2"]; present {
		t.Errorf("node-2 should be skipped (its value key expired), got %v", snap["node-2"])
	}
	for i := 0; i < 5; i++ {
		if i == 2 {
			continue
		}
		if _, present := snap["node-"+strconv.Itoa(i)]; !present {
			t.Errorf("node-%d missing from liveness snapshot", i)
		}
	}
}

// TestAllNodesPipelineSkipsExpired: the registry mirror skips a node whose reg:<id> value
// expired since the regset listing, returning the remaining registrations.
func TestAllNodesPipelineSkipsExpired(t *testing.T) {
	vs, mr := newTestValkey(t)
	for i := 0; i < 4; i++ {
		if err := vs.putNode("node-"+strconv.Itoa(i), []byte(`{"node_id":"`+strconv.Itoa(i)+`"}`), time.Minute); err != nil {
			t.Fatalf("putNode: %v", err)
		}
	}
	mr.Del(regKey("node-1"))

	regs, err := vs.allNodes()
	if err != nil {
		t.Fatalf("allNodes: %v", err)
	}
	if len(regs) != 3 {
		t.Fatalf("allNodes returned %d, want 3 (node-1 skipped)", len(regs))
	}
	if _, present := regs["node-1"]; present {
		t.Errorf("node-1 should be skipped (reg value expired)")
	}
}

// TestInflightByNodePipelineSumsAndExcludesSelf: the batched HGETALL sums peers' counts,
// excludes the reader's own field, and omits a node whose hash expired since the listing.
func TestInflightByNodePipelineSumsAndExcludesSelf(t *testing.T) {
	vs, mr := newTestValkey(t)
	now := time.Now()
	// node-0: two peers + self -> sum should EXCLUDE self.
	_ = vs.markInflight("peer-a", "node-0", 2, now)
	_ = vs.markInflight("peer-b", "node-0", 3, now)
	_ = vs.markInflight("self", "node-0", 9, now)
	// node-1: one peer.
	_ = vs.markInflight("peer-a", "node-1", 4, now)
	// node-2: only self -> sum 0 -> omitted entirely.
	_ = vs.markInflight("self", "node-2", 7, now)
	// node-3: a peer, but its hash expires since the listing -> skipped.
	_ = vs.markInflight("peer-a", "node-3", 1, now)
	mr.Del(inflightKey("node-3"))

	snap, err := vs.inflightByNode("self")
	if err != nil {
		t.Fatalf("inflightByNode: %v", err)
	}
	if snap["node-0"] != 5 {
		t.Errorf("node-0 sum = %d, want 5 (2+3, self's 9 excluded)", snap["node-0"])
	}
	if snap["node-1"] != 4 {
		t.Errorf("node-1 sum = %d, want 4", snap["node-1"])
	}
	if _, present := snap["node-2"]; present {
		t.Errorf("node-2 should be omitted (only self reported -> peer sum 0)")
	}
	if _, present := snap["node-3"]; present {
		t.Errorf("node-3 should be skipped (hash expired since listing)")
	}
}
