package store

import "sort"

// This file is the per-model METRICS rollups: what an account SERVES (as a provider)
// and what it CONSUMES (as a consumer), aggregated from the receipts (the source of
// truth) grouped by model over a trailing time window. Both views split free-vs-paid
// per request: a $0 request (free model / self-use / a free window) is "free", any
// request with a non-zero charge is "paid".
//
// Provider side keys off owner_share (the owner's 70% net share already stored per
// receipt) and the node->account binding; consumer side keys off cost. The numbers
// are receipt-derived so they never drift from the ledger/earnings they roll up.

// ProviderModelMetric is one (model, node) row of what an account's node served.
type ProviderModelMetric struct {
	Model        string  `json:"model"`
	NodeID       string  `json:"node_id"`
	Requests     int64   `json:"requests"`
	TokensIn     int64   `json:"tokens_in"`
	TokensOut    int64   `json:"tokens_out"`
	FreeRequests int64   `json:"free_requests"`
	PaidRequests int64   `json:"paid_requests"`
	FreeTokens   int64   `json:"free_tokens"`
	PaidTokens   int64   `json:"paid_tokens"`
	EarningsUSD  float64 `json:"earnings_usd"` // owner's 70% share, in credits ($ at credit_usd=1)
}

// UsageModelMetric is one model row of what an account consumed.
type UsageModelMetric struct {
	Model        string  `json:"model"`
	Requests     int64   `json:"requests"`
	TokensIn     int64   `json:"tokens_in"`
	TokensOut    int64   `json:"tokens_out"`
	FreeRequests int64   `json:"free_requests"`
	PaidRequests int64   `json:"paid_requests"`
	SpendUSD     float64 `json:"spend_usd"`
}

// ProviderMetrics returns the account's per-(model,node) serve breakdown over the
// [since,until) unix window. accountID is the owner pubkey; the receipts are scoped
// to the nodes bound to that account. Rows are sorted by earnings desc (then model).
func (m *Mem) ProviderMetrics(accountID string, since, until int64) ([]ProviderModelMetric, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// nodes bound to this account.
	owned := map[string]bool{}
	for n, a := range m.nodeAcct {
		if a == accountID {
			owned[n] = true
		}
	}
	type key struct{ model, node string }
	agg := map[key]*ProviderModelMetric{}
	for _, e := range m.entries {
		if !owned[e.Node] {
			continue
		}
		if e.TS < since || e.TS >= until {
			continue
		}
		k := key{model: modelKey(e.Model), node: e.Node}
		row := agg[k]
		if row == nil {
			row = &ProviderModelMetric{Model: k.model, NodeID: k.node}
			agg[k] = row
		}
		accProvider(row, e)
	}
	out := make([]ProviderModelMetric, 0, len(agg))
	for _, r := range agg {
		r.EarningsUSD = round6(r.EarningsUSD)
		out = append(out, *r)
	}
	sortProvider(out)
	return out, nil
}

// UsageMetrics returns the wallet's per-model consume breakdown over the
// [since,until) unix window. Rows are sorted by spend desc (then model).
func (m *Mem) UsageMetrics(wallet string, since, until int64) ([]UsageModelMetric, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	agg := map[string]*UsageModelMetric{}
	for _, e := range m.entries {
		if e.User != wallet {
			continue
		}
		if e.TS < since || e.TS >= until {
			continue
		}
		mk := modelKey(e.Model)
		row := agg[mk]
		if row == nil {
			row = &UsageModelMetric{Model: mk}
			agg[mk] = row
		}
		accUsage(row, e)
	}
	out := make([]UsageModelMetric, 0, len(agg))
	for _, r := range agg {
		r.SpendUSD = round6(r.SpendUSD)
		out = append(out, *r)
	}
	sortUsage(out)
	return out, nil
}

// modelKey normalizes an empty model name so it groups under a stable bucket.
func modelKey(model string) string {
	if model == "" {
		return "unknown"
	}
	return model
}

// accProvider folds one receipt into a provider row. free = no owner earnings on the
// request (a $0 / free-window / self-use serve); paid = a positive owner share.
func accProvider(row *ProviderModelMetric, e Entry) {
	in := int64(e.PromptTokens)
	out := int64(e.CompletionTokens)
	row.Requests++
	row.TokensIn += in
	row.TokensOut += out
	row.EarningsUSD += e.OwnerShare
	if e.OwnerShare > 0 {
		row.PaidRequests++
		row.PaidTokens += in + out
	} else {
		row.FreeRequests++
		row.FreeTokens += in + out
	}
}

// accUsage folds one receipt into a consumer row. free = a $0 request (free model /
// free window / self-use); paid = a positive charge.
func accUsage(row *UsageModelMetric, e Entry) {
	row.Requests++
	row.TokensIn += int64(e.PromptTokens)
	row.TokensOut += int64(e.CompletionTokens)
	row.SpendUSD += e.Cost
	if e.Cost > 0 {
		row.PaidRequests++
	} else {
		row.FreeRequests++
	}
}

func sortProvider(rows []ProviderModelMetric) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].EarningsUSD != rows[j].EarningsUSD {
			return rows[i].EarningsUSD > rows[j].EarningsUSD
		}
		if rows[i].Model != rows[j].Model {
			return rows[i].Model < rows[j].Model
		}
		return rows[i].NodeID < rows[j].NodeID
	})
}

func sortUsage(rows []UsageModelMetric) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].SpendUSD != rows[j].SpendUSD {
			return rows[i].SpendUSD > rows[j].SpendUSD
		}
		return rows[i].Model < rows[j].Model
	})
}

// --- Postgres -------------------------------------------------------------

// ProviderMetrics aggregates the account's served receipts per (model, node) with a
// single GROUP BY over the receipts joined to the account's node bindings, bounded by
// the [since,until) ts window. Free vs paid is split on owner_share>0.
func (p *Postgres) ProviderMetrics(accountID string, since, until int64) ([]ProviderModelMetric, error) {
	rows, err := p.db.Query(`
		SELECT r.model, r.node,
		       COUNT(*),
		       COALESCE(SUM(r.prompt_tokens),0),
		       COALESCE(SUM(r.completion_tokens),0),
		       COUNT(*) FILTER (WHERE r.owner_share <= 0),
		       COUNT(*) FILTER (WHERE r.owner_share > 0),
		       COALESCE(SUM(CASE WHEN r.owner_share <= 0 THEN r.prompt_tokens + r.completion_tokens ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN r.owner_share > 0  THEN r.prompt_tokens + r.completion_tokens ELSE 0 END),0),
		       COALESCE(SUM(r.owner_share),0)
		FROM rogerai.receipts r
		JOIN rogerai.node_owner o ON o.node = r.node
		WHERE o.account_id = $1 AND r.ts >= $2 AND r.ts < $3
		GROUP BY r.model, r.node`, accountID, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderModelMetric
	for rows.Next() {
		var r ProviderModelMetric
		if err := rows.Scan(&r.Model, &r.NodeID, &r.Requests, &r.TokensIn, &r.TokensOut,
			&r.FreeRequests, &r.PaidRequests, &r.FreeTokens, &r.PaidTokens, &r.EarningsUSD); err != nil {
			return nil, err
		}
		r.Model = modelKey(r.Model)
		r.EarningsUSD = round6(r.EarningsUSD)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortProvider(out)
	return out, nil
}

// UsageMetrics aggregates the wallet's consumed receipts per model with a single
// GROUP BY bounded by the [since,until) ts window. Free vs paid is split on cost>0.
func (p *Postgres) UsageMetrics(wallet string, since, until int64) ([]UsageModelMetric, error) {
	rows, err := p.db.Query(`
		SELECT r.model,
		       COUNT(*),
		       COALESCE(SUM(r.prompt_tokens),0),
		       COALESCE(SUM(r.completion_tokens),0),
		       COUNT(*) FILTER (WHERE r.cost <= 0),
		       COUNT(*) FILTER (WHERE r.cost > 0),
		       COALESCE(SUM(r.cost),0)
		FROM rogerai.receipts r
		WHERE r.usr = $1 AND r.ts >= $2 AND r.ts < $3
		GROUP BY r.model`, wallet, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageModelMetric
	for rows.Next() {
		var r UsageModelMetric
		if err := rows.Scan(&r.Model, &r.Requests, &r.TokensIn, &r.TokensOut,
			&r.FreeRequests, &r.PaidRequests, &r.SpendUSD); err != nil {
			return nil, err
		}
		r.Model = modelKey(r.Model)
		r.SpendUSD = round6(r.SpendUSD)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortUsage(out)
	return out, nil
}

// round6 rounds a credit amount to 6 decimal places (the dashboard precision), so the
// summed earnings/spend don't carry float drift into the JSON.
func round6(f float64) float64 {
	return float64(int64(f*1e6+0.5)) / 1e6
}
