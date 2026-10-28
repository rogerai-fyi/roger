package store

import (
	"errors"
	"testing"
)

// TestEdgeNodeCRUDParity covers the Roger Edge fleet record on BOTH backends: enroll,
// account scoping, per-account name uniqueness, the one-Edge-per-node refusal, update,
// forget, and the money/registry rows a forget must leave alone.
//
// Same test, both stores: an invariant that holds only in Mem is not an invariant.
func TestEdgeNodeCRUDParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			n := EdgeNode{
				ID: "n_aaa", Account: "acct-1", Name: "bench-pi", Kind: "host",
				Caps:       []EdgeCap{{Name: "sense", State: "CLAIMED"}},
				Transports: []EdgeTransport{{Kind: "lan", Addr: "192.168.1.5:9443", Fingerprint: "ff00"}},
				Presence:   "VERIFIED", LastSeen: 1000, Pin: "ff00",
				History: []EdgeEvent{{At: 1, What: "enrolled", Detail: "bench-pi"}},
			}
			got, err := db.EnrollEdgeNode(n)
			if err != nil {
				t.Fatalf("EnrollEdgeNode: %v", err)
			}
			if got.ID != "n_aaa" || got.Name != "bench-pi" {
				t.Fatalf("enroll returned %+v", got)
			}

			// Round trip, field for field.
			back, ok, err := db.EdgeNodeByID("acct-1", "n_aaa")
			if err != nil || !ok {
				t.Fatalf("EdgeNodeByID: ok=%v err=%v", ok, err)
			}
			if back.Kind != "host" || back.Pin != "ff00" || back.Presence != "VERIFIED" || back.LastSeen != 1000 {
				t.Errorf("scalar round trip: %+v", back)
			}
			if len(back.Caps) != 1 || back.Caps[0].Name != "sense" || back.Caps[0].State != "CLAIMED" {
				t.Errorf("caps round trip: %+v", back.Caps)
			}
			if len(back.Transports) != 1 || back.Transports[0].Fingerprint != "ff00" {
				t.Errorf("transports round trip: %+v", back.Transports)
			}
			if len(back.History) != 1 || back.History[0].What != "enrolled" {
				t.Errorf("history round trip: %+v", back.History)
			}
			if back.Station {
				t.Errorf("Station is derived and must never be persisted")
			}

			// Account scoping: another account sees nothing, on every read path.
			if _, ok, _ := db.EdgeNodeByID("acct-2", "n_aaa"); ok {
				t.Error("acct-2 can describe acct-1's node")
			}
			if list, _ := db.EdgeNodesOfAccount("acct-2"); len(list) != 0 {
				t.Errorf("acct-2 lists acct-1's node: %+v", list)
			}
			if ok, _ := db.ForgetEdgeNode("acct-2", "n_aaa"); ok {
				t.Error("acct-2 could forget acct-1's node")
			}

			// A name is unique WITHIN an account...
			_, err = db.EnrollEdgeNode(EdgeNode{ID: "n_bbb", Account: "acct-1", Name: "bench-pi", Kind: "host"})
			if !errors.Is(err, ErrEdgeNameTaken) {
				t.Errorf("second node took the name in the same account: %v", err)
			}
			// ...and only within it.
			if _, err := db.EnrollEdgeNode(EdgeNode{ID: "n_ccc", Account: "acct-9", Name: "bench-pi", Kind: "host"}); err != nil {
				t.Errorf("another account was refused the same name: %v", err)
			}

			// A node belongs to exactly ONE Edge.
			_, err = db.EnrollEdgeNode(EdgeNode{ID: "n_aaa", Account: "acct-2", Name: "poached", Kind: "host"})
			var elsewhere *EdgeEnrolledElsewhere
			if !errors.As(err, &elsewhere) {
				t.Fatalf("a second enrollment was allowed: %v", err)
			}
			if elsewhere.Existing != "acct-1" {
				t.Errorf("the refusal does not carry the owning account: %+v", elsewhere)
			}
			if got := elsewhere.Error(); got != "that node already belongs to another Edge" {
				t.Errorf("the refusal message leaks or differs: %q", got)
			}

			// Re-enrolling into the SAME account refreshes rather than refusing.
			n.Name = "cabinet-pi"
			if _, err := db.EnrollEdgeNode(n); err != nil {
				t.Fatalf("re-enroll into the same account: %v", err)
			}
			if back, _, _ := db.EdgeNodeByID("acct-1", "n_aaa"); back.Name != "cabinet-pi" {
				t.Errorf("re-enroll did not refresh the name: %q", back.Name)
			}

			// Update is account-scoped and enforces the same name rule.
			n.Presence = "DARK"
			if err := db.UpdateEdgeNode(n); err != nil {
				t.Fatalf("UpdateEdgeNode: %v", err)
			}
			if back, _, _ := db.EdgeNodeByID("acct-1", "n_aaa"); back.Presence != "DARK" {
				t.Errorf("update did not land: %+v", back)
			}
			ghost := n
			ghost.Account = "acct-2"
			if err := db.UpdateEdgeNode(ghost); !errors.Is(err, ErrEdgeNoSuchNode) {
				t.Errorf("acct-2 could update acct-1's node: %v", err)
			}
			if err := db.UpdateEdgeNode(EdgeNode{ID: "n_zzz", Account: "acct-1", Name: "ghost"}); !errors.Is(err, ErrEdgeNoSuchNode) {
				t.Errorf("update of an unknown node: %v", err)
			}

			// Listing is ordered by name then id, on both backends.
			if _, err := db.EnrollEdgeNode(EdgeNode{ID: "n_ddd", Account: "acct-1", Name: "aardvark", Kind: "host"}); err != nil {
				t.Fatal(err)
			}
			list, err := db.EdgeNodesOfAccount("acct-1")
			if err != nil || len(list) != 2 {
				t.Fatalf("EdgeNodesOfAccount = %d rows, %v; want 2", len(list), err)
			}
			if list[0].Name != "aardvark" || list[1].Name != "cabinet-pi" {
				t.Errorf("listing is not name-ordered: %q, %q", list[0].Name, list[1].Name)
			}

			// A name freed by a rename may be taken by another node.
			if err := db.UpdateEdgeNode(EdgeNode{ID: "n_ddd", Account: "acct-1", Name: "cabinet-pi", Kind: "host"}); !errors.Is(err, ErrEdgeNameTaken) {
				t.Errorf("a live name was reusable: %v", err)
			}

			// Forget removes the record; a second forget is a clean false.
			ok, err = db.ForgetEdgeNode("acct-1", "n_aaa")
			if err != nil || !ok {
				t.Fatalf("ForgetEdgeNode: ok=%v err=%v", ok, err)
			}
			if _, ok, _ := db.EdgeNodeByID("acct-1", "n_aaa"); ok {
				t.Error("the node survived the forget")
			}
			if ok, _ := db.ForgetEdgeNode("acct-1", "n_aaa"); ok {
				t.Error("a second forget reported a deletion")
			}
			// And the name is free again.
			if _, err := db.EnrollEdgeNode(EdgeNode{ID: "n_eee", Account: "acct-1", Name: "cabinet-pi", Kind: "host"}); err != nil {
				t.Errorf("a forgotten node's name stayed reserved: %v", err)
			}
		})
	}
}

// TestEdgeFleetLeavesStationRegistryAlone pins the compatibility rule at the STORE
// layer: enrolling, updating and forgetting an Edge record never touches the Station
// registration, its owner binding, or the earnings the node accrued.
func TestEdgeFleetLeavesStationRegistryAlone(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := db.UpsertNode(NodeRecord{NodeID: "station-1", LastSeen: 7, RegisteredAt: 5}); err != nil {
				t.Fatal(err)
			}
			if err := db.BindNode("station-1", "acct-1"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.EnrollEdgeNode(EdgeNode{ID: "station-1", Account: "acct-1", Name: "the-box", Kind: "host"}); err != nil {
				t.Fatal(err)
			}
			if ok, _ := db.ForgetEdgeNode("acct-1", "station-1"); !ok {
				t.Fatal("forget reported nothing removed")
			}
			all, err := db.AllNodes()
			if err != nil || len(all) != 1 || all[0].NodeID != "station-1" ||
				all[0].LastSeen != 7 || all[0].RegisteredAt != 5 {
				t.Fatalf("the Station registration was disturbed: %+v (%v)", all, err)
			}
			acct, ok, err := db.AccountOfNode("station-1")
			if err != nil || !ok || acct != "acct-1" {
				t.Fatalf("the owner binding was disturbed: %q ok=%v err=%v", acct, ok, err)
			}
		})
	}
}
