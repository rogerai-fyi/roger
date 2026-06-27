package main

import (
	"testing"
	"time"
)

// TestValkeyAllNodes covers the registry-mirror read: putNode writes a node's reg JSON +
// indexes it in regset; allNodes enumerates the set and returns each reg blob (and an
// empty map when the set is empty).
func TestValkeyAllNodes(t *testing.T) {
	vs, _ := newTestValkey(t)

	// Empty registry -> empty map, no error.
	got, err := vs.allNodes()
	if err != nil || len(got) != 0 {
		t.Fatalf("empty allNodes = %v / %v, want {} / nil", got, err)
	}

	if err := vs.putNode("n1", []byte(`{"node_id":"n1"}`), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := vs.putNode("n2", []byte(`{"node_id":"n2"}`), time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err = vs.allNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got["n1"]) != `{"node_id":"n1"}` || string(got["n2"]) != `{"node_id":"n2"}` {
		t.Fatalf("allNodes = %v, want n1+n2 reg blobs", got)
	}
}

// TestValkeyInflightByNode covers the peer-inflight read: a PEER instance's inflight count
// for a node is summed, while the calling instance's OWN entry is excluded (the caller adds
// its exact local count itself, so double-counting is impossible).
func TestValkeyInflightByNode(t *testing.T) {
	vs, _ := newTestValkey(t)

	// Empty -> empty map.
	got, err := vs.inflightByNode("self")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty inflightByNode = %v / %v, want {} / nil", got, err)
	}

	now := time.Now()
	_ = vs.markInflight("peerA", "node1", 3, now)
	_ = vs.markInflight("peerB", "node1", 2, now)
	_ = vs.markInflight("self", "node1", 9, now)  // must be excluded for caller "self"
	_ = vs.markInflight("peerA", "node2", 0, now) // zero -> not summed

	got, err = vs.inflightByNode("self")
	if err != nil {
		t.Fatal(err)
	}
	if got["node1"] != 5 {
		t.Errorf("node1 peer inflight = %d, want 5 (3+2, self excluded)", got["node1"])
	}
	if _, ok := got["node2"]; ok {
		t.Errorf("node2 with only a zero entry must not appear, got %v", got["node2"])
	}
}
