import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const DIST = path.join(WEB, "dist");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

function walk(dir, exts) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = path.join(dir, name);
    if (statSync(p).isDirectory()) out.push(...walk(p, exts));
    else if (exts.has(path.extname(p))) out.push(p);
  }
  return out;
}

// Founder rule, 2026-08-23: no em dashes site-wide. The site's separator is a spaced
// hyphen ( - ), which is what every page already used apart from the stragglers this
// replaced.
//
// This generalizes the per-page checks in app-page, research-page and lets-talk, which
// are deliberately left in place: they are cheap, and a page-specific failure names the
// page faster than a tree-wide list does. The reason a tree-wide rule was needed is that
// three pages were locked and the other forty were not, so index.html and manual.html
// accumulated em dashes for months without anything failing.
//
// BOTH forms are banned. `&mdash;` renders as an em dash just as surely as the literal
// character, and only checking one is how a rule like this quietly stops working.
const EM = /—|&mdash;/;

test("no em dash anywhere in the site source", () => {
  const files = walk(SRC, new Set([".html", ".css", ".js", ".md", ".json", ".svg", ".txt"]));
  const bad = [];
  for (const f of files) {
    const body = readFileSync(f, "utf8");
    if (!EM.test(body)) continue;
    // report the first offending line so the failure is actionable
    const line = body.split("\n").findIndex((l) => EM.test(l)) + 1;
    bad.push(`${path.relative(WEB, f)}:${line}`);
  }
  assert.deepEqual(bad, [], `em dashes found (use a spaced hyphen " - " instead):\n  ${bad.join("\n  ")}`);
});

// The built output is what people actually receive, and it includes files that are
// copied rather than templated - the .md notes under assets/ ship publicly and were the
// last place em dashes survived, precisely because nobody thinks of them as "the site".
test("no em dash anywhere in the built site", () => {
  const files = walk(DIST, new Set([".html", ".css", ".js", ".md", ".txt"]));
  const bad = [];
  for (const f of files) {
    const body = readFileSync(f, "utf8");
    if (EM.test(body)) bad.push(path.relative(WEB, f));
  }
  assert.deepEqual(bad, [], `em dashes in built output:\n  ${bad.join("\n  ")}`);
});
