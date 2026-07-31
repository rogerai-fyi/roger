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
import { readFileSync, readdirSync } from "node:fs";
import { createHash } from "node:crypto";
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
    // EVERY claim in the window, judged individually: a negated one nearby is not a
    // licence for the next one. Skipping the window on the first negation is how a real
    // claim hides behind an honest sentence.
    for (const claim of around.matchAll(new RegExp(CLAIM, "g"))) {
      if (NEGATED.test(around.slice(0, claim.index))) continue;
      hits.push(`"${m[0]}" near "${claim[0]}": …${around.replace(/\s+/g, " ").trim()}…`);
      break;
    }
  }
  return hits;
}

// The sweep's own blind spot: a window may hold TWO claims. Taking only the first and
// skipping the whole window when it is negated lets a real claim ride along behind an
// honest sentence - which is precisely the failure mode this whole file exists to catch.
test("a negated claim does not mask a real one in the same window", () => {
  const honestOnly = "Wave Micro is not Released yet, so nothing ships today.";
  assert.deepEqual(offendingContext(honestOnly), [], "an honest denial must stay clean");

  const masked = "Wave Micro is not Released yet. Download Wave Micro v1.0 AVAILABLE now.";
  assert.notDeepEqual(
    offendingContext(masked),
    [],
    "a real claim following a negated one must still be caught",
  );
});

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
  const cards = walk(SRC, ".svg").filter((f) => /^og[-.]/.test(path.basename(f)));
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

// The rasterized card is what crawlers cache, and it cannot be grepped. A first
// draft compared mtimes - but git does not preserve them, and on a fresh checkout
// the PNG is often written first, so that check was both flaky and meaningless in
// CI. Instead each PNG carries the sha256 of the SVG it was rendered from: edit the
// source without re-rendering and the recorded hash no longer matches, which is
// exactly how the stale "AVAILABLE" card survived the first retraction pass.
test("every social card PNG was rendered from the current SVG", () => {
  const cards = walk(SRC, ".svg").filter((f) => /^og[-.]/.test(path.basename(f)));
  assert.ok(cards.length >= 2, `sweep found ${cards.length} og cards - it has gone blind`);
  for (const card of cards) {
    const png = card.replace(/\.svg$/, ".png");
    let hasPng = true;
    try {
      readFileSync(path.join(SRC, png));
    } catch {
      hasPng = false; // not every card is rasterized
    }
    if (!hasPng) continue;
    let recorded;
    try {
      recorded = readFileSync(path.join(SRC, `${png}.source-sha256`), "utf8").trim().split(/\s+/)[0];
    } catch {
      // A rendered card with no pin is UNGUARDED. Skipping it here would reproduce
      // the exact "goes blind and reports success" hole this file exists to close.
      assert.fail(
        `${png} exists but has no ${png}.source-sha256 pin, so nothing detects a stale render. ` +
          `Create it:\n  sha256sum web/src/${card} | cut -d" " -f1 > web/src/${png}.source-sha256`
      );
    }
    const actual = createHash("sha256").update(readFileSync(path.join(SRC, card))).digest("hex");
    assert.equal(
      actual,
      recorded,
      `${path.basename(png)} is stale: ${path.basename(card)} changed since it was rendered. ` +
        `Re-render and re-pin:\n` +
        `  rsvg-convert -w 1200 -h 630 web/src/${card} -o web/src/${png}\n` +
        `  sha256sum web/src/${card} | cut -d" " -f1 > web/src/${png}.source-sha256`
    );
  }
});
