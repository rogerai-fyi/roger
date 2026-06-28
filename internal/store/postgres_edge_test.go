package store

import (
	"os"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// pgOnly returns a freshly-truncated real Postgres store, or skips when no DB is
// configured. Used by the Postgres-specific edge tests (raw-SQL setups, whitebox tx
// helpers) that have no Mem equivalent.
func pgOnly(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set; skipping Postgres-specific edge test")
	}
	return freshPostgres(t, dsn)
}

// TestNewPostgresPingFailsOnUnreachableDB: NewPostgres must fail (not return a half-open
// store) when the DB cannot be pinged. Targets the Ping error branch.
func TestNewPostgresPingFailsOnUnreachableDB(t *testing.T) {
	// 127.0.0.1:1 refuses immediately: sql.Open succeeds (lazy), Ping fails.
	if pg, err := NewPostgres("postgres://nouser:nopass@127.0.0.1:1/nodb?sslmode=disable"); err == nil {
		_ = pg.Close()
		t.Fatal("NewPostgres must fail to ping an unreachable DB")
	}
}

// TestRealEarnShareTxNoWallet (whitebox): when the wallet row is absent (the
// shouldn't-happen-post-debit defensive case), realEarnShareTx treats the cost as fully
// REAL and returns the whole owner share, consuming no seed.
func TestRealEarnShareTxNoWallet(t *testing.T) {
	pg := pgOnly(t)
	tx, err := pg.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	got, err := pg.realEarnShareTx(tx, "no-such-wallet", 10, 5)
	if err != nil {
		t.Fatalf("realEarnShareTx(no wallet) err = %v, want nil", err)
	}
	if got != 5 {
		t.Fatalf("realEarnShareTx(no wallet) = %v, want the full owner share 5", got)
	}
}

// TestSeedStatusNoCounterRow: with the seed_counter row absent, SeedStatus reports zero
// seeded (the ErrNoRows -> count=0 fallback) instead of erroring.
func TestSeedStatusNoCounterRow(t *testing.T) {
	pg := pgOnly(t)
	pg.SetSeedLimit(100)
	if _, err := pg.db.Exec(`DELETE FROM rogerai.seed_counter WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	seeded, limit, remaining, err := pg.SeedStatus()
	if err != nil {
		t.Fatalf("SeedStatus err = %v, want nil", err)
	}
	if seeded != 0 || limit != 100 || remaining != 100 {
		t.Fatalf("SeedStatus = (%d,%d,%d), want (0,100,100) with no counter row", seeded, limit, remaining)
	}
}

// TestAllNodesSkipsUndecodableRegRow: a node row whose reg JSON cannot decode into a
// NodeRegistration is SKIPPED (a single bad row never blocks startup re-hydration); the
// other rows still load.
func TestAllNodesSkipsUndecodableRegRow(t *testing.T) {
	pg := pgOnly(t)
	now := time.Now().Unix()
	if err := pg.UpsertNode(NodeRecord{NodeID: "good-1", Reg: protocol.NodeRegistration{NodeID: "good-1"}, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	if err := pg.UpsertNode(NodeRecord{NodeID: "bad-1", Reg: protocol.NodeRegistration{NodeID: "bad-1"}, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	// Valid JSONB, but a bare string can't decode into the NodeRegistration struct.
	if _, err := pg.db.Exec(`UPDATE rogerai.nodes SET reg='"corrupt"'::jsonb WHERE node_id=$1`, "bad-1"); err != nil {
		t.Fatal(err)
	}
	recs, err := pg.AllNodes()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range recs {
		got[r.NodeID] = true
	}
	if !got["good-1"] {
		t.Error("AllNodes dropped the good row")
	}
	if got["bad-1"] {
		t.Error("AllNodes must skip the row with undecodable reg JSON")
	}
}

// TestEarningsOfUnknownNode: an unknown node has zero earnings (the no-row fallback),
// on BOTH backends.
func TestEarningsOfUnknownNode(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			e, err := db.EarningsOf("never-earned")
			if err != nil {
				t.Fatal(err)
			}
			if e != 0 {
				t.Fatalf("[%s] EarningsOf(unknown) = %v, want 0", name, e)
			}
		})
	}
}

// TestRecentDefaultsLimit: RecentByNode with limit<=0 falls back to the default page size
// and still returns the seeded receipt.
func TestRecentDefaultsLimit(t *testing.T) {
	pg := pgOnly(t)
	serveAt(t, pg, "rl-user", "rl-node", "m", 1, 1, 5, 0, time.Now().Unix())
	e, err := pg.RecentByNode("rl-node", 0) // limit<=0 -> default
	if err != nil {
		t.Fatal(err)
	}
	if len(e) != 1 || e[0].Node != "rl-node" {
		t.Fatalf("RecentByNode(limit 0) = %d rows %+v, want the one seeded receipt", len(e), e)
	}
}

// TestSettleAndFinalizeNoWalletRollBack: settling/finalizing for a wallet that was never
// funded errors (no row to debit/credit) and rolls back cleanly — no orphan receipt.
func TestSettleAndFinalizeNoWalletRollBack(t *testing.T) {
	pg := pgOnly(t)
	if err := pg.BindNode("nw-node", "nw-acct"); err != nil {
		t.Fatal(err)
	}
	rec := protocol.UsageReceipt{RequestID: "nw-r", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: time.Now().Unix()}
	if _, err := pg.Settle("ghost-user", "nw-node", 10, 5, rec); err == nil {
		t.Fatal("Settle for a never-funded wallet must error (no row to debit)")
	}
	frec := protocol.UsageReceipt{RequestID: "nw-f", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: time.Now().Unix()}
	if _, err := pg.Finalize("ghost-user", "nw-node", 10, 10, 5, frec); err == nil {
		t.Fatal("Finalize for a never-funded wallet must error (no row to credit)")
	}
	// Both failed settles rolled back: the claimed receipt rows were undone.
	if e, _ := pg.RecentByNode("nw-node", 10); len(e) != 0 {
		t.Fatalf("a rolled-back settle/finalize must leave no receipt, got %d", len(e))
	}
}

// TestSettleToUnboundNodeMintsNoLot: a settle to a node with NO owner binding still debits
// the consumer, but mints NO earning lot (addLot's no-bound-account no-op) — there is no
// account to credit. Asserted on BOTH backends.
func TestSettleToUnboundNodeMintsNoLot(t *testing.T) {
	t.Setenv("ROGERAI_PAYOUT_HOLD_DAYS", "0")
	t.Setenv("ROGERAI_PAYOUT_RESERVE", "0")
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now().Add(time.Hour)
			if _, err := db.AddCredits("ub-user", 100); err != nil {
				t.Fatal(err)
			}
			rec := protocol.UsageReceipt{RequestID: "ub-r", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: now.Unix()}
			if _, err := db.Settle("ub-user", "unbound-node", 10, 7, rec); err != nil {
				t.Fatal(err)
			}
			if bal, _ := db.PeekBalance("ub-user"); !approx(bal, 90) {
				t.Errorf("[%s] consumer balance = %v, want 90 (paid despite no binding)", name, bal)
			}
			s, _ := db.EarningSplitOfNode("unbound-node", now)
			if !approx(s.Held, 0) || !approx(s.Payable, 0) {
				t.Errorf("[%s] unbound node split = held %v / payable %v, want 0/0 (no lot)", name, s.Held, s.Payable)
			}
		})
	}
}

// TestDownwardAdjustmentWritesAuditRow: when the broker billed FEWER tokens than the node
// claimed, the settle writes a KindAdjust audit row (money delta 0) — the provable record
// of the downward adjustment. Asserted on BOTH backends.
func TestDownwardAdjustmentWritesAuditRow(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := db.BindNode("adj-node", "adj-acct"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.AddCredits("adj-user", 100); err != nil {
				t.Fatal(err)
			}
			// Broker billed 40 prompt tokens vs the node's claimed 100 -> downward adjustment.
			rec := protocol.UsageReceipt{
				RequestID: "adj-r", Model: "m",
				PromptTokens: 100, CompletionTokens: 50, BrokerPromptTokens: 40,
				TS: time.Now().Unix(),
			}
			if _, err := db.Settle("adj-user", "adj-node", 10, 7, rec); err != nil {
				t.Fatal(err)
			}
			rows, err := db.LedgerOf("adj-user", []string{KindAdjust}, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 {
				t.Fatalf("[%s] want exactly one KindAdjust audit row, got %d", name, len(rows))
			}
			if rows[0].Amount != 0 {
				t.Errorf("[%s] adjust audit row money delta = %v, want 0", name, rows[0].Amount)
			}
		})
	}
}

// TestAccountOfNodeUnknown: an unbound node resolves to (.."",false,nil) — not found, no
// error — on BOTH backends (the ErrNoRows / no-binding arm).
func TestAccountOfNodeUnknown(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			acct, ok, err := db.AccountOfNode("never-bound")
			if err != nil {
				t.Fatal(err)
			}
			if ok || acct != "" {
				t.Fatalf("[%s] AccountOfNode(unbound) = (%q, ok=%v), want (\"\", false)", name, acct, ok)
			}
		})
	}
}

// TestSeedOnceIdempotent: a second SeedOnce on an already-seeded wallet credits nothing
// (the per-wallet seed guard / "already seeded" arm) and reports seeded=false, while the
// balance is unchanged. Asserted on BOTH backends.
func TestSeedOnceIdempotent(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			db.SetSeedLimit(100)
			b1, seeded1, err := db.SeedOnce("seed-idem", 30)
			if err != nil {
				t.Fatal(err)
			}
			if !seeded1 || !approx(b1, 30) {
				t.Fatalf("[%s] first SeedOnce = (%v, seeded=%v), want (30,true)", name, b1, seeded1)
			}
			b2, seeded2, err := db.SeedOnce("seed-idem", 30)
			if err != nil {
				t.Fatal(err)
			}
			if seeded2 {
				t.Errorf("[%s] second SeedOnce should not re-seed", name)
			}
			if !approx(b2, 30) {
				t.Errorf("[%s] balance after 2nd SeedOnce = %v, want unchanged 30", name, b2)
			}
		})
	}
}

// TestPayoutLotsUnknownPayout: PayoutLots for a payout id that does not exist returns
// (nil,false,nil) — no rows, not found, no error (the ownership gate's not-found arm).
func TestPayoutLotsUnknownPayout(t *testing.T) {
	pg := pgOnly(t)
	lots, ok, err := pg.PayoutLots("any-acct", 999999)
	if err != nil {
		t.Fatalf("PayoutLots(unknown) err = %v, want nil", err)
	}
	if ok || len(lots) != 0 {
		t.Fatalf("PayoutLots(unknown) = (%v, ok=%v), want (nil,false)", lots, ok)
	}
}

// TestFailPayoutUnknownIsNoOp: FailPayout on an id that isn't a pending payout is a no-op
// (nil error, nothing created/altered).
func TestFailPayoutUnknownIsNoOp(t *testing.T) {
	pg := pgOnly(t)
	if err := pg.FailPayout(999999); err != nil {
		t.Fatalf("FailPayout(unknown) err = %v, want nil (no-op)", err)
	}
	if ps, _ := pg.AdminAllPayouts(10); len(ps) != 0 {
		t.Fatalf("FailPayout(unknown) must not create a payout; got %d", len(ps))
	}
}

// TestRecordPendingReversalEmptyKeyAndLimit: an empty key is a no-op (no row recorded),
// and OpenPendingReversals honours a positive limit.
func TestRecordPendingReversalEmptyKeyAndLimit(t *testing.T) {
	pg := pgOnly(t)
	// Empty key: not recorded.
	if err := pg.RecordPendingReversal(PendingReversal{Key: ""}); err != nil {
		t.Fatalf("RecordPendingReversal(empty key) err = %v, want nil", err)
	}
	// Two real reversals, ascending created_at.
	if err := pg.RecordPendingReversal(PendingReversal{Key: "k1", DisputeID: "d1", Amount: 1, CreatedAt: 100}); err != nil {
		t.Fatal(err)
	}
	if err := pg.RecordPendingReversal(PendingReversal{Key: "k2", DisputeID: "d2", Amount: 2, CreatedAt: 200}); err != nil {
		t.Fatal(err)
	}
	all, err := pg.OpenPendingReversals(0) // 0 = all
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("OpenPendingReversals(0) = %d, want 2 (empty key not recorded)", len(all))
	}
	limited, err := pg.OpenPendingReversals(1) // positive limit
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].Key != "k1" {
		t.Fatalf("OpenPendingReversals(1) = %+v, want just the oldest (k1)", limited)
	}
}
