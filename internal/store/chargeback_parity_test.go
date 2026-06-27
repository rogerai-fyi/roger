package store

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// spend runs one Hold+Finalize through the Store interface so the lot is attributed to
// `user` (via the receipt) and the operator behind `node` earns `ownerShare`. cost is the
// CONSUMER amount billed (the unit a dispute is denominated in); it can differ from the
// operator gross when a platform fee applies. The wallet is assumed already funded.
func spend(t *testing.T, db Store, user, node, reqID string, cost, ownerShare float64, ts int64) {
	t.Helper()
	if ok, err := db.Hold(user, cost); err != nil || !ok {
		t.Fatalf("Hold(%g) ok=%v err=%v", cost, ok, err)
	}
	if _, err := db.Finalize(user, node, cost, cost, ownerShare, protocol.UsageReceipt{
		RequestID: reqID, Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: ts,
	}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

// TestChargebackWalletRecencyParity runs the no-request-id dispute clawback (newest lot
// first, pro-rata on the overshooting lot, fee-aware cost cap, platform-loss remainder,
// and dispute-id idempotency) on BOTH Mem and the real Postgres money path, so the SQL
// ChargebackLineage/Chargeback branches are exercised, not just the reference Mem impl.
func TestChargebackWalletRecencyParity(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			_ = db.BindNode("n", "op1")
			if _, err := db.AddCredits("alice", 1000); err != nil {
				t.Fatal(err)
			}
			if _, err := db.AddCredits("bob", 1000); err != nil {
				t.Fatal(err)
			}
			// alice: older $100 lot (op share 70) then a newer $100 lot (op share 70); a 30%
			// platform fee makes cost(100) != gross(70). bob's lot must never be touched.
			spend(t, db, "alice", "n", "a-old", 100, 70, 1000)
			spend(t, db, "alice", "n", "a-new", 100, 70, 2000)
			spend(t, db, "bob", "n", "b1", 100, 70, 1500)

			// Dispute $100, no request id: claw alice's newest lot only (its $100 cost covers
			// the dispute), recovering the operator's $70 share; the $30 fee is a platform loss.
			clawed, err := db.Chargeback("dp1", "alice", "", 100, now)
			if err != nil {
				t.Fatalf("[%s] Chargeback: %v", name, err)
			}
			if !approx(clawed, 70) {
				t.Errorf("[%s] clawed = %v, want 70 (operator share of the one disputed $100 lot)", name, clawed)
			}
			// alice's wallet was debited the full disputed $100.
			if bal, _ := db.PeekBalance("alice"); !approx(bal, 1000-100-100-100) {
				t.Errorf("[%s] alice balance = %v, want %v", name, bal, 1000-100-100-100)
			}
			// op1 payable: bob's 70 + alice's surviving older lot 70 = 140 (newest clawed).
			if s, _ := db.EarningSplitOf("op1", now); !approx(s.Payable, 140) {
				t.Errorf("[%s] op1 payable = %v, want 140 (bob 70 + alice old-lot 70 survive)", name, s.Payable)
			}
			// A platform_loss ledger row recorded the $30 fee shortfall.
			led, _ := db.LedgerOf("platform", []string{KindPlatformLoss}, 10)
			if len(led) != 1 || !approx(led[0].Amount, -30) {
				t.Errorf("[%s] platform_loss ledger = %+v, want one -30 row", name, led)
			}
			// A consumer chargeback ledger row debits the disputed $100.
			cled, _ := db.LedgerOf("alice", []string{KindChargeback}, 10)
			if len(cled) != 1 || !approx(cled[0].Amount, -100) {
				t.Errorf("[%s] consumer chargeback ledger = %+v, want one -100 row", name, cled)
			}

			// Idempotent: a redelivery of dp1 claws nothing and leaves the wallet unchanged.
			balBefore, _ := db.PeekBalance("alice")
			res2, err := db.ChargebackLineage("dp1", "alice", "", 100, now)
			if err != nil {
				t.Fatalf("[%s] redeliver: %v", name, err)
			}
			if !res2.AlreadyHandled || res2.Clawed != 0 {
				t.Errorf("[%s] redelivered dispute = %+v, want AlreadyHandled / clawed 0", name, res2)
			}
			if bal, _ := db.PeekBalance("alice"); bal != balBefore {
				t.Errorf("[%s] redelivered dispute changed balance %v -> %v", name, balBefore, bal)
			}
		})
	}
}

// TestChargebackPartialProRataParity locks the partial-dispute pro-rata claw on BOTH
// backends: a dispute SMALLER than a single lot's consumer cost recovers only the
// operator's proportional share (the lot is kept, its gross reduced), with the fee share
// booked as a platform loss and conservation intact.
func TestChargebackPartialProRataParity(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			_ = db.BindNode("n", "op1")
			if _, err := db.AddCredits("alice", 1000); err != nil {
				t.Fatal(err)
			}
			// One $200-cost request; operator earns $140 (30% fee).
			spend(t, db, "alice", "n", "r_big", 200, 140, 1000)

			// Dispute $100 (< the lot's $200 cost): claw only the operator's pro-rata share
			// 140*(100/200)=70; the $30 fee share is a platform loss.
			res, err := db.ChargebackLineage("dp_partial", "alice", "", 100, now)
			if err != nil {
				t.Fatal(err)
			}
			if !approx(res.Clawed, 70) {
				t.Errorf("[%s] clawed = %v, want 70 (pro-rata share, not the whole lot)", name, res.Clawed)
			}
			if !approx(res.PlatformLoss, 30) {
				t.Errorf("[%s] platform loss = %v, want 30", name, res.PlatformLoss)
			}
			if res.Clawed > 100+1e-9 {
				t.Errorf("[%s] recovered %v exceeds the disputed 100 (over-claw)", name, res.Clawed)
			}
			// The lot survives with its gross reduced: operator keeps 140-70=70 payable.
			if s, _ := db.EarningSplitOf("op1", now); !approx(s.Payable, 70) {
				t.Errorf("[%s] op1 payable after partial claw = %v, want 70 (lot kept, gross reduced)", name, s.Payable)
			}
		})
	}
}

// TestChargebackPaidLotReversalParity locks the post-payout dispute path on BOTH backends:
// a dispute whose attributable lot was ALREADY PAID OUT yields a Reversal carrying the
// payout's Stripe transfer id (so the broker can reverse it) plus a payout_reversed ledger
// row, with Clawed 0 (recovery is via the reversal), and is idempotent on the dispute id.
func TestChargebackPaidLotReversalParity(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			_ = db.BindNode("n", "acct1")
			if _, err := db.AddCredits("u", 100); err != nil {
				t.Fatal(err)
			}
			// One $30 lot, no fee (cost==gross==30), then pay it out so it is PAID.
			spend(t, db, "u", "n", "r1", 30, 30, 1000)
			pay, ok, _, err := db.RequestPayout("acct1", now, 25)
			if err != nil || !ok {
				t.Fatalf("[%s] RequestPayout ok=%v err=%v", name, ok, err)
			}
			if err := db.SettlePayout(pay.ID, "tr_paid_1"); err != nil {
				t.Fatalf("[%s] SettlePayout: %v", name, err)
			}

			res, err := db.ChargebackLineage("dp_paid", "u", "", 30, now)
			if err != nil {
				t.Fatalf("[%s] ChargebackLineage: %v", name, err)
			}
			if len(res.Reversals) != 1 {
				t.Fatalf("[%s] want 1 reversal, got %d (res=%+v)", name, len(res.Reversals), res)
			}
			rv := res.Reversals[0]
			if rv.TransferID != "tr_paid_1" || !approx(rv.Amount, 30) || rv.AccountID != "acct1" {
				t.Errorf("[%s] reversal = %+v, want transfer tr_paid_1 / amount 30 / acct1", name, rv)
			}
			if res.Clawed != 0 {
				t.Errorf("[%s] Clawed = %v, want 0 (recovered via reversal)", name, res.Clawed)
			}
			if res.PlatformLoss != 0 {
				t.Errorf("[%s] PlatformLoss = %v, want 0 (fully recovered)", name, res.PlatformLoss)
			}
			led, _ := db.LedgerOf("acct1", []string{KindPayoutReversed}, 10)
			if len(led) != 1 || !approx(led[0].Amount, -30) {
				t.Errorf("[%s] payout_reversed ledger = %+v, want one -30 row", name, led)
			}
			// Idempotent redelivery.
			res2, _ := db.ChargebackLineage("dp_paid", "u", "", 30, now)
			if !res2.AlreadyHandled || len(res2.Reversals) != 0 {
				t.Errorf("[%s] redelivered = %+v, want AlreadyHandled / no reversals", name, res2)
			}
		})
	}
}

// TestChargebackExplicitRequestParity covers the explicit-request-id dispute path on BOTH
// backends: only the named request's lots are clawed WHOLE (no per-lot amount cap), an
// unrelated request by the same consumer survives, and the disputed `amount` not covered
// by the clawed gross is still booked as a platform loss.
func TestChargebackExplicitRequestParity(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			_ = db.BindNode("n", "opx")
			if _, err := db.AddCredits("u", 1000); err != nil {
				t.Fatal(err)
			}
			spend(t, db, "u", "n", "req-target", 50, 35, 1000)
			spend(t, db, "u", "n", "req-other", 80, 56, 2000)

			// Dispute the single named request for $50: its whole $35 operator gross is clawed
			// (lots are clawed whole on the explicit path), and the $15 of the disputed amount
			// the gross did not cover is booked as a platform loss.
			clawed, err := db.Chargeback("dp_req", "u", "req-target", 50, now)
			if err != nil {
				t.Fatalf("[%s] Chargeback: %v", name, err)
			}
			if !approx(clawed, 35) {
				t.Errorf("[%s] clawed = %v, want 35 (whole named-request lot)", name, clawed)
			}
			// The OTHER request's lot survives intact.
			if s, _ := db.EarningSplitOf("opx", now); !approx(s.Payable, 56) {
				t.Errorf("[%s] opx payable = %v, want 56 (req-other survives)", name, s.Payable)
			}
			// The uncovered $15 (amount 50 - gross 35) is a platform loss.
			led, _ := db.LedgerOf("platform", []string{KindPlatformLoss}, 10)
			if len(led) != 1 || !approx(led[0].Amount, -15) {
				t.Errorf("[%s] platform_loss ledger = %+v, want one -15 row", name, led)
			}
		})
	}
}
