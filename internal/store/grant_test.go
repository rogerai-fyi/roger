package store

import (
	"testing"
	"time"
)

// TestGrantCRUD covers the grant surface on BOTH backends: create, secret-hash
// lookup, owner-scoped list/revoke/update, the JSON node/model scopes round-trip,
// and the day/month usage rollup.
func TestGrantCRUD(t *testing.T) {
	for name, m := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			g := Grant{ID: "grant_a", SecretHash: "hash_a", Owner: "ownerpk", Label: "petlings",
				Free: true, DailyCap: 1000, Nodes: []string{"n1", "n2"}, Models: []string{"qwen"}}
			if err := m.CreateGrant(g); err != nil {
				t.Fatalf("create: %v", err)
			}
			// A second owner's grant proves owner-scoping.
			_ = m.CreateGrant(Grant{ID: "grant_b", SecretHash: "hash_b", Owner: "someoneelse"})

			got, ok, err := m.GrantBySecretHash("hash_a")
			if err != nil || !ok {
				t.Fatalf("by-secret: ok=%v err=%v", ok, err)
			}
			if got.Owner != "ownerpk" || !got.Free || got.Label != "petlings" {
				t.Fatalf("round-trip mismatch: %+v", got)
			}
			if len(got.Nodes) != 2 || len(got.Models) != 1 || got.Models[0] != "qwen" {
				t.Fatalf("scope round-trip mismatch: nodes=%v models=%v", got.Nodes, got.Models)
			}
			if _, ok, _ := m.GrantBySecretHash("nope"); ok {
				t.Fatalf("unknown hash should not resolve")
			}

			list, _ := m.GrantsByOwner("ownerpk")
			if len(list) != 1 || list[0].ID != "grant_a" {
				t.Fatalf("GrantsByOwner = %+v, want only grant_a", list)
			}

			// Revoke is owner-scoped: a wrong owner cannot revoke.
			if ok, _ := m.SetGrantRevoked("grant_a", "intruder", true); ok {
				t.Fatalf("revoke by wrong owner should fail")
			}
			if ok, _ := m.SetGrantRevoked("grant_a", "ownerpk", true); !ok {
				t.Fatalf("revoke by owner should succeed")
			}
			if g2, _, _ := m.GrantBySecretHash("hash_a"); !g2.Revoked {
				t.Fatalf("grant should be revoked")
			}

			// Update is owner-scoped too: wrong owner is a no-op.
			if _, ok, _ := m.UpdateGrant("grant_a", "intruder", GrantPatch{}); ok {
				t.Fatalf("update by wrong owner should fail")
			}
			cap := int64(5000)
			upd, ok, _ := m.UpdateGrant("grant_a", "ownerpk", GrantPatch{DailyCap: &cap})
			if !ok || upd.DailyCap != 5000 {
				t.Fatalf("update daily cap: ok=%v cap=%d", ok, upd.DailyCap)
			}

			// Usage rollup accumulates per UTC window.
			now := time.Now()
			if err := m.AddGrantUsage("grant_a", 120, now); err != nil {
				t.Fatalf("add usage: %v", err)
			}
			_ = m.AddGrantUsage("grant_a", 80, now)
			u, _ := m.GrantUsageOf("grant_a", now)
			if u.DayTokens != 200 || u.MonthTokens != 200 {
				t.Fatalf("usage rollup = %+v, want 200/200", u)
			}
		})
	}
}

// TestGrantUpdateAllFields exercises every field of applyPatch through UpdateGrant
// on both backends - a full patch must change all editable fields at once.
func TestGrantUpdateAllFields(t *testing.T) {
	for name, m := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			_ = m.CreateGrant(Grant{ID: "g", SecretHash: "h", Owner: "o"})
			label, free := "renamed", false
			nodes, models := []string{"nx"}, []string{"mx", "my"}
			pin, pout, rpm, burst := 0.11, 0.22, 33.0, 44.0
			day, mon, exp := int64(7), int64(70), int64(1893456000)
			rev := true
			upd, ok, err := m.UpdateGrant("g", "o", GrantPatch{
				Label: &label, Nodes: &nodes, Models: &models, Free: &free,
				PriceIn: &pin, PriceOut: &pout, RPM: &rpm, Burst: &burst,
				DailyCap: &day, MonthlyCap: &mon, ExpiresAt: &exp, Revoked: &rev,
			})
			if err != nil || !ok {
				t.Fatalf("UpdateGrant ok=%v err=%v", ok, err)
			}
			if upd.Label != "renamed" || upd.Free || len(upd.Nodes) != 1 || len(upd.Models) != 2 ||
				upd.PriceIn != 0.11 || upd.PriceOut != 0.22 || upd.RPM != 33 || upd.Burst != 44 ||
				upd.DailyCap != 7 || upd.MonthlyCap != 70 || upd.ExpiresAt != exp || !upd.Revoked {
				t.Fatalf("full patch not fully applied: %+v", upd)
			}
		})
	}
}

// TestGrantPriceAndExpiry covers the free/self $0 rule and expiry.
func TestGrantPriceAndExpiry(t *testing.T) {
	free := Grant{Free: true, PriceIn: 9, PriceOut: 9}
	if in, out := free.GrantPrice(); in != 0 || out != 0 {
		t.Fatalf("free grant price = %v/%v, want 0/0", in, out)
	}
	self := Grant{Self: true, PriceIn: 9, PriceOut: 9}
	if in, out := self.GrantPrice(); in != 0 || out != 0 {
		t.Fatalf("self grant price = %v/%v, want 0/0", in, out)
	}
	priced := Grant{PriceIn: 0.2, PriceOut: 0.5}
	if in, out := priced.GrantPrice(); in != 0.2 || out != 0.5 {
		t.Fatalf("priced grant = %v/%v", in, out)
	}
	g := Grant{ExpiresAt: time.Now().Add(-time.Hour).Unix()}
	if !g.Expired(time.Now()) {
		t.Fatalf("past-expiry grant should be expired")
	}
	if (Grant{}).Expired(time.Now()) {
		t.Fatalf("zero expiry = never")
	}
}
