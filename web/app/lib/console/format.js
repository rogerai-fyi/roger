// Console cell formatters (ported from web/src/js/console.js). Pure, so vitest covers them.

import { n0 } from "#lib/format.js";
export { n0, cr, num, kfmt, ago, shortId, rfmt } from "#lib/format.js";   // the shared ones
export function priceStr(n) {
  n = n0(n);
  return n === 0 ? "0" : n.toFixed(6).replace(/0+$/, "").replace(/\.$/, "");
}
export const fnum = (v) => { const n = parseFloat(v); return isFinite(n) && n >= 0 ? n : 0; };
export const daysStr = (arr) => (arr && arr.length ? arr.join(",") : "");
export function parseDays(s) {
  const out = [];
  for (let t of (s || "").split(",")) {
    t = t.trim(); if (t === "") continue;
    const d = parseInt(t, 10);
    if (isFinite(d) && d >= 0 && d <= 6 && !out.includes(d)) out.push(d);
  }
  return out;
}
