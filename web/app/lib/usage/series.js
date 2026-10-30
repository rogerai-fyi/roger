// The Metrics page's time-series charts as pure geometry (legacy: buildTimeSeries in
// web/src/js/metrics.js). Hand-rolled inline SVG, no chart library: a 640 x 168 viewBox the
// CSS scales, one or two series (stacked areas for tokens in/out, an area + line + dots for a
// single value), sparse x ticks, and the crosshair the component drives.
import { n0, dayLabel } from "#lib/format.js";

export const W = 640, H = 168;
const PAD = { l: 8, r: 8, t: 12, b: 22 };

// The chartable points for a series set: a label per bucket, the values it draws.
export function points(daily, series) {
  return daily.map((b) => {
    const s = {};
    for (const d of series) s[d.key] = n0(b[d.key]);
    return { bucket: b.bucket, label: dayLabel(b.bucket), series: s };
  });
}

// Honest empty: a figure only shows when some bucket carries a nonzero value for it.
export const anyValue = (daily, series) => (daily || []).some((b) => series.some((s) => n0(b[s.key]) !== 0));

export function geometry(pts, series, stack) {
  const plotW = W - PAD.l - PAD.r, plotH = H - PAD.t - PAD.b, n = pts.length;
  // y-domain: the max over the (possibly stacked) total of all series in a bucket
  let maxY = 0;
  for (const p of pts) {
    let tot = 0, single = 0;
    for (const s of series) { const v = n0(p.series[s.key]); tot += v; if (v > single) single = v; }
    const cand = stack ? tot : single;
    if (cand > maxY) maxY = cand;
  }
  if (maxY <= 0) maxY = 1;   // a flat-zero range still draws a baseline, not a divide-by-0
  const xAt = (i) => (n <= 1 ? PAD.l + plotW / 2 : PAD.l + (i / (n - 1)) * plotW);
  const yAt = (v) => PAD.t + plotH - (v / maxY) * plotH;
  const base = PAD.t + plotH;
  const layers = [];
  const below = [];
  for (const s of series) {
    const ps = pts.map((p, i) => {
      let b = 0;
      if (stack) for (const k of below) b += n0(p.series[k]);
      const v = n0(p.series[s.key]);
      return { x: xAt(i), yTop: yAt(b + v), yBase: yAt(b) };
    });
    if (stack) {
      // the filled area between this series' running total and the one below it
      let d = "";
      ps.forEach((p, a) => { d += (a ? "L" : "M") + p.x + " " + p.yTop + " "; });
      for (let z = ps.length - 1; z >= 0; z--) d += "L" + ps[z].x + " " + ps[z].yBase + " ";
      layers.push({ cls: s.cls, area: d + "Z" });
      below.push(s.key);
    } else {
      // the area under a single line for body, the line on top, a dot at each bucket so a
      // single-point series is still visible
      let area = "", line = "";
      ps.forEach((p, a) => { area += (a ? "L" : "M") + p.x + " " + p.yTop + " "; line += (a ? "L" : "M") + p.x + " " + p.yTop + " "; });
      if (ps.length) area += "L" + ps[ps.length - 1].x + " " + base + " L" + ps[0].x + " " + base + " Z";
      layers.push({ cls: s.cls, area, line, dots: ps.map((p) => ({ cx: p.x, cy: p.yTop })), r: n <= 14 ? 2.2 : 1.6 });
    }
  }
  // sparse x-axis tick labels: first + last + a couple between
  const ticks = [];
  if (n) {
    const idx = [0];
    if (n > 1) idx.push(n - 1);
    if (n > 6) idx.push(Math.round((n - 1) / 2));
    if (n > 12) { idx.push(Math.round((n - 1) / 4)); idx.push(Math.round((n - 1) * 3 / 4)); }
    idx.sort((a, b) => a - b);
    const seen = new Set();
    for (const i of idx) {
      if (seen.has(i)) continue;
      seen.add(i);
      ticks.push({ x: xAt(i), anchor: i === 0 ? "start" : i === n - 1 ? "end" : "middle", label: pts[i].label });
    }
  }
  // the focus dot tracks the topmost series at a bucket
  const topAt = (i) => {
    if (stack) { let t = 0; for (const s of series) t += n0(pts[i].series[s.key]); return yAt(t); }
    return yAt(n0(pts[i].series[series[series.length - 1].key]));
  };
  return { viewBox: `0 0 ${W} ${H}`, n, axis: { x1: PAD.l, y1: base, x2: PAD.l + plotW, y2: base }, cursor: { y1: PAD.t, y2: base }, layers, ticks, xAt, topAt };
}

// The bucket nearest an x in viewBox units.
export function nearest(geo, rel) {
  let best = 0, bestD = Infinity;
  for (let i = 0; i < geo.n; i++) { const d = Math.abs(geo.xAt(i) - rel); if (d < bestD) { bestD = d; best = i; } }
  return best;
}
