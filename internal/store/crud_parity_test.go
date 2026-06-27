package store

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// TestNodeRegistryCRUD covers the persisted node registry on BOTH backends: upsert,
// the first-register timestamp preserved across a refresh, last_seen refreshed,
// TouchNode (and its unknown-node no-op), AllNodes re-hydration, account scoping,
// and DeleteNode (which leaves the owner binding intact).
func TestNodeRegistryCRUD(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			rec := NodeRecord{
				NodeID:       "node-x",
				Reg:          protocol.NodeRegistration{NodeID: "node-x", PubKey: "deadbeef", Region: "us"},
				Confidential: true,
				LastSeen:     1000,
				RegisteredAt: 500,
			}
			if err := db.UpsertNode(rec); err != nil {
				t.Fatalf("UpsertNode: %v", err)
			}
			// Bind it to an account so scoping + delete-keeps-binding are testable.
			_ = db.BindNode("node-x", "acct-1")

			// Re-hydrate: AllNodes returns the row with its fields round-tripped.
			all, err := db.AllNodes()
			if err != nil || len(all) != 1 {
				t.Fatalf("AllNodes = %d rows, %v; want 1", len(all), err)
			}
			if all[0].NodeID != "node-x" || !all[0].Confidential || all[0].Reg.Region != "us" {
				t.Fatalf("node round-trip mismatch: %+v", all[0])
			}
			if all[0].RegisteredAt != 500 {
				t.Errorf("RegisteredAt = %d, want 500", all[0].RegisteredAt)
			}

			// Refresh: a re-upsert with a NEW registered_at preserves the original, and
			// refreshes last_seen.
			rec.RegisteredAt = 9999
			rec.LastSeen = 2000
			if err := db.UpsertNode(rec); err != nil {
				t.Fatalf("re-UpsertNode: %v", err)
			}
			all, _ = db.AllNodes()
			if all[0].RegisteredAt != 500 {
				t.Errorf("registered_at not preserved on refresh: %d, want 500", all[0].RegisteredAt)
			}
			if all[0].LastSeen != 2000 {
				t.Errorf("last_seen not refreshed: %d, want 2000", all[0].LastSeen)
			}

			// TouchNode bumps last_seen without a re-register; unknown node is a no-op.
			if err := db.TouchNode("node-x", time.Unix(3000, 0)); err != nil {
				t.Fatalf("TouchNode: %v", err)
			}
			if err := db.TouchNode("ghost", time.Unix(3000, 0)); err != nil {
				t.Fatalf("TouchNode(unknown) should be a no-op, got %v", err)
			}
			all, _ = db.AllNodes()
			if all[0].LastSeen != 3000 {
				t.Errorf("TouchNode last_seen = %d, want 3000", all[0].LastSeen)
			}

			// Account scoping: the node is listed under its account, not others.
			nodes, _ := db.NodesOfAccount("acct-1")
			if len(nodes) != 1 || nodes[0] != "node-x" {
				t.Errorf("NodesOfAccount(acct-1) = %v, want [node-x]", nodes)
			}
			if other, _ := db.NodesOfAccount("acct-2"); len(other) != 0 {
				t.Errorf("NodesOfAccount(acct-2) = %v, want empty", other)
			}

			// DeleteNode removes the registry row but NOT the owner binding.
			if err := db.DeleteNode("node-x"); err != nil {
				t.Fatalf("DeleteNode: %v", err)
			}
			if all, _ := db.AllNodes(); len(all) != 0 {
				t.Errorf("AllNodes after delete = %d, want 0", len(all))
			}
			if acct, ok, _ := db.AccountOfNode("node-x"); !ok || acct != "acct-1" {
				t.Errorf("owner binding should survive DeleteNode: acct=%q ok=%v", acct, ok)
			}
		})
	}
}

// TestOfferOverrideCRUD covers the owner-authored price override on BOTH backends:
// upsert + update-in-place, (node,model) lookup, the time-of-use schedule round-trip,
// owner-scoped listing, and owner-scoped clear (a wrong owner can neither clear nor
// list another account's override).
func TestOfferOverrideCRUD(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			ov := OfferOverride{
				Owner: "owner-a", NodeID: "n1", Model: "qwen",
				PriceIn: 0.10, PriceOut: 0.20, UpdatedAt: 100,
				Schedule: []protocol.PriceWindow{{Start: "22:00", End: "06:00", Free: true}},
			}
			if err := db.SetOfferOverride(ov); err != nil {
				t.Fatalf("SetOfferOverride: %v", err)
			}
			// A second owner's override (different node) proves owner scoping.
			_ = db.SetOfferOverride(OfferOverride{Owner: "owner-b", NodeID: "n2", Model: "llama", PriceIn: 1})

			got, ok, err := db.OfferOverride("n1", "qwen")
			if err != nil || !ok {
				t.Fatalf("OfferOverride lookup ok=%v err=%v", ok, err)
			}
			if got.PriceIn != 0.10 || got.PriceOut != 0.20 || got.Owner != "owner-a" {
				t.Fatalf("override round-trip mismatch: %+v", got)
			}
			if len(got.Schedule) != 1 || !got.Schedule[0].Free || got.Schedule[0].Start != "22:00" {
				t.Fatalf("schedule round-trip mismatch: %+v", got.Schedule)
			}
			if _, ok, _ := db.OfferOverride("n1", "absent"); ok {
				t.Errorf("OfferOverride(absent model) ok=true, want false")
			}

			// Upsert in place: same (node,model) updates, never duplicates.
			ov.PriceIn = 0.99
			ov.UpdatedAt = 200
			if err := db.SetOfferOverride(ov); err != nil {
				t.Fatalf("re-SetOfferOverride: %v", err)
			}
			got, _, _ = db.OfferOverride("n1", "qwen")
			if got.PriceIn != 0.99 || got.UpdatedAt != 200 {
				t.Errorf("upsert did not update in place: %+v", got)
			}

			// Owner-scoped listing.
			list, _ := db.OverridesByOwner("owner-a")
			if len(list) != 1 || list[0].NodeID != "n1" {
				t.Fatalf("OverridesByOwner(owner-a) = %+v, want only n1", list)
			}

			// Owner-scoped clear: a wrong owner can't clear it.
			if ok, _ := db.ClearOfferOverride("intruder", "n1", "qwen"); ok {
				t.Errorf("ClearOfferOverride by wrong owner succeeded - must be owner-scoped")
			}
			if ok, _ := db.ClearOfferOverride("owner-a", "n1", "qwen"); !ok {
				t.Errorf("ClearOfferOverride by owner failed")
			}
			if _, ok, _ := db.OfferOverride("n1", "qwen"); ok {
				t.Errorf("override still present after clear")
			}
		})
	}
}

// TestLedgerOfAndDeriveBalance covers the ledger read + balance derivation on BOTH
// backends: a topup then a settle leave two wallet-affecting rows; LedgerOf returns
// them newest-first, the kind filter narrows to one, and DeriveBalance reconstructs
// the wallet balance purely from the ledger (the authoritative cross-check).
func TestLedgerOfAndDeriveBalance(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			user := "u-led"
			if _, err := db.AddCredits(user, 10); err != nil { // KindTopup +10
				t.Fatal(err)
			}
			_ = db.BindNode("n-led", "acct-led")
			// A real settle posts a KindSpend -3 (and a node earning, different holder).
			if _, err := db.Settle(user, "n-led", 3, 1, protocol.UsageReceipt{
				RequestID: "rq-led", Model: "m", TS: 1,
			}); err != nil {
				t.Fatal(err)
			}

			// Full ledger for the wallet, newest-first.
			rows, err := db.LedgerOf(user, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) < 2 {
				t.Fatalf("LedgerOf = %d rows, want >=2 (topup + spend)", len(rows))
			}
			for i := 1; i < len(rows); i++ {
				if rows[i-1].ID < rows[i].ID {
					t.Errorf("LedgerOf not newest-first at %d", i)
				}
			}

			// Kind filter: only the spend rows.
			spends, _ := db.LedgerOf(user, []string{KindSpend}, 0)
			if len(spends) != 1 || spends[0].Kind != KindSpend {
				t.Fatalf("LedgerOf(spend) = %+v, want exactly one spend", spends)
			}

			// DeriveBalance reconstructs the balance from the ledger: 10 - 3 = 7.
			bal, err := db.DeriveBalance(user)
			if err != nil {
				t.Fatal(err)
			}
			if !approx(bal, 7) {
				t.Errorf("DeriveBalance = %v, want 7 (10 topup - 3 spend)", bal)
			}
		})
	}
}
