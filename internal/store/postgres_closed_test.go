package store

import (
	"os"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// closedPostgres opens the real Postgres store, then CLOSES its connection pool. Every
// subsequent call therefore hits a genuinely dead pool (sql: database is closed) — a REAL
// failure mode, not a mock. It backs the error-propagation contract battery below: each
// money/admin/safety/grant method must surface a dead DB as an error AND return its zero
// value, never panic or silently "succeed" (the codebase has several `_ = p.db...Scan`
// spots, so a method that drops a real DB error is a regression this guards).
func closedPostgres(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set; skipping real-Postgres closed-pool test")
	}
	pg, err := NewPostgres(dsn)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	if err := pg.Close(); err != nil {
		t.Fatalf("close pool: %v", err)
	}
	return pg
}

func errNil(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: want a DB error on a closed pool, got nil", name)
	}
}

func errZeroF(t *testing.T, name string, v float64, err error) {
	t.Helper()
	errNil(t, name, err)
	if v != 0 {
		t.Errorf("%s: want zero value alongside the error, got %v", name, v)
	}
}

func errZeroI(t *testing.T, name string, v int, err error) {
	t.Helper()
	errNil(t, name, err)
	if v != 0 {
		t.Errorf("%s: want zero value alongside the error, got %v", name, v)
	}
}

func errEmpty[T any](t *testing.T, name string, s []T, err error) {
	t.Helper()
	errNil(t, name, err)
	if len(s) != 0 {
		t.Errorf("%s: want a nil slice alongside the error, got %d rows", name, len(s))
	}
}

// TestPostgresClosedPoolPropagatesErrors drives every Postgres method against a closed pool
// and asserts the error surfaces with a zero/empty value. This deterministically covers the
// top-of-function DB-error branch in each method without any driver mock.
func TestPostgresClosedPoolPropagatesErrors(t *testing.T) {
	pg := closedPostgres(t)
	now := time.Now()
	rec := protocol.UsageReceipt{RequestID: "r-closed", Model: "m", PromptTokens: 1, CompletionTokens: 1, TS: now.Unix()}

	// --- money / wallet path ---
	{
		v, err := pg.BalanceOf("u", 5)
		errZeroF(t, "BalanceOf", v, err)
	}
	{
		v, seeded, err := pg.SeedOnce("u", 5)
		errZeroF(t, "SeedOnce", v, err)
		if seeded {
			t.Error("SeedOnce: want seeded=false on error")
		}
	}
	{
		_, _, remaining, err := pg.SeedStatus()
		errNil(t, "SeedStatus", err)
		if remaining != 0 {
			t.Errorf("SeedStatus: want remaining=0 on error, got %d", remaining)
		}
	}
	{
		v, err := pg.Settle("u", "n", 10, 5, rec)
		errZeroF(t, "Settle", v, err)
	}
	{
		v, err := pg.Finalize("u", "n", 10, 10, 5, rec)
		errZeroF(t, "Finalize", v, err)
	}
	{
		v, err := pg.AddCredits("u", 5)
		errZeroF(t, "AddCredits", v, err)
	}
	{
		ok, err := pg.Hold("u", 5)
		errNil(t, "Hold", err)
		if ok {
			t.Error("Hold: want ok=false on error")
		}
	}
	{
		v, err := pg.ReleaseHold("u", 5)
		errZeroF(t, "ReleaseHold", v, err)
	}
	{
		ok, err := pg.MarkProcessed("k")
		errNil(t, "MarkProcessed", err)
		if ok {
			t.Error("MarkProcessed: want ok=false on error")
		}
	}
	{
		ok, v, err := pg.CreditOnce("k", "u", 5)
		errZeroF(t, "CreditOnce", v, err)
		if ok {
			t.Error("CreditOnce: want ok=false on error")
		}
	}
	{
		v, err := pg.PeekBalance("u")
		errZeroF(t, "PeekBalance", v, err)
	}
	{
		v, err := pg.DeriveBalance("u")
		errZeroF(t, "DeriveBalance", v, err)
	}
	{
		v, err := pg.EarningsOf("n")
		errZeroF(t, "EarningsOf", v, err)
	}
	{
		v, err := pg.SpendOf("u")
		errZeroF(t, "SpendOf", v, err)
	}
	{
		v, err := pg.MonthlyCapOf("u")
		errZeroF(t, "MonthlyCapOf", v, err)
	}
	{
		v, err := pg.MonthSpendOf("u", now)
		errZeroF(t, "MonthSpendOf", v, err)
	}

	// --- receipt / ledger reads ---
	{
		e, err := pg.EntriesByUser("u", 0, now.Unix())
		errEmpty(t, "EntriesByUser", e, err)
	}
	{
		e, err := pg.EntriesByAccount("a", 0, now.Unix())
		errEmpty(t, "EntriesByAccount", e, err)
	}
	{
		e, err := pg.RecentByUser("u", 10)
		errEmpty(t, "RecentByUser", e, err)
	}
	{
		e, err := pg.RecentByNode("n", 10)
		errEmpty(t, "RecentByNode", e, err)
	}
	{
		r, err := pg.LedgerOf("u", nil, 10)
		errEmpty(t, "LedgerOf", r, err)
	}

	// --- owners / accounts / nodes ---
	{
		_, ok, err := pg.OwnerByPubkey("pk")
		errNil(t, "OwnerByPubkey", err)
		if ok {
			t.Error("OwnerByPubkey: want ok=false on error")
		}
	}
	{
		_, ok, err := pg.OwnerByLogin("l")
		errNil(t, "OwnerByLogin", err)
		if ok {
			t.Error("OwnerByLogin: want ok=false on error")
		}
	}
	errNil(t, "BindOwner", pg.BindOwner(Owner{Pubkey: "pk"}))
	{
		_, ok, err := pg.UpdateAccount("l", "e@x")
		errNil(t, "UpdateAccount", err)
		if ok {
			t.Error("UpdateAccount: want ok=false on error")
		}
	}
	{
		ok, err := pg.ClaimWelcome("pk")
		errNil(t, "ClaimWelcome", err)
		if ok {
			t.Error("ClaimWelcome: want ok=false on error")
		}
	}
	errNil(t, "SetConnect", pg.SetConnect("l", "acct", "active"))
	{
		ok, err := pg.DeleteAccount("l")
		errNil(t, "DeleteAccount", err)
		if ok {
			t.Error("DeleteAccount: want ok=false on error")
		}
	}
	errNil(t, "BindNode", pg.BindNode("n", "a"))
	{
		ns, err := pg.NodesOfAccount("a")
		errEmpty(t, "NodesOfAccount", ns, err)
	}
	errNil(t, "UpsertNode", pg.UpsertNode(NodeRecord{NodeID: "n"}))
	errNil(t, "TouchNode", pg.TouchNode("n", now))
	{
		recs, err := pg.AllNodes()
		errEmpty(t, "AllNodes", recs, err)
	}
	errNil(t, "DeleteNode", pg.DeleteNode("n"))
	{
		_, ok, err := pg.AccountOfNode("n")
		errNil(t, "AccountOfNode", err)
		if ok {
			t.Error("AccountOfNode: want ok=false on error")
		}
	}

	// --- metrics rollups ---
	{
		ms, err := pg.ProviderMetrics("a", 0, now.Unix())
		errEmpty(t, "ProviderMetrics", ms, err)
	}
	{
		ms, err := pg.UsageMetrics("u", 0, now.Unix())
		errEmpty(t, "UsageMetrics", ms, err)
	}

	// --- private bands ---
	errNil(t, "CreateBand", pg.CreateBand(Band{ID: "b", Owner: "o"}))
	{
		_, ok, err := pg.BandByCodeHash("h")
		errNil(t, "BandByCodeHash", err)
		if ok {
			t.Error("BandByCodeHash: want ok=false on error")
		}
	}
	{
		_, ok, err := pg.BandByNode("n")
		errNil(t, "BandByNode", err)
		if ok {
			t.Error("BandByNode: want ok=false on error")
		}
	}
	{
		bs, err := pg.BandsByOwner("o")
		errEmpty(t, "BandsByOwner", bs, err)
	}
	{
		ok, err := pg.SetBandRevoked("b", "o", true)
		errNil(t, "SetBandRevoked", err)
		if ok {
			t.Error("SetBandRevoked: want ok=false on error")
		}
	}
	{
		n, err := pg.CountActiveBands("o", now)
		errZeroI(t, "CountActiveBands", n, err)
	}

	// --- offer overrides ---
	{
		_, ok, err := pg.OfferOverride("n", "m")
		errNil(t, "OfferOverride", err)
		if ok {
			t.Error("OfferOverride: want ok=false on error")
		}
	}
	{
		ovs, err := pg.OverridesByOwner("o")
		errEmpty(t, "OverridesByOwner", ovs, err)
	}
	{
		ok, err := pg.ClearOfferOverride("o", "n", "m")
		errNil(t, "ClearOfferOverride", err)
		if ok {
			t.Error("ClearOfferOverride: want ok=false on error")
		}
	}

	// --- payouts / lots / rollups ---
	{
		s, err := pg.EarningSplitOf("a", now)
		errZeroF(t, "EarningSplitOf", s.Payable, err)
	}
	{
		s, err := pg.EarningSplitOfNode("n", now)
		errZeroF(t, "EarningSplitOfNode", s.Payable, err)
	}
	{
		_, ok, _, err := pg.RequestPayout("a", now, 1)
		errNil(t, "RequestPayout", err)
		if ok {
			t.Error("RequestPayout: want ok=false on error")
		}
	}
	errNil(t, "SettlePayout", pg.SettlePayout(1, "tr"))
	errNil(t, "FailPayout", pg.FailPayout(1))
	{
		ps, err := pg.PayoutsOf("a", 10)
		errEmpty(t, "PayoutsOf", ps, err)
	}
	{
		bs, err := pg.ReleaseSchedule("a", now)
		errEmpty(t, "ReleaseSchedule", bs, err)
	}
	{
		bm, bn, err := pg.EarningRollups("a")
		errNil(t, "EarningRollups", err)
		if len(bm) != 0 || len(bn) != 0 {
			t.Error("EarningRollups: want empty rollups on error")
		}
	}
	{
		ls, ok, err := pg.PayoutLots("a", 1)
		errEmpty(t, "PayoutLots", ls, err)
		if ok {
			t.Error("PayoutLots: want ok=false on error")
		}
	}

	// --- chargebacks / disputes / reversals ---
	{
		v, err := pg.Chargeback("d", "u", "r", 5, now)
		errZeroF(t, "Chargeback", v, err)
	}
	{
		res, err := pg.ChargebackLineage("d2", "u", "r", 5, now)
		errZeroF(t, "ChargebackLineage", res.Clawed, err)
	}
	errNil(t, "LinkCharge", pg.LinkCharge("s", "pi", "ch", "u", 5))
	{
		_, _, ok, err := pg.WalletByCharge("pi")
		errNil(t, "WalletByCharge", err)
		if ok {
			t.Error("WalletByCharge: want ok=false on error")
		}
	}
	{
		n, err := pg.OpenDisputeCount("a")
		errZeroI(t, "OpenDisputeCount", n, err)
	}
	errNil(t, "RecordPendingReversal", pg.RecordPendingReversal(PendingReversal{Key: "k"}))
	{
		rs, err := pg.OpenPendingReversals(10)
		errEmpty(t, "OpenPendingReversals", rs, err)
	}
	errNil(t, "MarkReversalAttempt", pg.MarkReversalAttempt("k", true, "", 3, now))
	errNil(t, "Healthy", pg.Healthy())

	// --- recount holds ---
	errNil(t, "SetNodeRecountHold", pg.SetNodeRecountHold("n", true))
	errNil(t, "SetAccountRecountHold", pg.SetAccountRecountHold("a", true))
	{
		n, err := pg.ExpireRecountHolds(now)
		errZeroI(t, "ExpireRecountHolds", n, err)
	}
	{
		m, err := pg.RecountHeldNodes()
		errNil(t, "RecountHeldNodes", err)
		if len(m) != 0 {
			t.Error("RecountHeldNodes: want empty map on error")
		}
	}

	// --- grants ---
	errNil(t, "CreateGrant", pg.CreateGrant(Grant{ID: "g", Owner: "o"}))
	{
		_, ok, err := pg.GrantBySecretHash("h")
		errNil(t, "GrantBySecretHash", err)
		if ok {
			t.Error("GrantBySecretHash: want ok=false on error")
		}
	}
	{
		gs, err := pg.GrantsByOwner("o")
		errEmpty(t, "GrantsByOwner", gs, err)
	}
	{
		ok, err := pg.SetGrantRevoked("g", "o", true)
		errNil(t, "SetGrantRevoked", err)
		if ok {
			t.Error("SetGrantRevoked: want ok=false on error")
		}
	}
	{
		_, ok, err := pg.UpdateGrant("g", "o", GrantPatch{})
		errNil(t, "UpdateGrant", err)
		if ok {
			t.Error("UpdateGrant: want ok=false on error")
		}
	}
	{
		u, err := pg.GrantUsageOf("g", now)
		errNil(t, "GrantUsageOf", err)
		if u.DayTokens != 0 || u.MonthTokens != 0 {
			t.Error("GrantUsageOf: want zero usage on error")
		}
	}
	errNil(t, "AddGrantUsage", pg.AddGrantUsage("g", 5, now))

	// --- safety: CSAM / reports / bans / strikes / appeals ---
	{
		id, err := pg.PreserveCSAM(CSAMIncident{Pseudonym: "p", Content: []byte("x")})
		errNil(t, "PreserveCSAM", err)
		if id != 0 {
			t.Error("PreserveCSAM: want id=0 on error")
		}
	}
	{
		cs, err := pg.PendingCSAMReports(10)
		errEmpty(t, "PendingCSAMReports", cs, err)
	}
	errNil(t, "MarkCSAMReported", pg.MarkCSAMReported(1))
	{
		id, err := pg.AddReport(Report{Category: "spam", NodeID: "n"})
		errNil(t, "AddReport", err)
		if id != 0 {
			t.Error("AddReport: want id=0 on error")
		}
	}
	{
		n, err := pg.ReportCountByNode("n")
		errZeroI(t, "ReportCountByNode", n, err)
	}
	{
		n, err := pg.DistinctReporterCountByNode("n", 0)
		errZeroI(t, "DistinctReporterCountByNode", n, err)
	}
	{
		rs, err := pg.ReportsByNode("n", 10)
		errEmpty(t, "ReportsByNode", rs, err)
	}
	errNil(t, "BanNode", pg.BanNode("n", "r"))
	errNil(t, "UnbanNode", pg.UnbanNode("n"))
	{
		ids, err := pg.ExpireNodeBans(now)
		errEmpty(t, "ExpireNodeBans", ids, err)
	}
	{
		m, err := pg.BannedNodes()
		errNil(t, "BannedNodes", err)
		if len(m) != 0 {
			t.Error("BannedNodes: want empty map on error")
		}
	}
	{
		n, err := pg.OwnerStrike("a", "kind", "", "idem")
		errZeroI(t, "OwnerStrike", n, err)
	}
	{
		ss, err := pg.StrikesByOwner("a", 10)
		errEmpty(t, "StrikesByOwner", ss, err)
	}
	{
		w, d, err := pg.OwnerStrikeStats("a", 0)
		errNil(t, "OwnerStrikeStats", err)
		if w != 0 || d != 0 {
			t.Error("OwnerStrikeStats: want zero counts on error")
		}
	}
	{
		id, err := pg.AddAppeal(Appeal{AccountID: "a", Reason: "r"})
		errNil(t, "AddAppeal", err)
		if id != 0 {
			t.Error("AddAppeal: want id=0 on error")
		}
	}
	{
		as, err := pg.AppealsByOwner("a", 10)
		errEmpty(t, "AppealsByOwner", as, err)
	}
	{
		as, err := pg.PendingAppeals(10)
		errEmpty(t, "PendingAppeals", as, err)
	}
	errNil(t, "BanOwner", pg.BanOwner("a", "r", ""))
	{
		banned, _, err := pg.IsOwnerBanned("a")
		errNil(t, "IsOwnerBanned", err)
		if banned {
			t.Error("IsOwnerBanned: want banned=false on error")
		}
	}
	{
		m, err := pg.BannedOwners()
		errNil(t, "BannedOwners", err)
		if len(m) != 0 {
			t.Error("BannedOwners: want empty map on error")
		}
	}
	{
		n, err := pg.ForgiveOwner("a")
		errZeroI(t, "ForgiveOwner", n, err)
	}

	// --- admin aggregates ---
	{
		_, err := pg.AdminFinancials(now)
		errNil(t, "AdminFinancials", err)
	}
	{
		_, err := pg.AdminMarketTotals(0, now.Unix())
		errNil(t, "AdminMarketTotals", err)
	}
	{
		rows, err := pg.AdminPayoutQueue(now, 10)
		errEmpty(t, "AdminPayoutQueue", rows, err)
	}
	{
		ps, err := pg.AdminAllPayouts(10)
		errEmpty(t, "AdminAllPayouts", ps, err)
	}
	{
		_, err := pg.AdminAbuse()
		errNil(t, "AdminAbuse", err)
	}
	{
		rows, err := pg.AdminActivity(10)
		errEmpty(t, "AdminActivity", rows, err)
	}
}
