// The dashboard's charts as pure geometry (legacy: the DOM builders in web/src/js/dashboard.js).
// Hand-rolled SVG paths and CSS bars, no chart library; static, so reduced-motion and narrow
// safe. The component renders what these return.
import { n0, cr, dayLabel } from "#lib/format.js";

// frontierAt cycles the frontier reference table for the savings toggle: given the reference
// list (each carrying a server-computed frontier_est = what THIS account's tokens would have
// cost at that model's list price), the user's spend, and a step index, it returns the model
// to show, what it would have billed, and the savings (floored at 0: a frontier model cheaper
// than what you paid shows $0 saved, never a negative). i wraps both ways.
export function frontierAt(ref, spend, i) {
  const n = ref.length;
  const r = ref[((i % n) + n) % n];
  const frontier = n0(r.frontier_est);
  return { model: r.model, frontier, savings: Math.max(0, frontier - n0(spend)) };
}

// A line/area chart over the daily buckets: one or two series on a shared x-axis.
// series = [{ key, color, fill }]; reads point[key] per day.
export function linePlot(daily, series) {
  const W = 720, H = 200, padL = 8, padR = 8, padT = 14, padB = 8;
  const iw = W - padL - padR, ih = H - padT - padB;
  const n = daily.length;
  let max = 0;
  for (const d of daily) for (const s of series) max = Math.max(max, n0(d[s.key]));
  if (max <= 0) max = 1;
  const x = (i) => (n <= 1 ? padL + iw / 2 : padL + (i / (n - 1)) * iw);
  const y = (v) => padT + ih - (n0(v) / max) * ih;
  const grid = [0, 0.5, 1].map((f) => ({ y: padT + ih - f * ih, cls: f === 0 ? "ds-axis" : "ds-grid", x1: padL, x2: W - padR }));
  const lines = series.map((s) => {
    let d = "";
    daily.forEach((p, i) => { d += (i === 0 ? "M" : "L") + x(i).toFixed(1) + " " + y(p[s.key]).toFixed(1) + " "; });
    const area = s.fill && n > 1 ? d + "L" + x(n - 1).toFixed(1) + " " + (padT + ih) + " L" + x(0).toFixed(1) + " " + (padT + ih) + " Z" : null;
    return { d, area, color: s.color, fill: s.fill, cx: x(n - 1).toFixed(1), cy: y(daily[n - 1][s.key]).toFixed(1) };
  });
  return { viewBox: `0 0 ${W} ${H}`, grid, lines };
}

// x-axis labels under a chart: first / middle / last bucket (readable at 30 points)
export function axisLabels(daily) {
  const n = daily.length;
  const picks = n <= 1 ? [0] : [0, Math.floor((n - 1) / 2), n - 1];
  return [...new Set(picks)].map((i) => dayLabel(daily[i].bucket));
}

// Per-day savings columns (red = savings, the one glint), heights scaled to the max day
export function savingsBars(daily) {
  const max = daily.reduce((m, d) => Math.max(m, n0(d.savings_est)), 0);
  return daily.map((d) => ({ title: dayLabel(d.bucket) + ": " + cr(n0(d.savings_est)) + " saved",
    height: Math.max(0, max > 0 ? (n0(d.savings_est) / max) * 100 : 0) }));
}

// Per-model breakdown: roll the daily models[] up over the window, biggest first
export { rollupModels } from "#lib/rollup.js";
