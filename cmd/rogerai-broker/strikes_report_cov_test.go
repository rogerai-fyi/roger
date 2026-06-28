package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// failBroker wires a testBrokerWithDB whose db is a toggleable failStore over a real Mem,
// so the safety/strike ERROR branches (persist failures, read failures) are exercised with
// the established failStore convention. Returns the broker, the toggle, and the inner Mem.
func failBroker(t *testing.T) (*broker, *failStore, *store.Mem) {
	t.Helper()
	mem := store.NewMem()
	fs := &failStore{Store: mem}
	return testBrokerWithDB(fs), fs, mem
}

// --- report.go safety branches ------------------------------------------------

// TestPreserveCSAMStoreErrorIsNonFatal: a PreserveCSAM persist failure must NOT crash the
// caller (the response path is never blocked on a store error) and records no incident.
func TestPreserveCSAMStoreErrorIsNonFatal(t *testing.T) {
	b, fs, mem := failBroker(t)
	fs.failPreserveCSAM = true
	b.preserveCSAM("u_x", "1.2.3.4", "S4", []byte("offending")) // must not panic
	if pending, _ := mem.PendingCSAMReports(0); len(pending) != 0 {
		t.Fatalf("a failed PreserveCSAM must record no incident, got %d", len(pending))
	}
}

// TestRehydrateBansStoreErrorIsNonFatal: a BannedNodes read failure at startup leaves the
// in-memory ban cache empty (broker still boots) rather than crashing.
func TestRehydrateBansStoreErrorIsNonFatal(t *testing.T) {
	b, fs, _ := failBroker(t)
	fs.failBannedNodes = true
	b.rehydrateBans() // must not panic
	if b.isBanned("anything") {
		t.Error("a failed rehydrate must leave the ban cache empty")
	}
}

// TestRehydrateBansLoadsPersisted: a persisted node ban is re-hydrated into the in-memory
// cache at startup (survives a restart).
func TestRehydrateBansLoadsPersisted(t *testing.T) {
	b, _, mem := failBroker(t)
	if err := mem.BanNode("persisted-ban", "manual"); err != nil {
		t.Fatal(err)
	}
	b.rehydrateBans()
	if !b.isBanned("persisted-ban") {
		t.Error("rehydrateBans should load the persisted ban into the cache")
	}
}

// TestBanNodeEmptyAndPersistFail: an empty node id is a no-op; a persist failure still flips
// the in-memory ban (routing stops immediately) — the cache is the authority on the hot path.
func TestBanNodeEmptyAndPersistFail(t *testing.T) {
	b, fs, _ := failBroker(t)
	b.banNode("", "reason") // no-op, no panic
	if len(b.banned) != 0 {
		t.Error("banNode(\"\") must be a no-op")
	}
	fs.failBanNode = true
	b.banNode("n-persistfail", "report threshold")
	if !b.isBanned("n-persistfail") {
		t.Error("banNode must flip the in-memory ban even when persistence fails")
	}
}

// TestUnbanNodeEmptyAndStoreError: an empty id is a nil no-op; a store error is surfaced so
// the caller (admin/appeal) sees the failure.
func TestUnbanNodeEmptyAndStoreError(t *testing.T) {
	b, fs, _ := failBroker(t)
	if err := b.unbanNode(""); err != nil {
		t.Errorf("unbanNode(\"\") = %v, want nil no-op", err)
	}
	fs.failUnbanNode = true
	if err := b.unbanNode("n"); err == nil {
		t.Error("unbanNode should surface a store error")
	}
}

// TestNodeBanSweepOnceErrorAndEmpty: a sweep whose ExpireNodeBans errors is non-fatal; a
// sweep that clears nothing is a clean no-op.
func TestNodeBanSweepOnceErrorAndEmpty(t *testing.T) {
	b, fs, _ := failBroker(t)
	b.nodeBanDays = 3
	fs.failExpireBans = true
	b.nodeBanSweepOnce(time.Now()) // error path: must not panic

	fs.failExpireBans = false
	b.nodeBanSweepOnce(time.Now()) // nothing banned -> len(cleared)==0 no-op
}

// TestNodeBanSweepOnceClearsCache: an aged report-origin ban is lifted by the sweep and the
// in-memory cache is refreshed so routing restores without a restart.
func TestNodeBanSweepOnceClearsCache(t *testing.T) {
	b, _, _ := failBroker(t)
	b.nodeBanDays = 3
	b.banNode("aged", "report threshold (3 distinct reporters)")
	if !b.isBanned("aged") {
		t.Fatal("precondition: node should be banned")
	}
	b.nodeBanSweepOnce(time.Now().Add(time.Hour)) // cutoff in the future -> the ban is "old"
	if b.isBanned("aged") {
		t.Error("nodeBanSweepOnce should evict the auto-lifted ban from the cache")
	}
}

// TestNodeBanSweepDisabledAndNilDB: the loop returns immediately when auto-lift is disabled
// (nodeBanDays<=0) or there is no db, never starting a ticker.
func TestNodeBanSweepDisabledAndNilDB(t *testing.T) {
	(&broker{nodeBanDays: 0}).nodeBanSweep(make(chan struct{})) // disabled -> return
	(&broker{nodeBanDays: 3}).nodeBanSweep(make(chan struct{})) // db == nil -> return
}

// TestReportPreflightInvalidJSONTruncationAndStoreError covers the report handler's edge
// branches: an OPTIONS preflight, an invalid-JSON 400, free-text truncation to 4096, and a
// persist failure 500.
func TestReportPreflightInvalidJSONTruncationAndStoreError(t *testing.T) {
	t.Run("preflight", func(t *testing.T) {
		b, _, _ := failBroker(t)
		req := httptest.NewRequest(http.MethodOptions, "/report", nil)
		req.Header.Set("Origin", "https://rogerai.fyi")
		w := httptest.NewRecorder()
		b.report(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatalf("OPTIONS preflight = %d, want 204", w.Code)
		}
	})

	t.Run("invalidJSON", func(t *testing.T) {
		b, _, _ := failBroker(t)
		if rec := postReportIP(b, `{not valid json`, "1.1.1.1"); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid JSON = %d, want 400", rec.Code)
		}
	})

	t.Run("detailTruncated", func(t *testing.T) {
		b, _, mem := failBroker(t)
		long := strings.Repeat("x", 5000)
		if rec := postReportIP(b, `{"category":"spam","node_id":"tn","detail":"`+long+`"}`, "2.2.2.2"); rec.Code != http.StatusOK {
			t.Fatalf("report = %d, want 200", rec.Code)
		}
		rows, err := mem.ReportsByNode("tn", 10)
		if err != nil || len(rows) != 1 {
			t.Fatalf("ReportsByNode = %d rows (%v), want 1", len(rows), err)
		}
		if len(rows[0].Detail) != 4096 {
			t.Fatalf("detail length = %d, want truncated to 4096", len(rows[0].Detail))
		}
	})

	t.Run("persistError", func(t *testing.T) {
		b, fs, _ := failBroker(t)
		fs.failAddReport = true
		if rec := postReportIP(b, `{"category":"abuse","node_id":"n"}`, "3.3.3.3"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("AddReport failure = %d, want 500", rec.Code)
		}
	})
}

// --- strikes.go owner-keyed anti-abuse branches -------------------------------

// TestOwnerOfNilDBAndUnowned: with no db, or for an unbound node, ownerOf falls back to the
// node id and reports ok=false (best identity available; nothing to rotate to).
func TestOwnerOfNilDBAndUnowned(t *testing.T) {
	if acct, ok := (&broker{}).ownerOf("node-x"); acct != "node-x" || ok {
		t.Fatalf("nil-db ownerOf = (%q,%v), want (node-x,false)", acct, ok)
	}
	b, _, _ := failBroker(t)
	if acct, ok := b.ownerOf("unbound-node"); acct != "unbound-node" || ok {
		t.Fatalf("unbound ownerOf = (%q,%v), want (unbound-node,false)", acct, ok)
	}
}

// TestStrikeNilDBIsNoOp: a strike with no db is a no-op (never panics).
func TestStrikeNilDBIsNoOp(t *testing.T) {
	(&broker{}).strike("n", "kind", "idem", false, nil)
}

// TestStrikeStoreErrorsFailSoft: an OwnerStrike write failure aborts the strike (no ban),
// and an OwnerStrikeStats read failure fails SOFT (single-class count) so a read error
// NEVER escalates an account to a ban.
func TestStrikeStoreErrorsFailSoft(t *testing.T) {
	t.Run("ownerStrikeWriteFails", func(t *testing.T) {
		b, fs, _ := failBroker(t)
		fs.failOwnerStrike = true
		b.strike("node-a", store.StrikeEmptyOutput, "idem-1", false, map[string]any{"x": 1})
		if b.isOwnerBanned("node-a") {
			t.Error("a strike whose OwnerStrike write failed must not ban the owner")
		}
	})

	t.Run("statsReadFailsSoft", func(t *testing.T) {
		b, fs, _ := failBroker(t)
		b.strikeWarnAt, b.strikeBanAt, b.strikeCorroborateKinds, b.strikeDecayDays = 3, 5, 2, 30
		fs.failStrikeStats = true // OwnerStrike + hold succeed; stats read fails -> fail soft
		b.strike("node-b", store.StrikeEmptyOutput, "idem-2", false, map[string]any{"x": 1})
		if b.isOwnerBanned("node-b") {
			t.Error("a stats read error must fail SOFT (windowed=1) and never escalate to a ban")
		}
	})
}

// TestBanOwnerEmptyAndPersistFail: an empty account is a no-op; a persist failure still
// flips the in-memory owner-ban (the hot pick/settle path reads the cache).
func TestBanOwnerEmptyAndPersistFail(t *testing.T) {
	b, fs, _ := failBroker(t)
	b.banOwner("", "r", "ev") // no-op
	if len(b.bannedOwners) != 0 {
		t.Error("banOwner(\"\") must be a no-op")
	}
	fs.failBanOwner = true
	b.banOwner("acct-z", "abuse", "{}")
	if !b.isOwnerBanned("acct-z") {
		t.Error("banOwner must flip the in-memory ban even when persistence fails")
	}
}

// TestIsOwnerBannedEmpty: an empty account id is never banned (the cheap guard).
func TestIsOwnerBannedEmpty(t *testing.T) {
	b, _, _ := failBroker(t)
	if b.isOwnerBanned("") {
		t.Error("isOwnerBanned(\"\") must be false")
	}
}

// TestNodeOwnerBannedUnownedNode: with the ban set non-empty, an unowned node (no owner
// binding) is not owner-banned — the node-id ban path handles those.
func TestNodeOwnerBannedUnownedNode(t *testing.T) {
	b, _, _ := failBroker(t)
	b.banOwner("some-owner", "abuse", "{}") // make the ban set non-empty (skips fast path)
	if b.nodeOwnerBanned("an-unowned-node") {
		t.Error("an unowned node must not be treated as owner-banned")
	}
}

// TestRehydrateOwnerBansNilErrorAndHappy: no-db and read-error rehydrates are non-fatal; a
// persisted owner ban is loaded into the cache at startup.
func TestRehydrateOwnerBansNilErrorAndHappy(t *testing.T) {
	(&broker{}).rehydrateOwnerBans() // nil db -> return, no panic

	b, fs, mem := failBroker(t)
	fs.failBannedOwners = true
	b.rehydrateOwnerBans() // read error -> non-fatal, cache empty
	if b.isOwnerBanned("anyone") {
		t.Error("a failed owner-ban rehydrate must leave the cache empty")
	}

	fs.failBannedOwners = false
	if err := mem.BanOwner("durable-owner", "abuse", "{}"); err != nil {
		t.Fatal(err)
	}
	b.rehydrateOwnerBans()
	if !b.isOwnerBanned("durable-owner") {
		t.Error("rehydrateOwnerBans should load the persisted owner ban into the cache")
	}
}
