package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// newSeriesBroker wires a broker whose operator (gid 7 -> wallet u_gh_7, node-1) has a
// handful of settled receipts with UNIQUE request ids spread across TWO UTC days, so
// the time-series exercises multiple day buckets + a per-model split. It returns the
// broker + the operator's signing key (for the signed-CLI auth path).
func newSeriesBroker(t *testing.T) (*broker, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	_, bpriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: pubHex})
	node := "node-1"
	_ = db.BindNode(node, pubHex)
	wallet := "u_gh_7"

	id := 0
	settle := func(model string, in, out int, cost, share float64, ts int64) {
		id++
		if cost > 0 {
			_, _ = db.AddCredits(wallet, cost)
		}
		_, _ = db.Settle(wallet, node, cost, share, protocol.UsageReceipt{
			RequestID: "req-" + strconv.Itoa(id), Model: model,
			PromptTokens: in, CompletionTokens: out, TS: ts,
		})
	}
	now := time.Now().UTC()
	today := now.Unix()
	// Yesterday at the same clock time (a distinct UTC day bucket as long as it is not
	// midnight; pin to noon-ish to avoid a boundary flake).
	noonYesterday := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC).AddDate(0, 0, -1).Unix()

	settle("modelA", 10, 20, 0.50, 0.35, today)                   // today: paid
	settle("modelA", 5, 5, 0, 0, today)                           // today: free
	settle("modelB", 100, 200, 1.00, 0.70, today)                 // today: paid
	settle("modelA", 1, 1, 0.10, 0.07, noonYesterday)             // yesterday: paid
	settle("old", 1, 1, 9.0, 6.0, now.AddDate(0, 0, -400).Unix()) // far outside the window

	b := &broker{priv: bpriv, db: db, seedFunds: 0, conn: loadConnect(), pubOfUser: map[string]string{}}
	b.bill.creditUSD = 1
	return b, priv
}

// TestMetricsSeriesShape locks the /metrics/series shape: per-day buckets, the
// per-model split inside a bucket, the spend/earned figures, and the savings-vs-
// frontier rollup. Both auth paths (web session + signed Ed25519) are served.
func TestMetricsSeriesShape(t *testing.T) {
	b, priv := newSeriesBroker(t)

	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{"session", sessionReq(b, http.MethodGet, "/metrics/series?days=30", "octocat", 7)},
		{"signed", signedMetricsGET("/metrics/series?days=30", "/metrics/series", priv)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			b.metricsSeries(w, tc.req)
			if w.Code != http.StatusOK {
				t.Fatalf("series %s = %d, want 200 (body=%s)", tc.name, w.Code, w.Body.String())
			}
			var resp struct {
				PeriodDays int           `json:"period_days"`
				IsConsumer bool          `json:"is_consumer"`
				IsProvider bool          `json:"is_provider"`
				Daily      []seriesPoint `json:"daily"`
				Hourly     []seriesPoint `json:"hourly"`
				Savings    struct {
					BaselineModel string           `json:"baseline_model"`
					SpendUSD      float64          `json:"spend_usd"`
					FrontierEst   float64          `json:"frontier_est"`
					SavingsEst    float64          `json:"savings_est"`
					Reference     []frontierRefEst `json:"reference"`
				} `json:"savings"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.PeriodDays != 30 {
				t.Errorf("period_days = %d, want 30", resp.PeriodDays)
			}
			if !resp.IsConsumer || !resp.IsProvider {
				t.Errorf("is_consumer=%v is_provider=%v, want both true", resp.IsConsumer, resp.IsProvider)
			}
			// The fixture spans TWO UTC days (3 receipts today, 1 yesterday); the -400d
			// "old" row is outside the 30d window. Buckets are oldest-first (chart order).
			if len(resp.Daily) != 2 {
				t.Fatalf("daily buckets = %d, want 2 (%+v)", len(resp.Daily), resp.Daily)
			}
			day := resp.Daily[1]   // newest bucket = today
			if day.Requests != 3 { // modelA paid, modelA free, modelB paid
				t.Errorf("today.requests = %d, want 3", day.Requests)
			}
			// today spend: 0.50 + 1.00 = 1.50; earned: 0.35 + 0.70 = 1.05.
			if day.Spend < 1.4999 || day.Spend > 1.5001 {
				t.Errorf("today.spend = %v, want 1.50", day.Spend)
			}
			if day.Earned < 1.0499 || day.Earned > 1.0501 {
				t.Errorf("today.earned = %v, want 1.05", day.Earned)
			}
			// per-model split inside the bucket: modelA + modelB present (one request each
			// counts ONCE even though the caller both consumed AND served it - self-serve).
			if len(day.Models) != 2 {
				t.Fatalf("today.models = %d, want 2 (%+v)", len(day.Models), day.Models)
			}
			var sawA, sawB bool
			for _, m := range day.Models {
				if m.Model == "modelA" {
					sawA = true
					if m.Requests != 2 { // paid + free
						t.Errorf("modelA requests = %d, want 2", m.Requests)
					}
				}
				if m.Model == "modelB" {
					sawB = true
				}
			}
			if !sawA || !sawB {
				t.Errorf("per-model split missing modelA/modelB: %+v", day.Models)
			}
			// Savings frontier total (gpt-4o 2.50 in / 10.00 out per 1M) over ALL consumed
			// tokens in-window: today modelA 10/20 + 5/5, modelB 100/200; yesterday modelA
			// 1/1 -> in=116, out=226. frontier = (116*2.50 + 226*10.00)/1e6.
			wantFrontier := (116*2.50 + 226*10.00) / 1e6
			if resp.Savings.FrontierEst < wantFrontier-1e-9 || resp.Savings.FrontierEst > wantFrontier+1e-9 {
				t.Errorf("savings.frontier_est = %v, want %v", resp.Savings.FrontierEst, wantFrontier)
			}
			if resp.Savings.BaselineModel != frontierBaseline {
				t.Errorf("savings.baseline_model = %q, want %q", resp.Savings.BaselineModel, frontierBaseline)
			}
			if len(resp.Savings.Reference) != len(frontierTable) {
				t.Errorf("savings.reference len = %d, want %d", len(resp.Savings.Reference), len(frontierTable))
			}
			// Each reference model carries THIS account's frontier_est = frontierCost over the
			// in-window tokens (in=116, out=226), and the baseline row equals the headline
			// frontier_est (linear in tokens) - the consistency the dashboard "vs <model>" toggle relies on.
			for _, ref := range resp.Savings.Reference {
				want := round6(frontierCost(frontierTable, 116, 226, ref.Model))
				if ref.FrontierEst < want-1e-9 || ref.FrontierEst > want+1e-9 {
					t.Errorf("reference %q frontier_est = %v, want %v", ref.Model, ref.FrontierEst, want)
				}
				if ref.Model == frontierBaseline && (ref.FrontierEst < resp.Savings.FrontierEst-1e-9 || ref.FrontierEst > resp.Savings.FrontierEst+1e-9) {
					t.Errorf("baseline reference frontier_est = %v, want headline %v", ref.FrontierEst, resp.Savings.FrontierEst)
				}
			}
			// Here spend (1.50) exceeds the frontier estimate (tiny token counts), so the
			// per-bucket floor pins savings at 0 - it must never go negative.
			if resp.Savings.SavingsEst < 0 {
				t.Errorf("savings.savings_est = %v, must be >= 0", resp.Savings.SavingsEst)
			}
		})
	}
}

// TestMetricsSeriesSavingsPositive proves the savings math when frontier list prices
// DWARF the RogerAI spend (the real-world case: big token counts, cheap RogerAI cost).
func TestMetricsSeriesSavingsPositive(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)
	_, bpriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	wallet := "u_gh_7"
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: pubHex})
	now := time.Now()
	// 1,000,000 in + 1,000,000 out consumed for a RogerAI cost of $0.10. gpt-4o list
	// would be 2.50 + 10.00 = $12.50, so savings ~ $12.40.
	_, _ = db.AddCredits(wallet, 0.10)
	_, _ = db.Settle(wallet, "node-x", 0.10, 0, protocol.UsageReceipt{
		RequestID: "big", Model: "gpt-4o", PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TS: now.Unix(),
	})
	b := &broker{priv: bpriv, db: db, seedFunds: 0, conn: loadConnect(), pubOfUser: map[string]string{}}
	b.bill.creditUSD = 1
	_ = priv

	w := httptest.NewRecorder()
	b.metricsSeries(w, sessionReq(b, http.MethodGet, "/metrics/series?days=7", "octocat", 7))
	if w.Code != http.StatusOK {
		t.Fatalf("series = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Savings struct {
			SpendUSD    float64 `json:"spend_usd"`
			FrontierEst float64 `json:"frontier_est"`
			SavingsEst  float64 `json:"savings_est"`
		} `json:"savings"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Savings.FrontierEst < 12.4999 || resp.Savings.FrontierEst > 12.5001 {
		t.Errorf("frontier_est = %v, want 12.50", resp.Savings.FrontierEst)
	}
	wantSavings := 12.50 - 0.10
	if resp.Savings.SavingsEst < wantSavings-1e-4 || resp.Savings.SavingsEst > wantSavings+1e-4 {
		t.Errorf("savings_est = %v, want %v", resp.Savings.SavingsEst, wantSavings)
	}
}

// TestLiveFrontierTableTracksSync proves the savings baseline tracks the live OpenRouter
// sync (refprices.go) instead of going stale on a hard-coded number: a synced gpt-4o OUT
// price overrides the static seed's OUT, while the IN price (not synced) stays from the
// seed; a model with no live price falls back to the static seed entirely.
func TestLiveFrontierTableTracksSync(t *testing.T) {
	b := &broker{refPrices: map[string]float64{"gpt-4o": 20.0}}
	var gpt4o frontierRef
	for _, f := range b.liveFrontierTable() {
		if f.Model == "gpt-4o" {
			gpt4o = f
		}
	}
	if gpt4o.OutPer1M != 20.0 {
		t.Errorf("live gpt-4o OUT = %v, want 20.0 (synced override)", gpt4o.OutPer1M)
	}
	if gpt4o.InPer1M != 2.50 {
		t.Errorf("live gpt-4o IN = %v, want 2.50 (static seed; IN is not synced)", gpt4o.InPer1M)
	}
	// 1M in + 1M out at the LIVE baseline = 2.50 + 20.00 = 22.50 (not the stale 12.50).
	if got := frontierCost(b.liveFrontierTable(), 1_000_000, 1_000_000, ""); got < 22.4999 || got > 22.5001 {
		t.Errorf("frontierCost(live baseline) = %v, want 22.50", got)
	}
	// No live price -> the static offline seed (gpt-4o 2.50 + 10.00 = 12.50).
	if got := frontierCost((&broker{}).liveFrontierTable(), 1_000_000, 1_000_000, ""); got < 12.4999 || got > 12.5001 {
		t.Errorf("frontierCost(seed baseline) = %v, want 12.50 (static offline seed)", got)
	}
}

// TestMetricsSeriesAuth covers the auth contract: anon + signed-but-unbound are 401.
func TestMetricsSeriesAuth(t *testing.T) {
	b, _, _ := newMetricsBroker(t)
	w := httptest.NewRecorder()
	b.metricsSeries(w, httptest.NewRequest(http.MethodGet, "/metrics/series", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("anon series = %d, want 401", w.Code)
	}
	_, unbound, _ := ed25519.GenerateKey(nil)
	w2 := httptest.NewRecorder()
	b.metricsSeries(w2, signedReq(http.MethodGet, "/metrics/series", nil, unbound))
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("unbound series = %d, want 401", w2.Code)
	}
}

// TestMetricsSeriesCrossAccount proves an authed owner reads ONLY their own data: a
// SECOND operator account (different node, different receipts) sees only its own
// figures, never the first account's. Mirrors the /earnings cross-account guarantee.
func TestMetricsSeriesCrossAccount(t *testing.T) {
	// Account A: the fixture (octocat / gid 7 / node-1, spend 1.50, earned 1.05).
	b, _, _ := newMetricsBroker(t)

	// Account B: a different github wallet + a different bound node, with its OWN one
	// settled receipt - and NO consumption.
	pubB, _, _ := ed25519.GenerateKey(nil)
	pubBHex := hex.EncodeToString(pubB)
	_ = b.db.BindOwner(store.Owner{GitHubID: 99, Login: "mona", Pubkey: pubBHex})
	_ = b.db.BindNode("node-B", pubBHex)
	walletB := "u_gh_99"
	_, _ = b.db.AddCredits(walletB, 5.0)
	_, _ = b.db.Settle(walletB, "node-B", 5.0, 3.5, protocol.UsageReceipt{
		RequestID: "rB", Model: "modelZ", PromptTokens: 7, CompletionTokens: 8, TS: time.Now().Unix(),
	})

	// B reads its OWN series via a web session for gid 99: it must see modelZ only,
	// spend 5.0 / earned 3.5 - NOT account A's modelA/modelB or its 1.50/1.05.
	w := httptest.NewRecorder()
	b.metricsSeries(w, sessionReq(b, http.MethodGet, "/metrics/series?days=30", "mona", 99))
	if w.Code != http.StatusOK {
		t.Fatalf("B series = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Daily []seriesPoint `json:"daily"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Daily) != 1 {
		t.Fatalf("B daily = %d, want 1", len(resp.Daily))
	}
	day := resp.Daily[0]
	if day.Requests != 1 {
		t.Errorf("B requests = %d, want 1 (must not see account A)", day.Requests)
	}
	if day.Spend < 4.9999 || day.Spend > 5.0001 {
		t.Errorf("B spend = %v, want 5.0", day.Spend)
	}
	if day.Earned < 3.4999 || day.Earned > 3.5001 {
		t.Errorf("B earned = %v, want 3.5", day.Earned)
	}
	for _, m := range day.Models {
		if m.Model == "modelA" || m.Model == "modelB" {
			t.Errorf("B leaked account A model %q", m.Model)
		}
	}
}

// TestConsoleOwner locks the owner /console view: real receipts projected to lineage
// events + the owner counters (requests_today, earned_today, active_nodes).
func TestConsoleOwner(t *testing.T) {
	b, _, priv := newMetricsBroker(t)

	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{"session", sessionReq(b, http.MethodGet, "/console", "octocat", 7)},
		{"signed", signedMetricsGET("/console", "/console", priv)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			b.console(w, tc.req)
			if w.Code != http.StatusOK {
				t.Fatalf("console %s = %d, want 200 (%s)", tc.name, w.Code, w.Body.String())
			}
			var resp struct {
				Role     string         `json:"role"`
				Events   []consoleEvent `json:"events"`
				Counters map[string]any `json:"counters"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Role != "owner" {
				t.Errorf("role = %q, want owner", resp.Role)
			}
			// node-1 served 3 in-window receipts + 1 old; all 4 are this node's, recent.
			if len(resp.Events) != 4 {
				t.Fatalf("events = %d, want 4 (%+v)", len(resp.Events), resp.Events)
			}
			for _, e := range resp.Events {
				if e.Node != "node-1" {
					t.Errorf("event node = %q, want node-1", e.Node)
				}
				if !e.Success {
					t.Errorf("settled receipt must report success=true")
				}
				if e.RequestID == "" {
					t.Errorf("event missing request_id (the chain/receipt id)")
				}
			}
			// today: the 3 in-window receipts (the old one is -400d).
			if got := resp.Counters["requests_today"].(float64); got != 3 {
				t.Errorf("requests_today = %v, want 3", got)
			}
			if _, ok := resp.Counters["earned_today"]; !ok {
				t.Errorf("owner counters missing earned_today")
			}
			if got := resp.Counters["active_nodes"].(float64); got != 1 {
				t.Errorf("active_nodes = %v, want 1", got)
			}
		})
	}
}

// TestConsoleEmpty is the honest empty state: a logged-in consumer wallet with NO
// receipts gets role=consumer, an empty (non-null) events array, and zero counters.
func TestConsoleEmpty(t *testing.T) {
	_, bpriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	// Seed the github wallet so it is "logged in" but has no receipts.
	_, _ = db.AddCredits("u_gh_42", 1.0)
	b := &broker{priv: bpriv, db: db, seedFunds: 0, conn: loadConnect(), pubOfUser: map[string]string{}}
	b.bill.creditUSD = 1

	w := httptest.NewRecorder()
	b.console(w, sessionReq(b, http.MethodGet, "/console", "ghost", 42))
	if w.Code != http.StatusOK {
		t.Fatalf("console = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	// events must be [] not null.
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	if string(raw["events"]) != "[]" {
		t.Errorf("empty events = %s, want []", string(raw["events"]))
	}
	var resp struct {
		Role     string         `json:"role"`
		Events   []consoleEvent `json:"events"`
		Counters map[string]any `json:"counters"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Role != "consumer" {
		t.Errorf("role = %q, want consumer", resp.Role)
	}
	if len(resp.Events) != 0 {
		t.Errorf("events = %d, want 0", len(resp.Events))
	}
	if got := resp.Counters["requests_today"].(float64); got != 0 {
		t.Errorf("requests_today = %v, want 0", got)
	}
	if got := resp.Counters["spend_today"].(float64); got != 0 {
		t.Errorf("spend_today = %v, want 0", got)
	}
}

// TestConsoleAuth: anon + signed-but-unbound are 401 on /console.
func TestConsoleAuth(t *testing.T) {
	b, _, _ := newMetricsBroker(t)
	w := httptest.NewRecorder()
	b.console(w, httptest.NewRequest(http.MethodGet, "/console", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("anon console = %d, want 401", w.Code)
	}
	_, unbound, _ := ed25519.GenerateKey(nil)
	w2 := httptest.NewRecorder()
	b.console(w2, signedReq(http.MethodGet, "/console", nil, unbound))
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("unbound console = %d, want 401", w2.Code)
	}
}

// TestMetricsSeriesCachePerUserIsolation is the end-to-end security guarantee for the
// authed-feed cache: with a SHARED Valkey wired in, user A loading /metrics/series
// (which populates the cache) must NEVER cause user B to receive A's data. Each
// identity keys its own cache entry, so B always sees B's own spend.
func TestMetricsSeriesCachePerUserIsolation(t *testing.T) {
	vs, _ := newTestValkey(t)

	// Two pure consumers (distinct github-scoped wallets) with DIFFERENT spend.
	db := store.NewMem()
	_, bpriv, _ := ed25519.GenerateKey(nil)
	now := time.Now().Unix()
	settle := func(wallet, model string, cost float64, id string) {
		_, _ = db.AddCredits(wallet, cost)
		_, _ = db.Settle(wallet, "", cost, 0, protocol.UsageReceipt{
			RequestID: id, Model: model, PromptTokens: 10, CompletionTokens: 10, TS: now,
		})
	}
	settle("u_gh_7", "modelA", 0.50, "a1") // user A: $0.50
	settle("u_gh_8", "modelB", 7.00, "b1") // user B: $7.00

	b := &broker{priv: bpriv, db: db, seedFunds: 0, conn: loadConnect(),
		pubOfUser: map[string]string{}, shared: vs}
	b.bill.creditUSD = 1

	spendOf := func(login string, gid int64) float64 {
		w := httptest.NewRecorder()
		b.metricsSeries(w, sessionReq(b, http.MethodGet, "/metrics/series?days=30", login, gid))
		if w.Code != http.StatusOK {
			t.Fatalf("series for %s = %d (%s)", login, w.Code, w.Body.String())
		}
		var resp struct {
			Savings struct {
				SpendUSD float64 `json:"spend_usd"`
			} `json:"savings"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.Savings.SpendUSD
	}

	// A loads first (populates A's cache entry).
	if got := spendOf("alice", 7); got < 0.4999 || got > 0.5001 {
		t.Fatalf("user A spend = %v, want 0.50", got)
	}
	// B must see B's OWN spend, never A's cached 0.50.
	if got := spendOf("bob", 8); got < 6.9999 || got > 7.0001 {
		t.Errorf("user B spend = %v, want 7.00 - A's cached data must NOT leak to B", got)
	}
	// A again -> still A's own data (served from A's cache entry).
	if got := spendOf("alice", 7); got < 0.4999 || got > 0.5001 {
		t.Errorf("user A re-fetch spend = %v, want 0.50", got)
	}
}
