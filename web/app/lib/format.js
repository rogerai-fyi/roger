// Formatting the account pages share (legacy: the cr()/num()/kfmt()/ago() copies in
// console.js, dashboard.js, metrics.js, account.js, stations.js). Money and counts go
// through RogerFmt, the one renderer the CLI and TUI also use.
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
// "2026-06-01" -> "Jun 1"
export function dayLabel(b) {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(b || "");
  if (!m) return b || "";
  return new Date(Date.UTC(+m[1], +m[2] - 1, +m[3])).toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" });
}
// A growing stat tile: compact text, exact value on hover/tap (RogerFmt.bind). Svelte action;
// the parameter is the value, or { value, usd } for money.
export function rfmt(node, param) {
  const apply = (p) => (p && typeof p === "object" ? RogerFmt.bind(node, p.value, { usd: !!p.usd }) : RogerFmt.bind(node, p));
  apply(param);
  return { update: apply };
}
