// Broadcast 011 is an argument made entirely of arithmetic, published on a site whose
// rule is that a printed number has to be one somebody can check. So the numbers are not
// prose here: every cost row carries the inputs it was computed from, and this test
// recomputes each one and compares it to what the page prints.
//
// That is the only way an article like this stays true. Copy drifts, a card gets swapped,
// a rate is updated, and a table that was right in August quietly stops being right - with
// nothing failing, because nothing was ever checking the sums.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const PAGE = "broadcasts-what-a-million-tokens-costs.html";
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

/* ---- it exists and is wired like every other broadcast ---------------- */

test("economics: the broadcast builds, is indexable, and is in the sitemap", () => {
  assert.doesNotMatch(read(PAGE), /name=["']robots["'][^>]*noindex/i);
  assert.match(read("sitemap.xml"), new RegExp(`<loc>https://rogerai\\.fm/${PAGE}</loc>`));
});

test("economics: the transmission log carries it, newest first", () => {
  const log = read("broadcasts.html");
  assert.match(log, new RegExp(`href="/${PAGE}"`), "the index links it");
  const first = log.indexOf(`href="/${PAGE}"`);
  const prev = log.indexOf('href="/broadcasts-run-a-tower.html"');
  assert.ok(first > 0 && prev > 0 && first < prev, "it sits above the previous broadcast");
});

/* ---- THE ARITHMETIC ---------------------------------------------------- */

// The operator's share, declared once on the page and read from there rather than
// hardcoded here: if the split ever changes, the page and this test move together.
const share = () => {
  // more than one table states the share now, so they all have to agree - a page that
  // grossed one table up at 70% and another at some other number would still look fine.
  const all = [...read(PAGE).matchAll(/data-share="([0-9.]+)"/g)].map((m) => Number(m[1]));
  assert.ok(all.length >= 1, "the page declares the operator's share");
  assert.equal(new Set(all).size, 1, `the tables disagree about the operator's share: ${all.join(", ")}`);
  const s = all[0];
  assert.ok(s > 0 && s < 1, "and it is a share, not a percentage or a multiple");
  return s;
};

// A row is either an OWNED card (watts + electricity rate) or a RENTED one (an hourly
// price). Both reduce to a cost per hour.
const rows = () =>
  [...read(PAGE).matchAll(/<tr[^>]*data-cost[^>]*>/g)].map((m) => {
    const attr = (n) => {
      const v = m[0].match(new RegExp(`data-${n}="([0-9.]+)"`))?.[1];
      return v === undefined ? undefined : Number(v);
    };
    return {
      tag: m[0],
      watts: attr("watts"), rate: attr("rate"), hourly: attr("hourly"),
      tps: attr("tps"), util: attr("util"), price: attr("price"),
    };
  });

test("economics: every printed cost is the formula applied to its own inputs", () => {
  const S = share();
  const all = rows();
  assert.ok(all.length >= 9, `the cost rows carry their inputs (found ${all.length})`);

  for (const r of all) {
    const costHr = r.hourly !== undefined ? r.hourly : (r.watts / 1000) * r.rate;
    assert.ok(Number.isFinite(costHr), `a row states either an hourly price or watts + a rate: ${r.tag}`);
    assert.ok(r.tps > 0 && r.util > 0 && r.price > 0, `a row states throughput, duty and a price: ${r.tag}`);

    // break-even $/1M = (cost per hour / the operator's share) / millions of tokens served
    const expected = (costHr / S) / ((r.tps * 3600 * r.util) / 1e6);
    assert.ok(Math.abs(expected - r.price) < 0.005,
      `printed $${r.price}/1M, but ${r.tps} tok/s at ${r.util * 100}% duty on $${costHr.toFixed(4)}/hr works out to $${expected.toFixed(3)}/1M`);
  }
});

test("economics: the printed price also appears as text in its own row", () => {
  // Guards the other half: the attribute could be right while the visible cell is stale.
  const page = read(PAGE);
  for (const m of page.matchAll(/<tr[^>]*data-price="([0-9.]+)"[^>]*>([\s\S]*?)<\/tr>/g)) {
    const shown = Number(m[1]).toFixed(2);
    assert.ok(m[2].includes(shown),
      `a row computes $${shown} but does not print it: ${m[2].replace(/\s+/g, " ").slice(0, 90)}`);
  }
});

/* ---- the comparison, and where it came from ---------------------------- */

test("economics: every aggregator price is attributed and dated", () => {
  const page = read(PAGE);
  const quoted = [...page.matchAll(/data-market="([^"]+)" data-market-price="([0-9.]+)"/g)];
  assert.ok(quoted.length >= 4, `it quotes real comparison prices (found ${quoted.length})`);
  for (const [, model] of quoted) {
    assert.match(model, /\//, `"${model}" is a full model id, so a reader can check it`);
  }
  assert.match(page, /openrouter/i, "it says whose prices these are");
  assert.match(page, /2026-08-31|31 August 2026|August 2026/, "and when they were read");
});

test("economics: the assumption doing the work is stated, not buried", () => {
  // Throughput moves every number on the page. An article that prints break-even figures
  // without saying what tok/s they assume is not showing its working, it is decorating.
  const page = read(PAGE).replace(/\s+/g, " ");
  assert.match(page, /tok\/s/, "throughput is named");
  assert.match(page, /assum|depends on|the number that moves/i, "and flagged as the assumption");
  assert.match(page, /30%|70%/, "and the platform's share is stated where the money is");
});

/* ---- the shape the site expects --------------------------------------- */

test("economics: it carries a quick answer and FAQ structured data", () => {
  const page = read(PAGE);
  assert.match(page, /"@type"\s*:\s*"FAQPage"/, "FAQPage JSON-LD");
  assert.match(page, /class="bc-answer"/, "a quick answer block leads the piece");
});

test("economics: the chart is described for a reader who cannot see it", () => {
  const fig = read(PAGE).match(/<svg[^>]*class="[^"]*ec-chart[^"]*"[\s\S]*?<\/svg>/)?.[0];
  assert.ok(fig, "the comparison chart is inline SVG");
  assert.match(fig, /role="img"/, "it is announced as an image");
  // a <title> may carry an id (aria-labelledby points at it), so do not require a bare tag
  assert.match(fig, /<title[\s>]|aria-label="[^"]{40,}"/, "with a name for what it shows");
  const long = fig.match(/<desc[^>]*>([\s\S]*?)<\/desc>/)?.[1] ?? fig.match(/aria-label="([^"]*)"/)?.[1] ?? "";
  assert.ok(long.replace(/\s+/g, " ").trim().length >= 120,
    "and prose that actually reads out the comparison, not just a label");
});

test("economics: it is registered for its own stylesheet", () => {
  assert.match(src("../build.mjs"), new RegExp(`"${PAGE}"`), "the page has a CSS bundle");
  assert.match(read(PAGE), /styles\/broadcast-economics\.css/, "and links it");
});

/* ---- the two tables the first pass left unpinned ------------------------ */

// The margin table was prose-checked and wrong: it divided what an operator KEEPS (already
// net of the fee) by a break-even price that already embeds the fee, so every multiple came
// out 0.7x of the truth. The honest ratio is what you charge over what you must charge.
test("economics: the margin table is the same arithmetic, not a second opinion", () => {
  const page = read(PAGE);
  const rows = [...page.matchAll(/<tr[^>]*data-margin[^>]*>/g)].map((m) => ({
    tag: m[0],
    be: Number(m[0].match(/data-be="([0-9.]+)"/)?.[1]),
    charge: Number(m[0].match(/data-charge="([0-9.]+)"/)?.[1]),
    shown: Number(m[0].match(/data-margin="([0-9.]+)"/)?.[1]),
  }));
  assert.ok(rows.length >= 4, `the margin rows carry their inputs (found ${rows.length})`);
  for (const r of rows) {
    assert.ok(r.be > 0 && r.charge > 0 && r.shown > 0, `a margin row states both prices: ${r.tag}`);
    const expected = r.charge / r.be;
    assert.ok(Math.abs(expected - r.shown) < 0.05,
      `printed ${r.shown}x, but charging $${r.charge} against a $${r.be} break-even is ${expected.toFixed(2)}x`);
  }
});

test("economics: every margin row's break-even is one the cost tables computed", () => {
  // A margin row may not invent a break-even: it has to be a price the pinned rows produced.
  const priced = new Set(rows().map((r) => r.price.toFixed(2)));
  for (const m of read(PAGE).matchAll(/data-be="([0-9.]+)"/g)) {
    const be = Number(m[1]).toFixed(2);
    assert.ok(priced.has(be), `$${be}/1M is quoted as a break-even that no cost row computes`);
  }
});

// FIG.1 is a bar chart drawn by hand, which means the bars can silently stop agreeing with
// the axis printed underneath them. The scale is declared on the SVG and every bar is
// measured against it.
test("economics: the chart bars are drawn to the axis printed under them", () => {
  const fig = read(PAGE).match(/<svg[^>]*class="[^"]*ec-chart[^"]*"[\s\S]*?<\/svg>/)?.[0];
  const x0 = Number(fig.match(/data-x0="([0-9.]+)"/)?.[1]);
  const ppd = Number(fig.match(/data-px-per-dollar="([0-9.]+)"/)?.[1]);
  assert.ok(x0 > 0 && ppd > 0, "the chart declares its origin and its scale");

  // the declared scale has to be the one the axis labels are actually placed on
  const ticks = [...fig.matchAll(/<text class="ec-chart__axis" x="([0-9.]+)"[^>]*>\$([0-9.]+)<\/text>/g)];
  assert.ok(ticks.length >= 3, `the axis is labelled (found ${ticks.length} ticks)`);
  for (const [, x, v] of ticks) {
    assert.ok(Math.abs(Number(x) - (x0 + Number(v) * ppd)) < 0.5,
      `the $${v} tick sits at x=${x}, but the declared scale puts it at ${x0 + Number(v) * ppd}`);
  }

  const bars = [...fig.matchAll(/<rect[^>]*class="[^"]*ec-chart__bar[^"]*"[^>]*>/g)];
  assert.ok(bars.length >= 5, `the bars carry the value they draw (found ${bars.length})`);
  for (const [tag] of bars) {
    const value = Number(tag.match(/data-value="([0-9.]+)"/)?.[1]);
    const width = Number(tag.match(/\bwidth="([0-9.]+)"/)?.[1]);
    const x = Number(tag.match(/\bx="([0-9.]+)"/)?.[1]);
    assert.ok(value > 0, `a bar states its value: ${tag}`);
    assert.equal(x, x0, `every bar starts at the axis origin: ${tag}`);
    assert.ok(Math.abs(width - value * ppd) <= 0.5,
      `a $${value} bar is ${width}px wide, but the axis makes that ${(value * ppd).toFixed(1)}px`);
  }
});

// The broadcast carries FAQPage markup, and markup that promises questions the page does
// not show is exactly what Google's visible-content guidance forbids. faq.html has been
// locked to its rendered headings since it shipped; this applies the same lock here, where
// the two lists had already drifted to 5 declared against 4 visible - and not the same four.
test("economics: the FAQ markup and the rendered questions are the same list", () => {
  const html = read(PAGE);
  const visible = [...html.matchAll(/<dt><b>([\s\S]*?)<\/b><\/dt>/g)]
    .map((m) => m[1].replace(/<[^>]+>/g, "").replace(/&#39;|&rsquo;/g, "'").replace(/\s+/g, " ").trim());
  assert.ok(visible.length >= 4, `the piece renders its questions (found ${visible.length})`);

  const block = [...html.matchAll(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g)]
    .map((m) => m[1]).find((b) => /"FAQPage"/.test(b));
  assert.ok(block, "an ld+json block carrying the FAQPage");
  const declared = JSON.parse(block).mainEntity.map((q) => q.name.replace(/\s+/g, " ").trim());

  assert.deepEqual([...declared].sort(), [...visible].sort(),
    "the FAQ markup promises questions the page does not show (or hides ones it does)");
});
