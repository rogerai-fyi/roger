package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestReversalRetryOnce covers the silent-money-leak recovery pass: a pending reversal is
// re-attempted; success clears it from the open set, while a persistent failure records
// attempts and (past the max) dead-letters without losing the intent.
func TestReversalRetryOnce(t *testing.T) {
	t.Run("success clears the pending reversal", func(t *testing.T) {
		mem := store.NewMem()
		b := &broker{db: mem}
		b.bill.creditUSD = 1
		b.conn = connect{reverseTransfer: func(tid string, cents int64, idem string) (string, error) {
			return "trr_ok", nil
		}}
		_ = mem.RecordPendingReversal(store.PendingReversal{
			Key: "reverse:d1:5", DisputeID: "d1", LotID: 5, AccountID: "acct", TransferID: "tr_1", Amount: 2,
		})
		b.reversalRetryOnce()
		if open, _ := mem.OpenPendingReversals(100); len(open) != 0 {
			t.Errorf("a recovered reversal should leave the open set empty, got %d", len(open))
		}
	})

	failing := func() connect {
		return connect{reverseTransfer: func(tid string, cents int64, idem string) (string, error) {
			return "", fmt.Errorf("stripe down")
		}}
	}

	t.Run("retryable failure stays open for the next tick", func(t *testing.T) {
		t.Setenv("ROGERAI_REVERSAL_MAX_ATTEMPTS", "5") // well above 1 attempt -> still retryable
		mem := store.NewMem()
		b := &broker{db: mem, conn: failing()}
		b.bill.creditUSD = 1
		_ = mem.RecordPendingReversal(store.PendingReversal{
			Key: "reverse:d2:6", DisputeID: "d2", LotID: 6, AccountID: "acct", TransferID: "tr_2", Amount: 1,
		})
		b.reversalRetryOnce() // attempt 1 fails, 1 < 5 -> remains an OPEN (retryable) intent
		open, _ := mem.OpenPendingReversals(100)
		if len(open) != 1 || open[0].Attempts < 1 {
			t.Fatalf("a retryable failure should stay open with an incremented attempt, got %+v", open)
		}
	})

	t.Run("dead-letters past the max (drops out of the open set)", func(t *testing.T) {
		t.Setenv("ROGERAI_REVERSAL_MAX_ATTEMPTS", "1") // 1 attempt then terminal
		mem := store.NewMem()
		b := &broker{db: mem, conn: failing()}
		b.bill.creditUSD = 1
		_ = mem.RecordPendingReversal(store.PendingReversal{
			Key: "reverse:d3:7", DisputeID: "d3", LotID: 7, AccountID: "acct", TransferID: "tr_3", Amount: 1,
		})
		b.reversalRetryOnce() // attempt 1 fails, 1 >= max(1) -> dead-letter
		// Dead-lettered intents drop out of the OPEN set (terminal, reconciled out of band) -
		// distinct from the retryable case above where the row stays open.
		if open, _ := mem.OpenPendingReversals(100); len(open) != 0 {
			t.Errorf("a dead-lettered reversal should leave the open set, got %d open", len(open))
		}
	})
}

// TestStrikeAndReversalConfig covers the strike-threshold + reversal-attempt + duration
// + recent-limit config helpers (env override + default + bounds).
func TestStrikeAndReversalConfig(t *testing.T) {
	t.Setenv("ROGERAI_STRIKE_WARN_AT", "2")
	if strikeWarnAt() != 2 {
		t.Errorf("strikeWarnAt env = %d, want 2", strikeWarnAt())
	}
	t.Setenv("ROGERAI_STRIKE_WARN_AT", "")
	if strikeWarnAt() != defaultStrikeWarnAt {
		t.Errorf("strikeWarnAt default = %d", strikeWarnAt())
	}
	t.Setenv("ROGERAI_STRIKE_BAN_AT", "9")
	if strikeBanAt() != 9 {
		t.Errorf("strikeBanAt env = %d, want 9", strikeBanAt())
	}

	b := &broker{}
	t.Setenv("ROGERAI_REVERSAL_MAX_ATTEMPTS", "7")
	if b.reversalMaxAttempts() != 7 {
		t.Errorf("reversalMaxAttempts env = %d, want 7", b.reversalMaxAttempts())
	}
	t.Setenv("ROGERAI_REVERSAL_MAX_ATTEMPTS", "")
	if b.reversalMaxAttempts() != defaultReversalMaxAttempts {
		t.Errorf("reversalMaxAttempts default = %d", b.reversalMaxAttempts())
	}

	// envDuration: valid, invalid (->default), unset (->default).
	t.Setenv("ROGERAI_TEST_DUR", "90s")
	if envDuration("ROGERAI_TEST_DUR", time.Minute) != 90*time.Second {
		t.Error("envDuration valid override failed")
	}
	t.Setenv("ROGERAI_TEST_DUR", "nonsense")
	if envDuration("ROGERAI_TEST_DUR", time.Minute) != time.Minute {
		t.Error("envDuration garbage should fall back to default")
	}
	if envDuration("ROGERAI_TEST_DUR_UNSET", 5*time.Second) != 5*time.Second {
		t.Error("envDuration unset should be default")
	}

	// recentLimit: default, custom, over-cap, garbage.
	mk := func(q string) *http.Request { return httptest.NewRequest(http.MethodGet, "/x"+q, nil) }
	if recentLimit(mk("")) != 20 || recentLimit(mk("?limit=5")) != 5 ||
		recentLimit(mk("?limit=9999")) != 100 || recentLimit(mk("?limit=abc")) != 20 {
		t.Error("recentLimit bounds wrong")
	}
}

// TestPayoutTransfer covers the transfer rail's three non-HTTP branches: the injectable
// seam (tests/dev), the dev-stub (no key), and the REQUIRE_LIVE refusal.
func TestPayoutTransfer(t *testing.T) {
	b := &broker{}
	b.bill.creditUSD = 1

	// 1) Injectable seam wins and gets the credits-as-cents.
	var gotCents int64
	b.conn = connect{transfer: func(dest string, cents int64, idem string) (string, error) {
		gotCents = cents
		return "tr_seam", nil
	}}
	id, err := b.payoutTransfer("acct_x", "octocat", 4.0, "idem1")
	if err != nil || id != "tr_seam" || gotCents != 400 {
		t.Fatalf("payoutTransfer(seam) = %q/%v cents=%d, want tr_seam/400", id, err, gotCents)
	}

	// 2) Dev stub: no seam, no key -> a tr_dev_stub_ id, no real money.
	b.conn = connect{}
	id, err = b.payoutTransfer("", "octocat", 1.0, "idem2")
	if err != nil || id == "" || id[:12] != "tr_dev_stub_" {
		t.Fatalf("payoutTransfer(stub) = %q/%v, want tr_dev_stub_*", id, err)
	}

	// 3) REQUIRE_LIVE with a non-live key -> refusal (FailPayout rolls the payout back).
	t.Setenv("ROGERAI_REQUIRE_LIVE", "1")
	b.conn = connect{secretKey: "sk_test_notlive"}
	if _, err := b.payoutTransfer("acct_x", "octocat", 1.0, "idem3"); err == nil {
		t.Error("payoutTransfer under REQUIRE_LIVE with a non-live key should refuse")
	}
}

// TestPayoutTransferReversal covers the reversal rail's three non-HTTP branches.
func TestPayoutTransferReversal(t *testing.T) {
	b := &broker{}
	b.bill.creditUSD = 1

	var gotCents int64
	b.conn = connect{reverseTransfer: func(tid string, cents int64, idem string) (string, error) {
		gotCents = cents
		return "trr_seam", nil
	}}
	id, err := b.payoutTransferReversal("tr_1", 2.0, "idem1")
	if err != nil || id != "trr_seam" || gotCents != 200 {
		t.Fatalf("reversal(seam) = %q/%v cents=%d, want trr_seam/200", id, err, gotCents)
	}

	b.conn = connect{}
	id, err = b.payoutTransferReversal("tr_dev_stub_x", 1.0, "idem2")
	if err != nil || id[:13] != "trr_dev_stub_" {
		t.Fatalf("reversal(stub) = %q/%v, want trr_dev_stub_*", id, err)
	}

	t.Setenv("ROGERAI_REQUIRE_LIVE", "1")
	b.conn = connect{secretKey: "sk_test_notlive"}
	if _, err := b.payoutTransferReversal("tr_dev_stub_x", 1.0, "idem3"); err == nil {
		t.Error("reversal under REQUIRE_LIVE with a stub transfer should refuse")
	}
}
