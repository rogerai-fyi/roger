// Console cell formatters (ported from web/src/js/console.js). Pure, so vitest covers them.
import RogerFmt from "#lib/fmt.js";

export const n0 = (v) => (typeof v === "number" && isFinite(v) ? v : 0);
export const cr = (n) => (typeof n === "number" && isFinite(n) ? RogerFmt.usdSigned(n) : "-");
export const num = (n) => (typeof n === "number" && isFinite(n) ? Math.round(n).toLocaleString("en-US") : "-");
export const kfmt = (n) => RogerFmt.count(n0(n));
export function ago(ts, now = Date.now()) {
  const s = Math.max(0, Math.floor(now / 1000 - n0(ts)));
  if (s < 60) return s + "s";
  if (s < 3600) return Math.floor(s / 60) + "m";
  if (s < 86400) return Math.floor(s / 3600) + "h";
  if (s < 7 * 86400) return Math.floor(s / 86400) + "d";
  return new Date(n0(ts) * 1000).toLocaleDateString("en-US", { month: "short", day: "numeric" });
}
export const shortId = (id) => (!id ? "-" : id.length > 12 ? id.slice(0, 12) : id);
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
// A growing number: compact text, exact value on hover/tap (RogerFmt.bind). Svelte action.
export function rfmt(node, value) {
  RogerFmt.bind(node, value);
  return { update(v) { RogerFmt.bind(node, v); } };
}
