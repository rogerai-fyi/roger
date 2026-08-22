package store

import (
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

// The self-relayed stamp is EVIDENCE on the money object: one account paid on both sides of an
// edge split - 70% for serving the request and 10% for relaying it - which is 80% of what an
// arms-length consumer paid landing in one place. Nothing withholds it and nothing may: under
// per-request relay selection your own node behind your own relay is frequently the correct
// placement (docs/relay-selection-design.md section 6.7). It is recorded so a policy can be
// written later against data that already exists.
//
// Run on BOTH stores, because a fact that survives in mem and evaporates in Postgres is not a
// fact - and because the whole point of storing it is that somebody reads it back months from
// now, from the durable one.
func TestSelfRelayedRollupParity(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			if _, err := db.AddCredits("alice", 1000); err != nil {
				t.Fatal(err)
			}
			// req-1: the Station owner and the Tower operator are ONE account ("bothends").
			if ok, err := db.HoldFor("alice", "req-1", 100); err != nil || !ok {
				t.Fatalf("HoldFor: ok=%v err=%v", ok, err)
			}
			if _, err := db.SettleEdge("alice", "st-1", "bothends", "tower:tw-1", "bothends",
				100, 70, 10, true, protocol.UsageReceipt{RequestID: "req-1", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: now.Unix()}); err != nil {
				t.Fatalf("SettleEdge self-relayed: %v", err)
			}
			// req-2: the SAME Station owner, carried by somebody else's Tower. Arms length.
			if ok, err := db.HoldFor("alice", "req-2", 100); err != nil || !ok {
				t.Fatalf("HoldFor: ok=%v err=%v", ok, err)
			}
			if _, err := db.SettleEdge("alice", "st-1", "bothends", "tower:tw-2", "someone-else",
				100, 70, 10, false, protocol.UsageReceipt{RequestID: "req-2", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: now.Unix()}); err != nil {
				t.Fatalf("SettleEdge arms-length: %v", err)
			}

			// THE MONEY IS UNCHANGED BY THE STAMP. 140 for serving two requests, 10 for relaying
			// one of them - the self-relayed request paid exactly what it would have paid anyway.
			if s, _ := db.EarningSplitOf("bothends", now.Add(time.Hour)); !approx(s.Payable, 150) {
				t.Errorf("[%s] bothends payable = %v, want 150 (70+70 serving, 10 relaying)", name, s.Payable)
			}
			if s, _ := db.EarningSplitOf("someone-else", now.Add(time.Hour)); !approx(s.Payable, 10) {
				t.Errorf("[%s] arms-length tower payable = %v, want 10", name, s.Payable)
			}

			// THE EVIDENCE. Only req-1's two lots are stamped, and both of them are: the serving
			// 70 and the relaying 10. req-2's serving lot, on the same node and the same account,
			// is not - which is what makes this a per-REQUEST fact rather than a per-node one.
			got, err := db.SelfRelayedRollup("bothends")
			if err != nil {
				t.Fatalf("SelfRelayedRollup: %v", err)
			}
			byNode := map[string]EarningRollup{}
			for _, r := range got {
				byNode[r.Key] = r
			}
			if len(byNode) != 2 {
				t.Fatalf("[%s] self-relayed rollup = %+v, want exactly the two lots of req-1", name, got)
			}
			if r := byNode["st-1"]; !approx(r.Amount, 70) || r.Lots != 1 {
				t.Errorf("[%s] serving side = %+v, want 70 across 1 lot", name, r)
			}
			if r := byNode["tower:tw-1"]; !approx(r.Amount, 10) || r.Lots != 1 {
				t.Errorf("[%s] relaying side = %+v, want 10 across 1 lot", name, r)
			}
			// The arms-length operator has no self-relayed earnings at all, and the answer is an
			// empty rollup rather than an error or a nil surprise.
			other, err := db.SelfRelayedRollup("someone-else")
			if err != nil {
				t.Fatalf("SelfRelayedRollup(someone-else): %v", err)
			}
			if len(other) != 0 {
				t.Errorf("[%s] arms-length operator has self-relayed earnings: %+v", name, other)
			}

			// AND IT IS DIVISIBLE BY THE ROLLUP IT MIRRORS, which is the only reason it is shaped
			// like this: "what fraction of this node's earnings were self-relayed" has to be one
			// subtraction away, not a second schema.
			_, byNodeAll, err := db.EarningRollups("bothends")
			if err != nil {
				t.Fatalf("EarningRollups: %v", err)
			}
			all := map[string]float64{}
			for _, r := range byNodeAll {
				all[r.Key] = r.Amount
			}
			if !approx(all["st-1"], 140) {
				t.Errorf("[%s] total serving = %v, want 140", name, all["st-1"])
			}
			if !approx(byNode["st-1"].Amount/all["st-1"], 0.5) {
				t.Errorf("[%s] self-relayed fraction of the serving node = %v, want 0.5",
					name, byNode["st-1"].Amount/all["st-1"])
			}
		})
	}
}

// A clawed-back lot is not evidence of an operator's current concentration - it is money that
// went away again - so it leaves the rollup exactly as it leaves EarningRollups. Same predicate,
// same answer, on both stores.
func TestSelfRelayedRollupDropsClawedLots(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			if _, err := db.AddCredits("alice", 1000); err != nil {
				t.Fatal(err)
			}
			if ok, err := db.HoldFor("alice", "req-1", 100); err != nil || !ok {
				t.Fatalf("HoldFor: ok=%v err=%v", ok, err)
			}
			if _, err := db.SettleEdge("alice", "st-1", "bothends", "tower:tw-1", "bothends",
				100, 70, 10, true, protocol.UsageReceipt{RequestID: "req-1", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: now.Unix()}); err != nil {
				t.Fatalf("SettleEdge: %v", err)
			}
			if got, err := db.SelfRelayedRollup("bothends"); err != nil || len(got) != 2 {
				t.Fatalf("[%s] before the refund: %+v (err %v)", name, got, err)
			}
			if _, _, err := db.RefundLineage("rf-1", []string{"req-1"}, "alice", "req-1", 100, now); err != nil {
				t.Fatalf("RefundLineage: %v", err)
			}
			if got, err := db.SelfRelayedRollup("bothends"); err != nil || len(got) != 0 {
				t.Errorf("[%s] a refunded request is not concentration evidence: %+v (err %v)", name, got, err)
			}
		})
	}
}
