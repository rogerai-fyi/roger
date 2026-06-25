package store

import (
	"os"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// metricsStores runs the per-model metrics suite against Mem always, and Postgres when
// ROGERAI_TEST_DATABASE_URL is set, so the provider/usage rollups (the GROUP BY and the
// iterate) behave identically on both backends.
func metricsStores(t *testing.T) map[string]Store {
	t.Helper()
	out := map[string]Store{"mem": NewMem()}
	if dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL"); dsn != "" {
		pg, err := NewPostgres(dsn)
		if err != nil {
			t.Fatalf("postgres: %v", err)
		}
		out["postgres"] = pg
	}
	return out
}

// serveAt records ONE served+settled request at unix `ts`: the consumer pays `cost`,
// the node owner earns `ownerShare`, and a receipt with the model + tokens is written.
// The wallet is funded first so the debit lands.
func serveAt(t *testing.T, db Store, user, node, model string, in, out int, cost, ownerShare float64, ts int64) {
	t.Helper()
	if cost > 0 {
		if _, err := db.AddCredits(user, cost); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Settle(user, node, cost, ownerShare, protocol.UsageReceipt{
		RequestID:        "r-" + time.Unix(ts, 0).UTC().Format("20060102-150405.000000000") + "-" + model,
		Model:            model,
		PromptTokens:     in,
		CompletionTokens: out,
		TS:               ts,
	}); err != nil {
		t.Fatal(err)
	}
}

func findProvider(rows []ProviderModelMetric, model, node string) (ProviderModelMetric, bool) {
	for _, r := range rows {
		if r.Model == model && r.NodeID == node {
			return r, true
		}
	}
	return ProviderModelMetric{}, false
}

func findUsage(rows []UsageModelMetric, model string) (UsageModelMetric, bool) {
	for _, r := range rows {
		if r.Model == model {
			return r, true
		}
	}
	return UsageModelMetric{}, false
}

// TestMetricsStoreParity covers per-model aggregation, the free-vs-paid split, the
// window boundary (a row just outside the window is excluded), node scoping, and
// Mem+Postgres parity.
func TestMetricsStoreParity(t *testing.T) {
	for name, db := range metricsStores(t) {
		t.Run(name, func(t *testing.T) {
			acct := "pk_owner"   // owner pubkey (the account id)
			node := "node-1"     // bound to acct
			other := "node-evil" // a DIFFERENT account's node (must be excluded)
			user := "u_gh_1"     // the consumer wallet
			_ = db.BindNode(node, acct)
			_ = db.BindNode(other, "pk_other")

			now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
			days := 30
			since := now.Add(-time.Duration(days) * 24 * time.Hour).Unix()
			until := now.Unix()
			inWindow := now.Add(-5 * 24 * time.Hour).Unix() // 5 days ago -> included
			justOut := since - 1                            // 1s before the window -> excluded
			atStart := since                                // exactly at since -> included ([since,until))

			// Two models on the owner's node. modelA: one PAID serve + one FREE serve.
			// modelB: one paid serve. All inside the window.
			serveAt(t, db, user, node, "modelA", 10, 20, 0.50, 0.35, inWindow) // paid (owner earns 0.35)
			serveAt(t, db, user, node, "modelA", 5, 7, 0, 0, atStart)          // FREE (cost 0, no earn)
			serveAt(t, db, user, node, "modelB", 100, 200, 1.00, 0.70, inWindow)

			// A serve JUST outside the window (excluded from both views).
			serveAt(t, db, user, node, "modelA", 999, 999, 9.99, 6.99, justOut)

			// A serve on ANOTHER account's node (excluded from THIS account's provider view,
			// but it IS this user's consumption, so it appears in the usage view).
			serveAt(t, db, user, other, "modelC", 3, 4, 0.20, 0.14, inWindow)

			// --- Provider view (account-scoped) ---
			prov, err := db.ProviderMetrics(acct, since, until)
			if err != nil {
				t.Fatal(err)
			}
			// Only the owner's node + in-window rows: (modelA,node) and (modelB,node).
			if len(prov) != 2 {
				t.Fatalf("provider rows = %d, want 2 (%+v)", len(prov), prov)
			}
			a, ok := findProvider(prov, "modelA", node)
			if !ok {
				t.Fatalf("missing provider (modelA,%s)", node)
			}
			// modelA: 2 requests (1 paid + 1 free), tokens summed, earnings = 0.35.
			if a.Requests != 2 || a.PaidRequests != 1 || a.FreeRequests != 1 {
				t.Errorf("modelA req split = {req:%d paid:%d free:%d}, want {2 1 1}", a.Requests, a.PaidRequests, a.FreeRequests)
			}
			if a.TokensIn != 15 || a.TokensOut != 27 {
				t.Errorf("modelA tokens = in:%d out:%d, want in:15 out:27", a.TokensIn, a.TokensOut)
			}
			if a.PaidTokens != 30 || a.FreeTokens != 12 { // paid=10+20, free=5+7
				t.Errorf("modelA paid/free tokens = %d/%d, want 30/12", a.PaidTokens, a.FreeTokens)
			}
			if !approx(a.EarningsUSD, 0.35) {
				t.Errorf("modelA earnings = %v, want 0.35 (the excluded out-of-window serve must NOT count)", a.EarningsUSD)
			}
			b, ok := findProvider(prov, "modelB", node)
			if !ok || b.Requests != 1 || !approx(b.EarningsUSD, 0.70) {
				t.Errorf("modelB row = %+v ok=%v, want 1 req / 0.70 earnings", b, ok)
			}
			// The other account's node must NOT appear in this account's provider view.
			if _, ok := findProvider(prov, "modelC", other); ok {
				t.Errorf("provider view leaked another account's node serve")
			}

			// --- Usage view (wallet-scoped) ---
			use, err := db.UsageMetrics(user, since, until)
			if err != nil {
				t.Fatal(err)
			}
			// modelA, modelB, modelC consumed in-window (the out-of-window modelA excluded).
			if len(use) != 3 {
				t.Fatalf("usage rows = %d, want 3 (%+v)", len(use), use)
			}
			ua, _ := findUsage(use, "modelA")
			if ua.Requests != 2 || ua.PaidRequests != 1 || ua.FreeRequests != 1 {
				t.Errorf("usage modelA split = {req:%d paid:%d free:%d}, want {2 1 1}", ua.Requests, ua.PaidRequests, ua.FreeRequests)
			}
			if ua.TokensIn != 15 || ua.TokensOut != 27 {
				t.Errorf("usage modelA tokens = in:%d out:%d, want in:15 out:27", ua.TokensIn, ua.TokensOut)
			}
			if !approx(ua.SpendUSD, 0.50) {
				t.Errorf("usage modelA spend = %v, want 0.50 (out-of-window 9.99 excluded)", ua.SpendUSD)
			}
			uc, ok := findUsage(use, "modelC")
			if !ok || !approx(uc.SpendUSD, 0.20) {
				t.Errorf("usage modelC = %+v ok=%v, want spend 0.20 (consumed on another's node, still the caller's spend)", uc, ok)
			}

			// --- Isolation: a different account / wallet sees nothing ---
			if rows, _ := db.ProviderMetrics("pk_nobody", since, until); len(rows) != 0 {
				t.Errorf("unknown account provider rows = %d, want 0", len(rows))
			}
			if rows, _ := db.UsageMetrics("u_gh_999", since, until); len(rows) != 0 {
				t.Errorf("unknown wallet usage rows = %d, want 0", len(rows))
			}
		})
	}
}

// TestWindowedEntriesParity covers the new windowed entry queries (EntriesByUser /
// EntriesByAccount) that power the time-series + console feeds: the [since,until)
// window boundary, node->account scoping, cross-account isolation, and Mem+Postgres
// parity.
func TestWindowedEntriesParity(t *testing.T) {
	for name, db := range metricsStores(t) {
		t.Run(name, func(t *testing.T) {
			defer db.Close()
			now := time.Now().UTC().Unix()
			old := now - 400*24*3600 // far outside any sane window
			user := "u_gh_7"
			_ = db.BindNode("node-A", "pk_owner")
			_ = db.BindNode("node-B", "pk_other") // a DIFFERENT account's node
			serveAt(t, db, user, "node-A", "modelA", 10, 20, 0.50, 0.35, now)
			serveAt(t, db, user, "node-A", "modelA", 5, 5, 0, 0, now)
			serveAt(t, db, user, "node-B", "modelX", 1, 1, 0.20, 0.14, now) // user consumed on another's node
			serveAt(t, db, user, "node-A", "old", 1, 1, 9.0, 6.0, old)      // outside window

			since := now - 24*3600
			until := now + 1

			// Consumer side: the user's in-window receipts (3), the old one excluded.
			ue, _ := db.EntriesByUser(user, since, until)
			if len(ue) != 3 {
				t.Fatalf("EntriesByUser = %d, want 3 (old excluded): %+v", len(ue), ue)
			}
			// Newest-first ordering.
			for i := 1; i < len(ue); i++ {
				if ue[i-1].TS < ue[i].TS {
					t.Errorf("EntriesByUser not newest-first at %d", i)
				}
			}

			// Provider side (account pk_owner = node-A only): 2 in-window receipts; the
			// node-B consume (a different account's node) is NOT counted, old excluded.
			ae, _ := db.EntriesByAccount("pk_owner", since, until)
			if len(ae) != 2 {
				t.Fatalf("EntriesByAccount(pk_owner) = %d, want 2: %+v", len(ae), ae)
			}
			for _, e := range ae {
				if e.Node != "node-A" {
					t.Errorf("EntriesByAccount leaked node %q (want node-A only)", e.Node)
				}
			}

			// Cross-account isolation: a stranger account sees nothing.
			if rows, _ := db.EntriesByAccount("pk_nobody", since, until); len(rows) != 0 {
				t.Errorf("EntriesByAccount(stranger) = %d, want 0", len(rows))
			}
			if rows, _ := db.EntriesByUser("u_gh_999", since, until); len(rows) != 0 {
				t.Errorf("EntriesByUser(stranger) = %d, want 0", len(rows))
			}
		})
	}
}
