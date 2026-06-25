package store

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// enforce_test.go covers the billing-enforcement + owner-keyed anti-abuse store
// additions: the KindAdjust audit row, adjusted (broker) counts on the Entry, the
// owner-level promotion hold, and the durable owner ban + evidence-bound strikes.

func ledgerKinds(m *Mem, holder string) map[string]int {
	out := map[string]int{}
	for _, r := range m.ledger {
		if r.Holder == holder {
			out[r.Kind]++
		}
	}
	return out
}

// TestBilledTokensCapsBothAxes: billedTokens records min(claim, broker) on each axis,
// never inflating a claim (a broker count of 0 or above the claim is ignored).
func TestBilledTokensCapsBothAxes(t *testing.T) {
	cases := []struct {
		name              string
		claimIn, claimOut int
		brokIn, brokOut   int
		wantIn, wantOut   int
	}{
		{"both capped", 100, 250, 40, 80, 40, 80},
		{"no broker count", 100, 250, 0, 0, 100, 250},
		{"broker above claim ignored", 100, 250, 999, 999, 100, 250},
		{"only input capped", 100, 250, 40, 0, 40, 250},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := protocol.UsageReceipt{
				PromptTokens: c.claimIn, CompletionTokens: c.claimOut,
				BrokerPromptTokens: c.brokIn, BrokerCompletionTokens: c.brokOut,
			}
			gotIn, gotOut := billedTokens(rec)
			if gotIn != c.wantIn || gotOut != c.wantOut {
				t.Errorf("billedTokens = (%d,%d), want (%d,%d)", gotIn, gotOut, c.wantIn, c.wantOut)
			}
		})
	}
}

// TestFinalizeAdjustRowOnDownwardBilling: when the broker billed less than the node
// claimed on either axis, Finalize writes a KindAdjust audit row AND records the
// adjusted (broker) counts on the Entry. With no adjustment, no audit row appears.
func TestFinalizeAdjustRowOnDownwardBilling(t *testing.T) {
	m := NewMem()
	_, _ = m.BalanceOf("u", 1000)
	_, _ = m.Hold("u", 10)
	rec := protocol.UsageReceipt{
		RequestID: "r1", Model: "m", TS: time.Now().Unix(),
		PromptTokens: 100, CompletionTokens: 250,
		PriceIn: 0.20, PriceOut: 0.30,
		BrokerPromptTokens: 40, BrokerCompletionTokens: 80, // both capped down
	}
	// cost is computed by the caller on the adjusted counts.
	cost := rec.CostWith2(40, 80)
	if _, err := m.Finalize("u", "n", 10, cost, cost*0.7, rec); err != nil {
		t.Fatal(err)
	}
	if ledgerKinds(m, "u")[KindAdjust] != 1 {
		t.Errorf("want exactly 1 KindAdjust audit row, got %d", ledgerKinds(m, "u")[KindAdjust])
	}
	// The Entry must carry the ADJUSTED counts so dashboards/clawback use them.
	ents, _ := m.RecentByUser("u", 10)
	if len(ents) != 1 {
		t.Fatalf("want 1 entry, got %d", len(ents))
	}
	if ents[0].PromptTokens != 40 || ents[0].CompletionTokens != 80 {
		t.Errorf("Entry counts = (%d,%d), want adjusted (40,80)", ents[0].PromptTokens, ents[0].CompletionTokens)
	}
}

func TestFinalizeNoAdjustRowWhenFullClaimBilled(t *testing.T) {
	m := NewMem()
	_, _ = m.BalanceOf("u", 1000)
	_, _ = m.Hold("u", 10)
	rec := protocol.UsageReceipt{
		RequestID: "r1", Model: "m", TS: time.Now().Unix(),
		PromptTokens: 100, CompletionTokens: 250, PriceIn: 0.20, PriceOut: 0.30,
		// no broker counts -> full claim billed -> no adjustment
	}
	cost := rec.Cost()
	if _, err := m.Finalize("u", "n", 10, cost, cost*0.7, rec); err != nil {
		t.Fatal(err)
	}
	if n := ledgerKinds(m, "u")[KindAdjust]; n != 0 {
		t.Errorf("no adjustment -> want 0 KindAdjust rows, got %d", n)
	}
}

// TestOwnerStrikesAccrueEvidence: each strike appends an evidence row, the count grows,
// and StrikesByOwner returns the evidence newest-first (the surface that SHOWS the user).
func TestOwnerStrikesAccrueEvidence(t *testing.T) {
	m := NewMem()
	if n, _ := m.OwnerStrike("acct1", "empty-output", `{"request_id":"r1"}`, "k1"); n != 1 {
		t.Errorf("first strike count = %d, want 1", n)
	}
	if n, _ := m.OwnerStrike("acct1", "empty-output", `{"request_id":"r2"}`, "k2"); n != 2 {
		t.Errorf("second strike count = %d, want 2", n)
	}
	// Idempotent on idem key: a retried request does not double-strike.
	if n, _ := m.OwnerStrike("acct1", "empty-output", `{"request_id":"r2"}`, "k2"); n != 2 {
		t.Errorf("retried strike count = %d, want still 2 (idempotent)", n)
	}
	strikes, _ := m.StrikesByOwner("acct1", 0)
	if len(strikes) != 2 {
		t.Fatalf("want 2 evidence rows, got %d", len(strikes))
	}
	if strikes[0].Evidence == "" || strikes[0].Kind != "empty-output" {
		t.Errorf("strike evidence missing: %+v", strikes[0])
	}
}

// TestBanOwnerDurableAndRehydratable: BanOwner is idempotent, queryable via
// IsOwnerBanned, and surfaced in BannedOwners (for in-memory re-hydration).
func TestBanOwnerDurable(t *testing.T) {
	m := NewMem()
	if banned, _, _ := m.IsOwnerBanned("acct1"); banned {
		t.Fatal("acct1 must not start banned")
	}
	_ = m.BanOwner("acct1", "impossible-input", `{"request_id":"r1"}`)
	_ = m.BanOwner("acct1", "second-reason", `{}`) // idempotent: first reason wins
	banned, reason, _ := m.IsOwnerBanned("acct1")
	if !banned || reason != "impossible-input" {
		t.Errorf("IsOwnerBanned = (%v,%q), want (true,impossible-input)", banned, reason)
	}
	set, _ := m.BannedOwners()
	if _, ok := set["acct1"]; !ok {
		t.Error("BannedOwners must include acct1 (re-hydration source)")
	}
}

// TestAccountRecountHoldBlocksPromotion: an owner-level hold keeps ALL of that owner's
// lots from promoting held->payable, even after the release time passed, and survives a
// node-id change (a second node under the same owner is also held). Clearing it lets the
// lots promote.
func TestAccountRecountHoldBlocksPromotion(t *testing.T) {
	m := NewMem()
	m.policy.HoldDays = 0 // lots are releasable immediately
	_ = m.BindNode("nodeA", "acct1")
	_ = m.BindNode("nodeB", "acct1") // owner rotated to a new node id
	// Fund with REAL (non-seed) credits so the lots have a nonzero gross (seed-funded
	// spend mints no earning, P0-1).
	_, _ = m.AddCredits("u", 1000)

	settle := func(node, req string) {
		_, _ = m.Hold("u", 10)
		rec := protocol.UsageReceipt{RequestID: req, Model: "m", TS: time.Now().Unix(), PromptTokens: 1, CompletionTokens: 1}
		_, _ = m.Finalize("u", node, 10, 10, 7, rec)
	}
	settle("nodeA", "r1")
	settle("nodeB", "r2") // earnings under a DIFFERENT node id, same owner

	// Hold the OWNER (not the node) -> neither node's lots may promote.
	_ = m.SetAccountRecountHold("acct1", true)
	future := time.Now().Add(time.Hour)
	split, _ := m.EarningSplitOf("acct1", future)
	if split.Payable != 0 {
		t.Errorf("owner-held lots must NOT be payable, got payable=%v", split.Payable)
	}

	// Clear the owner hold -> lots promote on the next sweep.
	_ = m.SetAccountRecountHold("acct1", false)
	split, _ = m.EarningSplitOf("acct1", future)
	if split.Payable <= 0 {
		t.Errorf("after clearing the owner hold, lots must be payable, got payable=%v", split.Payable)
	}
}
