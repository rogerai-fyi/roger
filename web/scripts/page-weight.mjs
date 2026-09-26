#!/usr/bin/env node
// First-load weight of each built page, and its budget (test/perf-guards.test.mjs).
//
// "First load" is what a page asks for before anyone scrolls or presses play, in raw
// (uncompressed) bytes: the page, its stylesheets and scripts, preloads, every image that
// is not lazy (its largest srcset candidate), video posters, and an autoplaying film.
// Fonts a stylesheet pulls in beyond the preloads, and text compression, are left out, so
// it is a stable number to budget against rather than a transfer size.
//
//     node web/scripts/page-weight.mjs            # print every page, heaviest first
//     node web/scripts/page-weight.mjs --budget   # rewrite the budgets: weight + 10%
//
// Raise a budget only on purpose, in the commit that adds the weight.

import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
export const DIST = path.join(WEB, "dist");
export const BUDGET_FILE = path.join(WEB, "test", "fixtures", "page-weight-budget.json");
export const HEADROOM = 1.1;

export const noComments = (h) => h.replace(/<!--[\s\S]*?-->/g, "");
export const attr = (tag, name) => (tag.match(new RegExp(`\\s${name}="([^"]*)"`)) || [])[1];
export const local = (u) => (u && !/^(https?:|data:|\/\/)/.test(u) ? u.replace(/^\//, "").split("?")[0] : null);
export const bytes = (rel) => { try { return statSync(path.join(DIST, rel)).size; } catch { return 0; } };
export const candidates = (tag) => [local(attr(tag, "src")),
  ...(attr(tag, "srcset") || "").split(",").map((c) => local(c.trim().split(/\s+/)[0]))].filter(Boolean);

// <img> tags outside <video> (an <img> inside <video> is fallback content a modern browser
// never renders or fetches), each with its offset in the page. An <img> right after a
// </video> is that film's still stand-in (reduced motion), hidden until needed.
export function images(html) {
  const orig = noComments(html);
  const body = orig.replace(/<video\b[\s\S]*?<\/video>/g, (m) => " ".repeat(m.length));
  return [...body.matchAll(/<img\b[^>]*>/g)].map((m) => ({ tag: m[0], at: m.index,
    standin: /<\/video>\s*$/.test(orig.slice(0, m.index)) }));
}

export function firstLoad(name) {
  const html = noComments(readFileSync(path.join(DIST, name), "utf8"));
  const refs = new Set([name]);
  for (const m of html.matchAll(/<link\b[^>]*>/g))
    if (/rel="(stylesheet|preload)"/.test(m[0])) refs.add(local(attr(m[0], "href")));
  for (const m of html.matchAll(/<script\b[^>]*\bsrc="([^"]+)"/g)) refs.add(local(m[1]));
  for (const { tag } of images(html)) if (attr(tag, "loading") !== "lazy")
    refs.add(candidates(tag).sort((a, b) => bytes(b) - bytes(a))[0]);
  for (const m of html.matchAll(/<video\b[^>]*>[\s\S]*?<\/video>/g)) {
    const open = m[0].match(/<video\b[^>]*>/)[0];
    refs.add(local(attr(open, "poster")));
    if (/\sautoplay[\s>]/.test(open) && attr(open, "preload") !== "none")
      for (const s of m[0].matchAll(/<source\b[^>]*src="([^"]+)"/g)) { refs.add(local(s[1])); break; }
  }
  refs.delete(null); refs.delete(undefined);
  return [...refs].reduce((n, r) => n + bytes(r), 0);
}

function main() {
  execFileSync("node", ["build.mjs"], { cwd: WEB });
  const pages = readdirSync(DIST).filter((f) => f.endsWith(".html")).sort();
  const w = Object.fromEntries(pages.map((p) => [p, firstLoad(p)]));
  for (const [p, n] of Object.entries(w).sort((a, b) => b[1] - a[1])) console.log(`${String(Math.round(n / 1024)).padStart(6)} KB  ${p}`);
  if (process.argv.includes("--budget")) {
    const pagesBudget = Object.fromEntries(pages.map((p) => [p, Math.ceil((w[p] * HEADROOM) / 1024) * 1024]));
    writeFileSync(BUDGET_FILE, JSON.stringify({
      note: "First-load weight budget per page, raw bytes (scripts/page-weight.mjs): the weight after the 2026-09 performance pass + 10%. Raise one only on purpose, in the commit that adds the weight.",
      pages: pagesBudget }, null, 1) + "\n");
    console.log(`wrote ${path.relative(WEB, BUDGET_FILE)}`);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
