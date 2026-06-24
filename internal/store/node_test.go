package store

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestMemNodeRegistry exercises the persisted node registry on Mem: upsert preserves
// registered_at on refresh, refreshes the rest, TouchNode bumps only an existing
// node's last_seen, and AllNodes returns the full set for re-hydration. (Postgres
// implements the same contract against the rogerai.nodes table; there is no test DB
// in CI so the durable path is asserted here via the shared Store interface.)
func TestMemNodeRegistry(t *testing.T) {
	var s Store = NewMem()

	reg := protocol.NodeRegistration{
		NodeID: "n1", PubKey: "pk", BridgeToken: "tok-1", HW: "gpu",
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 1, PriceOut: 2, Ctx: 4096}},
	}
	if err := s.UpsertNode(NodeRecord{NodeID: "n1", Reg: reg, Confidential: true, LastSeen: 100}); err != nil {
		t.Fatal(err)
	}
	recs, err := s.AllNodes()
	if err != nil || len(recs) != 1 {
		t.Fatalf("AllNodes = %d recs err=%v, want 1", len(recs), err)
	}
	first := recs[0]
	if first.RegisteredAt == 0 {
		t.Error("registered_at should be set on first upsert")
	}
	if first.Reg.BridgeToken != "tok-1" || !first.Confidential || first.LastSeen != 100 {
		t.Errorf("round-trip mismatch: %+v", first)
	}
	if len(first.Reg.Offers) != 1 || first.Reg.Offers[0].PriceOut != 2 {
		t.Errorf("offers+pricing did not round-trip: %+v", first.Reg.Offers)
	}

	// Refresh: a new token + last_seen update, but registered_at is PRESERVED.
	reg2 := reg
	reg2.BridgeToken = "tok-2"
	if err := s.UpsertNode(NodeRecord{NodeID: "n1", Reg: reg2, LastSeen: 200}); err != nil {
		t.Fatal(err)
	}
	recs, _ = s.AllNodes()
	if len(recs) != 1 {
		t.Fatalf("refresh should not add a row, got %d", len(recs))
	}
	if recs[0].Reg.BridgeToken != "tok-2" || recs[0].LastSeen != 200 {
		t.Errorf("refresh should update token+last_seen: %+v", recs[0])
	}
	if recs[0].RegisteredAt != first.RegisteredAt {
		t.Errorf("registered_at must be preserved on refresh: was %d now %d", first.RegisteredAt, recs[0].RegisteredAt)
	}

	// TouchNode bumps an existing node's last_seen without a full re-register.
	now := time.Unix(500, 0)
	if err := s.TouchNode("n1", now); err != nil {
		t.Fatal(err)
	}
	recs, _ = s.AllNodes()
	if recs[0].LastSeen != 500 {
		t.Errorf("TouchNode last_seen = %d, want 500", recs[0].LastSeen)
	}
	if recs[0].Reg.BridgeToken != "tok-2" {
		t.Error("TouchNode must not disturb the registration")
	}

	// TouchNode on an unknown node is a no-op (no row created, no error).
	if err := s.TouchNode("ghost", now); err != nil {
		t.Fatal(err)
	}
	recs, _ = s.AllNodes()
	if len(recs) != 1 {
		t.Errorf("TouchNode on an unknown node must not create a row, got %d", len(recs))
	}
}
