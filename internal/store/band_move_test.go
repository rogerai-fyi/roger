package store

import (
	"testing"
	"time"
)

// MoveBand is the first write path Band.NodeID has ever had. Until now NodeID was set once
// at CreateBand and no store method could change it, which is why a private band was hard
// bound to one model for life (node id is "<station>-<model>"). Moving it is what lets an
// owner point their band at a different model WITHOUT rotating the secret code and cutting
// off everyone tuned in.
//
// Spec: features/sharing/band_management.feature - "Moving a band to another model keeps
// the frequency code alive" / "A move is refused when the destination already has its own
// band" / "A band can only be moved by the owner who holds it".

func bandFixture(t *testing.T) (*Mem, Band) {
	t.Helper()
	m := NewMem()
	b := Band{
		ID: "band_1", CodeHash: "hash_1", CodeDisplay: "147.520 MHz · ••••-••••",
		Owner: "owner_pub", NodeID: "amber-fox-model-a", CreatedAt: 1000,
	}
	if err := m.CreateBand(b); err != nil {
		t.Fatalf("CreateBand: %v", err)
	}
	return m, b
}

func TestMoveBandKeepsTheCodeAndFindsTheNewNode(t *testing.T) {
	m, b := bandFixture(t)

	moved, err := m.MoveBand(b.ID, b.Owner, "amber-fox-model-b")
	if err != nil {
		t.Fatalf("MoveBand: %v", err)
	}
	if !moved {
		t.Fatal("MoveBand should report that it moved the band")
	}

	// The secret is untouched: same id, same hash, same display. This is the whole point -
	// everyone holding the code keeps working.
	got, ok, err := m.BandByCodeHash("hash_1")
	if err != nil || !ok {
		t.Fatalf("the moved band must still resolve by its code hash (ok=%v err=%v)", ok, err)
	}
	if got.ID != b.ID || got.CodeDisplay != b.CodeDisplay {
		t.Errorf("move must not disturb identity/display: %+v", got)
	}
	if got.NodeID != "amber-fox-model-b" {
		t.Errorf("NodeID = %q, want the destination", got.NodeID)
	}

	// The register seam (tunnel.go BandByNode) must now find it at the DESTINATION...
	if nb, ok, _ := m.BandByNode("amber-fox-model-b"); !ok || nb.ID != b.ID {
		t.Errorf("BandByNode(destination) must return the moved band, got ok=%v %+v", ok, nb)
	}
	// ...and must NOT find it at the source, or the old node would silently keep the band.
	if _, ok, _ := m.BandByNode("amber-fox-model-a"); ok {
		t.Error("the source node must no longer resolve a band (privacy fails closed)")
	}

	// A move is not a mint: the quota is unchanged.
	n, _ := m.CountActiveBands(b.Owner, time.Now())
	if n != 1 {
		t.Errorf("active band count = %d, want 1 (a move must never mint)", n)
	}
}

func TestMoveBandRefusesAnOccupiedDestination(t *testing.T) {
	m, b := bandFixture(t)
	// A second band already lives on the destination node.
	other := Band{ID: "band_2", CodeHash: "hash_2", Owner: b.Owner, NodeID: "amber-fox-model-b"}
	if err := m.CreateBand(other); err != nil {
		t.Fatalf("CreateBand: %v", err)
	}

	moved, err := m.MoveBand(b.ID, b.Owner, "amber-fox-model-b")
	if err == nil && moved {
		t.Fatal("moving onto an occupied node must be refused - one node carries at most one band")
	}
	// Both bands are left exactly as they were.
	if got, _, _ := m.BandByNode("amber-fox-model-a"); got.ID != "band_1" {
		t.Error("a refused move must leave the source band in place")
	}
	if got, _, _ := m.BandByNode("amber-fox-model-b"); got.ID != "band_2" {
		t.Error("a refused move must leave the destination band in place")
	}
}

func TestMoveBandIsOwnerScoped(t *testing.T) {
	m, b := bandFixture(t)

	moved, _ := m.MoveBand(b.ID, "someone_else", "attacker-node")
	if moved {
		t.Fatal("a band must never be moved by anyone but its issuing owner")
	}
	if got, _, _ := m.BandByNode("amber-fox-model-a"); got.ID != b.ID {
		t.Error("a foreign move attempt must not disturb the band")
	}
	if _, ok, _ := m.BandByNode("attacker-node"); ok {
		t.Error("a foreign move must not bind the band to the attacker's node")
	}
}

func TestMoveBandUnknownIDReportsNoMove(t *testing.T) {
	m, b := bandFixture(t)
	if moved, _ := m.MoveBand("band_nope", b.Owner, "amber-fox-model-b"); moved {
		t.Error("an unknown band id must report no move")
	}
}

// A revoked band is dead: moving it would resurrect a burnt code at a new node.
func TestMoveBandRefusesARevokedBand(t *testing.T) {
	m, b := bandFixture(t)
	if _, err := m.SetBandRevoked(b.ID, b.Owner, true); err != nil {
		t.Fatalf("SetBandRevoked: %v", err)
	}
	if moved, _ := m.MoveBand(b.ID, b.Owner, "amber-fox-model-b"); moved {
		t.Error("a revoked band must not be movable - its code is permanently burnt")
	}
}

// Moving to the node it already sits on is a harmless no-op, not an "occupied" error
// (otherwise a retried/duplicated request would fail confusingly).
func TestMoveBandToItsOwnNodeIsIdempotent(t *testing.T) {
	m, b := bandFixture(t)
	moved, err := m.MoveBand(b.ID, b.Owner, b.NodeID)
	if err != nil {
		t.Fatalf("a self-move must not error: %v", err)
	}
	if !moved {
		t.Error("a self-move should report success (idempotent retry)")
	}
	if got, _, _ := m.BandByNode(b.NodeID); got.ID != b.ID {
		t.Error("a self-move must leave the band bound where it was")
	}
}
