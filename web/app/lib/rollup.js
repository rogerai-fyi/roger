// Rolling the broker's daily buckets up over a window (legacy: the rollupModels/rollupTotals
// copies in web/src/js/dashboard.js and metrics.js). One implementation for every page that
// reads /metrics/series.
import { n0 } from "#lib/format.js";

// Per-model breakdown: each model's bucket slices summed across daily[], one row per model,
// biggest by tokens first (or first seen first, for a page that sorts its own way). A slice
// without a model name rolls up under `unknown`.
export function rollupModels(daily, unknown = "unknown", sorted = true) {
  const by = {};
  for (const d of daily || []) for (const m of d.models || []) {
    const k = m.model || unknown;
    const r = by[k] || (by[k] = { model: k, requests: 0, tokens_in: 0, tokens_out: 0, spend: 0, earned: 0 });
    r.requests += n0(m.requests); r.tokens_in += n0(m.tokens_in); r.tokens_out += n0(m.tokens_out);
    r.spend += n0(m.spend); r.earned += n0(m.earned);
  }
  const rows = Object.values(by).map((r) => ({ ...r, tokens: r.tokens_in + r.tokens_out }));
  return sorted ? rows.sort((a, b) => b.tokens - a.tokens) : rows;
}

// Whether a /metrics/series body has the shape the rollups read: daily (when present) a list of
// bucket objects, each models (when present) a list of objects. A page checks this on arrival
// and shows its error state otherwise, as the legacy pages did when their render threw into the
// fetch chain's catch (a $derived that throws cannot be caught by the page's <svelte:boundary>).
export function seriesOK(d) {
  if (!d || typeof d !== "object") return false;
  if (d.daily == null) return true;
  return Array.isArray(d.daily) && d.daily.every((b) => b && typeof b === "object" &&
    (b.models == null || (Array.isArray(b.models) && b.models.every((m) => m && typeof m === "object"))));
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
