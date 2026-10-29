#!/usr/bin/env node
// Is the app's version of a page the same page? Build the legacy page and the app's (via
// WEB_PREVIEW), then compare what a reader or a crawler gets: the visible text, the
// headings, the links, the ids, the meta tags, the stylesheets and the scripts. Whitespace,
// comments, entity spelling and ?v= hashes are ignored. Exit 1 on any difference.
//
//   node scripts/page-parity.mjs <page.html>...
import { readFileSync, mkdtempSync, copyFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join, dirname } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { decodeText, decodeMarkup, stripComments, stripTags } from "../test/_html.mjs";

const WEB = join(dirname(fileURLToPath(import.meta.url)), "..");
const DIST = join(WEB, "dist");
const pages = process.argv.slice(2);
if (!pages.length) throw new Error("usage: page-parity.mjs <page.html>...");

const strip = (h) => decodeMarkup(stripComments(h).replace(/\?v=[0-9a-f]+/g, ""));
const text = (h) => decodeText(stripTags(strip(h).replace(/<script[\s\S]*?<\/script>/g, "").replace(/<style[\s\S]*?<\/style>/g, ""))).replace(/\s+/g, " ").trim();
const grab = (h, re) => [...strip(h).matchAll(re)].map((m) => m[1].trim());
export const SETS = {
  headings: (h) => grab(h, /<h[1-6][^>]*>([\s\S]*?)<\/h[1-6]>/g).map((s) => decodeText(s.replace(/<[^>]+>/g, "")).replace(/\s+/g, " ")),
  links: (h) => grab(h, /<a\s[^>]*href="([^"]*)"/g),
  ids: (h) => grab(h, /\sid="([^"]*)"/g),
  meta: (h) => grab(h, /(<meta\s[^>]*(?:name|property)="[^"]*"[^>]*>)/g).map((s) => s.replace(/\s+/g, " ")),
  stylesheets: (h) => grab(h, /<link rel="stylesheet" href="([^"]*)"/g),
  scripts: (h) => grab(h, /<script[^>]*src="([^"]*)"/g),
  jsonld: (h) => grab(h, /<script type="application\/ld\+json">([\s\S]*?)<\/script>/g).map((s) => s.replace(/\s+/g, "")),
};
export function compare(legacy, next) {
  const problems = [];
  const ta = text(legacy), tb = text(next);
  if (ta !== tb) {
    const wa = ta.split(" "), wb = tb.split(" ");
    let i = 0; while (i < wa.length && wa[i] === wb[i]) i++;
    problems.push(`text differs (${wa.length} vs ${wb.length} words) at word ${i}:\n    legacy: ${wa.slice(Math.max(0, i - 4), i + 8).join(" ")}\n    app:    ${wb.slice(Math.max(0, i - 4), i + 8).join(" ")}`);
  }
  for (const [k, f] of Object.entries(SETS)) {
    const A = f(legacy), B = f(next);
    if (k === "links" || k === "scripts" || k === "stylesheets" ? JSON.stringify(A) !== JSON.stringify(B) : JSON.stringify([...A].sort()) !== JSON.stringify([...B].sort())) {
      const onlyA = A.filter((x) => !B.includes(x)), onlyB = B.filter((x) => !A.includes(x));
      problems.push(`${k}: legacy ${A.length}, app ${B.length}${onlyA.length ? `; only legacy: ${JSON.stringify(onlyA.slice(0, 6))}` : ""}${onlyB.length ? `; only app: ${JSON.stringify(onlyB.slice(0, 6))}` : ""}${!onlyA.length && !onlyB.length ? " (same set, different order)" : ""}`);
    }
  }
  return problems;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const keep = mkdtempSync(join(tmpdir(), "parity-"));
  execFileSync("node", ["build.mjs"], { cwd: WEB, stdio: "inherit" });
  for (const p of pages) copyFileSync(join(DIST, p), join(keep, p));
  execFileSync("node", ["build.mjs"], { cwd: WEB, stdio: "inherit", env: { ...process.env, WEB_PREVIEW: pages.join(",") } });
  let bad = 0;
  for (const p of pages) {
    const problems = compare(readFileSync(join(keep, p), "utf8"), readFileSync(join(DIST, p), "utf8"));
    console.log(`${problems.length ? "DIFF" : "same"}  ${p}${problems.length ? "\n  " + problems.join("\n  ") : ""}`);
    bad += problems.length;
  }
  process.exit(bad ? 1 : 0);
}
