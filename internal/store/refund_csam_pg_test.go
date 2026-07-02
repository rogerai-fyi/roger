package store

import (
	"math"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// refund_csam_pg_test.go exercises the Postgres implementations of the launch-audit money
// + safety additions (RefundLineage / NoteRecovery / the CSAM drain) against the REAL
// Postgres store (no mocks), so their branches are HONESTLY covered - the broker BDD
// suites drive the Mem store, leaving these Postgres paths otherwise unexercised.

func near(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// TestPostgresRefundLineage covers the full refund clawback on real Postgres: the wallet
// debit, the operator claw, the platform loss (spent) vs unspent-reclaim (no loss), the
// per-charge cap after a dispute, and refund-id idempotency.
func TestPostgresRefundLineage(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)

	t.Run("full refund claws the operator share, books the fee as platform loss", func(t *testing.T) {
		pg := pgOnly(t)
		if err := pg.BindNode("n1", "op1"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := pg.CreditOnce("real:ch_1", "alice", 50); err != nil {
			t.Fatal(err)
		}
		if err := pg.LinkCharge("sess_1", "pi_1", "ch_1", "alice", 50); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.Settle("alice", "n1", 50, 35, protocol.UsageReceipt{RequestID: "r1", TS: now.Unix()}); err != nil {
			t.Fatal(err)
		}
		res, eff, err := pg.RefundLineage("re_1", []string{"pi_1", "ch_1"}, "alice", "", 50, now)
		if err != nil {
			t.Fatal(err)
		}
		near(t, eff, 50, "eff")
		near(t, res.Clawed, 35, "clawed")
		near(t, res.PlatformLoss, 15, "platform loss")
		bal, _ := pg.BalanceOf("alice", 0)
		near(t, bal, -50, "alice balance")
	})

	t.Run("refund of UNSPENT credits reclaims from balance, no platform loss", func(t *testing.T) {
		pg := pgOnly(t)
		if _, _, err := pg.CreditOnce("real:ch_2", "bob", 25); err != nil {
			t.Fatal(err)
		}
		if err := pg.LinkCharge("sess_2", "pi_2", "ch_2", "bob", 25); err != nil {
			t.Fatal(err)
		}
		res, eff, err := pg.RefundLineage("re_2", []string{"pi_2", "ch_2"}, "bob", "", 25, now)
		if err != nil {
			t.Fatal(err)
		}
		near(t, eff, 25, "eff")
		near(t, res.PlatformLoss, 0, "platform loss")
		bal, _ := pg.BalanceOf("bob", 0)
		near(t, bal, 0, "bob balance")
	})

	t.Run("refund id is idempotent", func(t *testing.T) {
		pg := pgOnly(t)
		if _, _, err := pg.CreditOnce("real:ch_3", "carol", 40); err != nil {
			t.Fatal(err)
		}
		if err := pg.LinkCharge("sess_3", "pi_3", "ch_3", "carol", 40); err != nil {
			t.Fatal(err)
		}
		if _, _, err := pg.RefundLineage("re_3", []string{"pi_3", "ch_3"}, "carol", "", 40, now); err != nil {
			t.Fatal(err)
		}
		res, eff, err := pg.RefundLineage("re_3", []string{"pi_3", "ch_3"}, "carol", "", 40, now)
		if err != nil {
			t.Fatal(err)
		}
		if !res.AlreadyHandled {
			t.Fatal("second delivery of the same refund id must be AlreadyHandled")
		}
		near(t, eff, 0, "eff on redelivery")
		bal, _ := pg.BalanceOf("carol", 0)
		near(t, bal, 0, "carol balance (debited once)")
	})

	t.Run("refund after a dispute on the same charge never double-debits", func(t *testing.T) {
		pg := pgOnly(t)
		if _, _, err := pg.CreditOnce("real:ch_4", "dave", 60); err != nil {
			t.Fatal(err)
		}
		if err := pg.LinkCharge("sess_4", "pi_4", "ch_4", "dave", 60); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.ChargebackLineage("dp_4", "dave", "", 60, now); err != nil {
			t.Fatal(err)
		}
		if err := pg.NoteRecovery([]string{"pi_4", "ch_4"}, 60); err != nil {
			t.Fatal(err)
		}
		_, eff, err := pg.RefundLineage("re_4", []string{"pi_4", "ch_4"}, "dave", "", 60, now)
		if err != nil {
			t.Fatal(err)
		}
		near(t, eff, 0, "eff (capped by prior dispute recovery)")
		bal, _ := pg.BalanceOf("dave", 0)
		near(t, bal, 0, "dave balance (dispute 60 took 60->0; refund capped to 0 - debited 60 total, not 120)")
	})

	t.Run("zero refund is a no-op", func(t *testing.T) {
		pg := pgOnly(t)
		if _, _, err := pg.CreditOnce("real:ch_5", "erin", 10); err != nil {
			t.Fatal(err)
		}
		if err := pg.LinkCharge("sess_5", "pi_5", "ch_5", "erin", 10); err != nil {
			t.Fatal(err)
		}
		_, eff, err := pg.RefundLineage("re_5z", []string{"pi_5", "ch_5"}, "erin", "", 0, now)
		if err != nil {
			t.Fatal(err)
		}
		near(t, eff, 0, "eff (zero refund)")
	})
}

// TestPostgresCSAMDrain covers the Postgres CSAM admin drain: submit (idempotent +
// monotonic), the empty-report-id guard, unknown-id, queue stats, and content retention.
func TestPostgresCSAMDrain(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	pg := pgOnly(t)

	id1, err := pg.PreserveCSAM(CSAMIncident{Pseudonym: "p1", IP: "203.0.113.1", Category: "S4", Content: []byte("ENC1"), CreatedAt: now.Add(-40 * time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.PreserveCSAM(CSAMIncident{Pseudonym: "p2", IP: "203.0.113.2", Category: "S4", Content: []byte("ENC2"), CreatedAt: now.Add(-2 * time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}

	depth, oldest, err := pg.CSAMQueueStats(now)
	if err != nil {
		t.Fatal(err)
	}
	if depth != 2 {
		t.Fatalf("depth = %d, want 2", depth)
	}
	if oldest < int64(39*3600) {
		t.Fatalf("oldest age = %d, want ~40h", oldest)
	}

	if _, _, err := pg.MarkCSAMSubmitted(id1, "", "gh:founder", now); err != errEmptyReportID {
		t.Fatalf("empty report id: got %v, want errEmptyReportID", err)
	}

	inc, found, err := pg.MarkCSAMSubmitted(id1, "CT-111", "gh:founder", now)
	if err != nil || !found {
		t.Fatalf("submit id1: found=%v err=%v", found, err)
	}
	if inc.ReportState != CSAMReported || inc.ReportID != "CT-111" || inc.ReportedBy != "gh:founder" {
		t.Fatalf("submit id1: %+v", inc)
	}
	if len(inc.Content) != 0 {
		t.Fatal("submit response must not carry the ciphertext")
	}

	inc2, found, err := pg.MarkCSAMSubmitted(id1, "CT-999", "gh:someoneelse", now)
	if err != nil || !found {
		t.Fatalf("re-submit id1: found=%v err=%v", found, err)
	}
	if inc2.ReportID != "CT-111" {
		t.Fatalf("re-submit must keep the original report id, got %q", inc2.ReportID)
	}

	if _, found, err := pg.MarkCSAMSubmitted(999999, "CT-x", "gh:founder", now); err != nil || found {
		t.Fatalf("unknown id: found=%v err=%v (want found=false)", found, err)
	}

	depth, _, err = pg.CSAMQueueStats(now)
	if err != nil {
		t.Fatal(err)
	}
	if depth != 1 {
		t.Fatalf("depth after one submit = %d, want 1", depth)
	}
	if ret, err := pg.CSAMContentRetained(id1); err != nil || !ret {
		t.Fatalf("id1 content must be retained: ret=%v err=%v", ret, err)
	}
	if gone, err := pg.CSAMContentRetained(424242); err != nil || gone {
		t.Fatalf("unknown id content: gone=%v err=%v (want false)", gone, err)
	}
}

// TestMemRefundLineage covers the Mem RefundLineage / NoteRecovery / cap on the in-memory
// store (no DB needed, so it always runs) - the per-package coverage counterpart to the
// Postgres test above (broker BDD suites cover Mem via a DIFFERENT package's coverage).
func TestMemRefundLineage(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)

	t.Run("full refund: claw + platform-loss fee", func(t *testing.T) {
		m := NewMem()
		_ = m.BindNode("n1", "op1")
		if _, _, err := m.CreditOnce("real:ch_1", "alice", 50); err != nil {
			t.Fatal(err)
		}
		_ = m.LinkCharge("s1", "pi_1", "ch_1", "alice", 50)
		if _, err := m.Settle("alice", "n1", 50, 35, protocol.UsageReceipt{RequestID: "r1", TS: now.Unix()}); err != nil {
			t.Fatal(err)
		}
		res, eff, err := m.RefundLineage("re_1", []string{"pi_1", "ch_1"}, "alice", "", 50, now)
		if err != nil {
			t.Fatal(err)
		}
		near(t, eff, 50, "eff")
		near(t, res.Clawed, 35, "clawed")
		near(t, res.PlatformLoss, 15, "platform loss")
	})

	t.Run("unspent refund: reclaimed from balance, no loss", func(t *testing.T) {
		m := NewMem()
		if _, _, err := m.CreditOnce("real:ch_2", "bob", 25); err != nil {
			t.Fatal(err)
		}
		_ = m.LinkCharge("s2", "pi_2", "ch_2", "bob", 25)
		res, eff, err := m.RefundLineage("re_2", []string{"pi_2", "ch_2"}, "bob", "", 25, now)
		if err != nil {
			t.Fatal(err)
		}
		near(t, eff, 25, "eff")
		near(t, res.PlatformLoss, 0, "platform loss")
	})

	t.Run("idempotent on refund id", func(t *testing.T) {
		m := NewMem()
		_, _, _ = m.CreditOnce("real:ch_3", "carol", 40)
		_ = m.LinkCharge("s3", "pi_3", "ch_3", "carol", 40)
		if _, _, err := m.RefundLineage("re_3", []string{"pi_3", "ch_3"}, "carol", "", 40, now); err != nil {
			t.Fatal(err)
		}
		res, eff, err := m.RefundLineage("re_3", []string{"pi_3", "ch_3"}, "carol", "", 40, now)
		if err != nil {
			t.Fatal(err)
		}
		if !res.AlreadyHandled {
			t.Fatal("want AlreadyHandled on redelivery")
		}
		near(t, eff, 0, "eff redelivery")
	})

	t.Run("dispute then refund on same charge: capped, no double-debit", func(t *testing.T) {
		m := NewMem()
		_, _, _ = m.CreditOnce("real:ch_4", "dave", 60)
		_ = m.LinkCharge("s4", "pi_4", "ch_4", "dave", 60)
		if _, err := m.ChargebackLineage("dp_4", "dave", "", 60, now); err != nil {
			t.Fatal(err)
		}
		_ = m.NoteRecovery([]string{"pi_4", "ch_4"}, 60)
		_, eff, err := m.RefundLineage("re_4", []string{"pi_4", "ch_4"}, "dave", "", 60, now)
		if err != nil {
			t.Fatal(err)
		}
		near(t, eff, 0, "eff capped")
		bal, _ := m.BalanceOf("dave", 0)
		near(t, bal, 0, "dave balance (60 total)")
	})

	t.Run("unmapped charge is not capped", func(t *testing.T) {
		m := NewMem()
		_, _, _ = m.CreditOnce("real:x", "frank", 5)
		res, eff, err := m.RefundLineage("re_z", nil, "frank", "", 5, now) // no chargeRefs
		if err != nil {
			t.Fatal(err)
		}
		near(t, eff, 5, "eff uncapped")
		near(t, res.PlatformLoss, 0, "loss (reclaimed from unspent balance)")
	})

	t.Run("zero refund no-op", func(t *testing.T) {
		m := NewMem()
		_, _, _ = m.CreditOnce("real:ch_6", "gina", 10)
		_ = m.LinkCharge("s6", "pi_6", "ch_6", "gina", 10)
		_, eff, err := m.RefundLineage("re_6z", []string{"pi_6", "ch_6"}, "gina", "", 0, now)
		if err != nil {
			t.Fatal(err)
		}
		near(t, eff, 0, "eff zero")
	})
}

// TestMemCSAMDrain covers the Mem CSAM drain methods (submit idempotency, empty-id guard,
// stats, retention) without a DB.
func TestMemCSAMDrain(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	m := NewMem()
	id1, _ := m.PreserveCSAM(CSAMIncident{Pseudonym: "p1", Category: "S4", Content: []byte("ENC1"), CreatedAt: now.Add(-40 * time.Hour).Unix()})
	_, _ = m.PreserveCSAM(CSAMIncident{Pseudonym: "p2", Category: "S4", Content: []byte("ENC2"), CreatedAt: now.Add(-2 * time.Hour).Unix()})

	depth, oldest, err := m.CSAMQueueStats(now)
	if err != nil || depth != 2 || oldest < int64(39*3600) {
		t.Fatalf("stats depth=%d oldest=%d err=%v", depth, oldest, err)
	}
	if _, _, err := m.MarkCSAMSubmitted(id1, "", "gh:f", now); err != errEmptyReportID {
		t.Fatalf("empty id: %v", err)
	}
	inc, found, err := m.MarkCSAMSubmitted(id1, "CT-1", "gh:f", now)
	if err != nil || !found || inc.ReportID != "CT-1" || len(inc.Content) != 0 {
		t.Fatalf("submit: %+v found=%v err=%v", inc, found, err)
	}
	inc2, _, _ := m.MarkCSAMSubmitted(id1, "CT-2", "gh:g", now)
	if inc2.ReportID != "CT-1" {
		t.Fatalf("re-submit must keep CT-1, got %q", inc2.ReportID)
	}
	if _, found, _ := m.MarkCSAMSubmitted(999, "CT-x", "gh:f", now); found {
		t.Fatal("unknown id must be found=false")
	}
	if depth, _, _ := m.CSAMQueueStats(now); depth != 1 {
		t.Fatalf("depth after submit = %d, want 1", depth)
	}
	if ret, _ := m.CSAMContentRetained(id1); !ret {
		t.Fatal("id1 content must be retained")
	}
	if gone, _ := m.CSAMContentRetained(4242); gone {
		t.Fatal("unknown id must not be retained")
	}
	// MarkCSAMReported (legacy path) still flips state.
	_ = m.MarkCSAMReported(id1)
}
