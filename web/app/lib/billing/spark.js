// The spend-velocity sparkline as pure geometry (legacy: renderVelocity in
// web/src/js/billing.js): the most recent 30 daily buckets, oldest first, in a 100 x 32
// viewBox the CSS scales. Honest-empty: null when no day carries spend.
import { n0 } from "#lib/format.js";

export function spark(daily) {
  if (!daily || !daily.length) return null;
  const pts = daily.slice(-30).map((d) => ({ day: d.bucket, v: n0(d.spend) }));
  const total = pts.reduce((s, p) => s + p.v, 0);
  if (total <= 0) return null;
  const peak = pts.reduce((m, p) => Math.max(m, p.v), 0);
  const max = peak || 1;
  const W = 100, H = 32, n = pts.length;
  const step = n > 1 ? W / (n - 1) : 0;
  const y = (v) => H - (v / max) * (H - 3) - 1.5;
  let line = "", area = "";
  pts.forEach((p, i) => {
    const px = (n > 1 ? i * step : W / 2).toFixed(2), py = y(p.v).toFixed(2);
    line += (i === 0 ? "M" : "L") + px + " " + py + " ";
    area += (i === 0 ? "M" : "L") + px + " " + py + " ";
  });
  // a single bucket: a flat tick, so the chart is not empty
  if (n === 1) line = "M0 " + y(pts[0].v).toFixed(2) + " L" + W + " " + y(pts[0].v).toFixed(2);
  area += "L" + W + " " + H + " L0 " + H + " Z";
  const last = pts[n - 1];
  return { viewBox: `0 0 ${W} ${H}`, n, peak, line, area,
    dot: { cx: (n > 1 ? (n - 1) * step : W / 2).toFixed(2), cy: y(last.v).toFixed(2) } };   // the live edge of the trace
}
