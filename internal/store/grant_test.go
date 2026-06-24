package store

import (
	"testing"
	"time"
)

// TestMemGrantCRUD covers the Mem grant surface: create, secret-hash lookup,
// owner-scoped list/revoke/update, and the day/month usage rollup.
func TestMemGrantCRUD(t *testing.T) {
	m := NewMem()
	g := Grant{ID: "grant_a", SecretHash: "hash_a", Owner: "ownerpk", Label: "petlings", Free: true, DailyCap: 1000}
	if err := m.CreateGrant(g); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, ok, err := m.GrantBySecretHash("hash_a")
	if err != nil || !ok {
		t.Fatalf("by-secret: ok=%v err=%v", ok, err)
	}
	if got.Owner != "ownerpk" || !got.Free || got.Label != "petlings" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if _, ok, _ := m.GrantBySecretHash("nope"); ok {
		t.Fatalf("unknown hash should not resolve")
	}

	list, _ := m.GrantsByOwner("ownerpk")
	if len(list) != 1 {
		t.Fatalf("GrantsByOwner = %d, want 1", len(list))
	}
	if other, _ := m.GrantsByOwner("someoneelse"); len(other) != 0 {
		t.Fatalf("owner scoping leaked: %d", len(other))
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

	// Update applies a patch (owner-scoped).
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
