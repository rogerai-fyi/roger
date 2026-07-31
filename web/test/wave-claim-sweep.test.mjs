// A SWEEP, not an enumeration.
//
// The Wave retraction was written page by page - index, research, research-models,
// company - and shipped with the suite green while three surfaces still carried the
// claim: research-wave-family.html said the status was "Released", og-research.svg
// still read "v1.0 · 350M · AVAILABLE", and the rasterized og-research.png (the card
// crawlers actually cache) rendered that same line.
//
// An enumerated allow-list can only ever assert about pages someone remembered to
// add. This walks EVERY built page and every social card source instead, so a new
// page inherits the guard by existing rather than by being remembered.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const SRC = path.join(WEB, "src");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

function walk(dir, ext, prefix = "") {
  const out = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const rel = prefix ? `${prefix}/${e.name}` : e.name;
    if (e.isDirectory()) out.push(...walk(path.join(dir, e.name), ext, rel));
    else if (e.name.endsWith(ext)) out.push(rel);
  }
  return out;
}

// The rungs of the Wave ladder. None has a public checkpoint.
const RUNGS = /Wave\s+(?:Micro|Nano|Core)/gi;
// Claims that would assert one exists. Deliberately CASE-SENSITIVE and matched only
// NEAR a rung. Lowercase prose is how the truth is written - "no checkpoint released",
// "Live data loads when JavaScript is available" - while an actual claim is a status
// badge (AVAILABLE), a status cell (Released) or a version (v1.0). A case-insensitive
// match flagged this release's own changelog entry for saying the honest thing.
const CLAIM = /\bAVAILABLE\b|\bReleased\b|\bv1\.0\b/;
// ...and never count one that is being DENIED.
const NEGATED = /\b(no|not|never|without)\b[^.]{0,60}$/i;
const WINDOW = 160;

function offendingContext(body) {
  const hits = [];
  for (const m of body.matchAll(RUNGS)) {
    const around = body.slice(Math.max(0, m.index - WINDOW), m.index + WINDOW);
    const claim = around.match(CLAIM);
    if (claim && NEGATED.test(around.slice(0, claim.index))) continue;
    if (claim) hits.push(`"${m[0]}" near "${claim[0]}": …${around.replace(/\s+/g, " ").trim()}…`);
  }
  return hits;
}

test("no built page claims a released Wave checkpoint", () => {
  const pages = walk(DIST, ".html");
  assert.ok(pages.length >= 10, `sweep found ${pages.length} pages - it has gone blind`);
  const failures = [];
  for (const page of pages) {
    const body = readFileSync(path.join(DIST, page), "utf8").replace(/<!--[\s\S]*?-->/g, "");
    for (const hit of offendingContext(body)) failures.push(`${page}: ${hit}`);
  }
  assert.deepEqual(failures, [], `pages claiming an unreleased Wave artifact:\n${failures.join("\n")}`);
});

test("no social card source claims a released Wave checkpoint", () => {
  const cards = walk(SRC, ".svg").filter((f) => path.basename(f).startsWith("og-"));
  assert.ok(cards.length >= 2, `sweep found ${cards.length} og cards - it has gone blind`);
  const failures = [];
  for (const card of cards) {
    const body = readFileSync(path.join(SRC, card), "utf8");
    for (const hit of offendingContext(body)) failures.push(`${card}: ${hit}`);
  }
  assert.deepEqual(failures, [], `social cards claiming an unreleased artifact:\n${failures.join("\n")}`);
});

test("the fabricated artifact id appears nowhere", () => {
  const failures = [];
  for (const page of walk(DIST, ".html")) {
    if (/wave-micro-350m-instruct/i.test(readFileSync(path.join(DIST, page), "utf8"))) failures.push(page);
  }
  for (const card of walk(SRC, ".svg")) {
    if (/wave-micro-350m-instruct/i.test(readFileSync(path.join(SRC, card), "utf8"))) failures.push(card);
  }
  assert.deepEqual(failures, [], `the unpublished artifact id survives in:\n${failures.join("\n")}`);
});

// The rasterized card is what crawlers cache, and it cannot be grepped. Pin its bytes
// to the corrected source so an SVG edit without a regenerate is caught: a PNG older
// than its SVG is exactly how the stale "AVAILABLE" card survived the first pass.
test("every social card PNG is newer than the SVG it is rendered from", () => {
  for (const card of walk(SRC, ".svg").filter((f) => path.basename(f).startsWith("og-"))) {
    const png = path.join(SRC, card.replace(/\.svg$/, ".png"));
    let pngStat;
    try {
      pngStat = statSync(png);
    } catch {
      continue; // not every card is rasterized
    }
    const svgStat = statSync(path.join(SRC, card));
    assert.ok(
      pngStat.mtimeMs >= svgStat.mtimeMs,
      `${path.basename(png)} is older than ${path.basename(card)} - regenerate it ` +
        `(rsvg-convert -w 1200 -h 630 src/${card} -o src/${card.replace(/\.svg$/, ".png")})`
    );
  }
});
