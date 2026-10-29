// Rolling the broker's daily buckets up over a window (legacy: the rollupModels/rollupTotals
// copies in web/src/js/dashboard.js and metrics.js). One implementation for every page that
// reads /metrics/series.
import { n0 } from "#lib/format.js";

// Per-model breakdown: each model's bucket slices summed across daily[], one row per model,
// biggest by tokens first. A slice without a model name rolls up under `unknown`.
export function rollupModels(daily, unknown = "unknown") {
  const by = {};
  for (const d of daily || []) for (const m of d.models || []) {
    const k = m.model || unknown;
    const r = by[k] || (by[k] = { model: k, requests: 0, tokens_in: 0, tokens_out: 0, spend: 0, earned: 0 });
    r.requests += n0(m.requests); r.tokens_in += n0(m.tokens_in); r.tokens_out += n0(m.tokens_out);
    r.spend += n0(m.spend); r.earned += n0(m.earned);
  }
  return Object.values(by).map((r) => ({ ...r, tokens: r.tokens_in + r.tokens_out })).sort((a, b) => b.tokens - a.tokens);
}

// The totals across the window, for the headline stats.
export function rollupTotals(daily) {
  const t = { requests: 0, tokens_in: 0, tokens_out: 0, spend: 0, earned: 0 };
  for (const b of daily || []) {
    t.requests += n0(b.requests); t.tokens_in += n0(b.tokens_in); t.tokens_out += n0(b.tokens_out);
    t.spend += n0(b.spend); t.earned += n0(b.earned);
  }
  return t;
}
