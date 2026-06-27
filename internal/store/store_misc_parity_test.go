package store

import (
	"testing"
	"time"
)

// TestStoreMissAndToggleParity covers the not-found no-ops and the node-hold toggle on
// BOTH backends: UpdateAccount/SetConnect on an unknown login, SettlePayout on an unknown
// id, SeedStatus with no cap configured (unlimited -> remaining -1), and SetNodeRecountHold
// set->clear (the DELETE arm).
func TestStoreMissAndToggleParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			// Unknown login: UpdateAccount reports not-found; SetConnect is a clean no-op.
			if _, ok, err := db.UpdateAccount("ghost-login", "e@x.com"); err != nil || ok {
				t.Errorf("[%s] UpdateAccount(ghost) ok=%v err=%v, want false/nil", name, ok, err)
			}
			if err := db.SetConnect("ghost-login", "acct_x", "active"); err != nil {
				t.Errorf("[%s] SetConnect(ghost) = %v, want nil", name, err)
			}
			// Unknown payout id: SettlePayout is a no-op (idempotent / nothing to settle).
			if err := db.SettlePayout(987654321, "tr_none"); err != nil {
				t.Errorf("[%s] SettlePayout(unknown) = %v, want nil", name, err)
			}

			// No seed cap configured: SeedStatus reports unlimited (remaining -1).
			if s, l, r, err := db.SeedStatus(); err != nil || r != -1 {
				t.Errorf("[%s] SeedStatus(no cap) = %d/%d/%d (err %v), want remaining -1", name, s, l, r, err)
			}

			// Node recount hold: set then clear; RecountHeldNodes reflects each step.
			if err := db.SetNodeRecountHold("nh", true); err != nil {
				t.Fatal(err)
			}
			if held, _ := db.RecountHeldNodes(); !held["nh"] {
				t.Errorf("[%s] nh not held after set", name)
			}
			if err := db.SetNodeRecountHold("nh", false); err != nil { // DELETE arm
				t.Fatal(err)
			}
			if held, _ := db.RecountHeldNodes(); held["nh"] {
				t.Errorf("[%s] nh still held after clear", name)
			}
			_ = db.Close()
		})
	}
}

// TestSeedStatusOverflowParity covers the SeedStatus remaining-clamp arm on BOTH backends:
// when the seeded count already exceeds a later, smaller cap, remaining clamps to 0 (never
// negative).
func TestSeedStatusOverflowParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			db.SetSeedLimit(5)
			if _, seeded, _ := db.SeedOnce("sw1", 10); !seeded {
				t.Fatal("sw1 should seed under the cap")
			}
			if _, seeded, _ := db.SeedOnce("sw2", 10); !seeded {
				t.Fatal("sw2 should seed under the cap")
			}
			db.SetSeedLimit(1) // seeded(2) now exceeds the cap(1)
			s, l, r, err := db.SeedStatus()
			if err != nil {
				t.Fatal(err)
			}
			if s != 2 || l != 1 || r != 0 {
				t.Errorf("[%s] SeedStatus = %d/%d/%d, want 2/1/0 (remaining clamped to 0)", name, s, l, r)
			}
			_ = db.Close()
		})
	}
}

// TestStoreGuardsParity covers the conditional guard arms on BOTH backends: a Hold that
// exceeds the balance is refused (no debit), RequestPayout below the minimum is refused,
// WalletByCharge with an empty ref resolves not-found, and SetMonthlyCap with a negative
// value is clamped to 0 (unlimited).
func TestStoreGuardsParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			if _, err := db.AddCredits("g", 10); err != nil {
				t.Fatal(err)
			}
			// Hold exceeding the balance is refused; the balance is untouched.
			if ok, err := db.Hold("g", 50); err != nil || ok {
				t.Errorf("[%s] Hold(50 > bal 10) ok=%v err=%v, want false/nil", name, ok, err)
			}
			if bal, _ := db.PeekBalance("g"); !approx(bal, 10) {
				t.Errorf("[%s] balance after refused hold = %v, want 10 (untouched)", name, bal)
			}

			// RequestPayout below the minimum is refused with a reason and no payout.
			_ = db.BindNode("ng", "acct-g")
			now := time.Now()
			if p, ok, reason, err := db.RequestPayout("acct-g", now, 100); err != nil || ok || reason == "" || p.ID != 0 {
				t.Errorf("[%s] RequestPayout(min 100, nothing payable) = %+v ok=%v reason=%q err=%v, want refused", name, p, ok, reason, err)
			}

			// WalletByCharge with an empty ref resolves not-found.
			if w, c, ok, err := db.WalletByCharge(""); err != nil || ok || w != "" || c != 0 {
				t.Errorf("[%s] WalletByCharge(\"\") = %q,%v,%v,%v, want \"\",0,false,nil", name, w, c, ok, err)
			}

			// SetMonthlyCap negative -> stored as 0 (unlimited).
			if err := db.SetMonthlyCap("g", -7); err != nil {
				t.Fatal(err)
			}
			if cap, _ := db.MonthlyCapOf("g"); cap != 0 {
				t.Errorf("[%s] cap after SetMonthlyCap(-7) = %v, want 0 (clamped)", name, cap)
			}
			_ = db.Close()
		})
	}
}

// TestUpsertNodeReRegisterParity covers UpsertNode's re-register path on BOTH backends: a
// node first registered with RegisteredAt==0 gets a server-stamped time, and a later
// re-upsert preserves that original registration time while refreshing last_seen.
func TestUpsertNodeReRegisterParity(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			// First register with RegisteredAt==0 -> server stamps a non-zero time.
			if err := db.UpsertNode(NodeRecord{NodeID: "nre", LastSeen: 1000}); err != nil {
				t.Fatal(err)
			}
			all, err := db.AllNodes()
			if err != nil {
				t.Fatal(err)
			}
			var first NodeRecord
			for _, n := range all {
				if n.NodeID == "nre" {
					first = n
				}
			}
			if first.NodeID != "nre" || first.RegisteredAt == 0 {
				t.Fatalf("[%s] first register = %+v, want a stamped RegisteredAt", name, first)
			}

			// Re-upsert with a new last_seen: registration time is preserved.
			if err := db.UpsertNode(NodeRecord{NodeID: "nre", LastSeen: 2000, RegisteredAt: time.Now().Add(time.Hour).Unix()}); err != nil {
				t.Fatal(err)
			}
			all, _ = db.AllNodes()
			var second NodeRecord
			for _, n := range all {
				if n.NodeID == "nre" {
					second = n
				}
			}
			if second.RegisteredAt != first.RegisteredAt {
				t.Errorf("[%s] re-register RegisteredAt = %d, want preserved %d", name, second.RegisteredAt, first.RegisteredAt)
			}
			if second.LastSeen != 2000 {
				t.Errorf("[%s] re-register LastSeen = %d, want 2000 (refreshed)", name, second.LastSeen)
			}
			_ = db.Close()
		})
	}
}
