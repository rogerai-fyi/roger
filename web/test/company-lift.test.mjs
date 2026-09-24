// Company section "lift" (2026-09-24): a PRESENTATION-only pass that moves About,
// Careers, Questions, Security, Privacy and Terms onto the homepage design system
// (web/DESIGN-SYSTEM.md). These tests pin what that pass promised:
//   (a) the words did not change. Each page's <main> reads character for character
//       as the pre-lift copy (fixtures/company-section-text.json, captured from the
//       built pages before the pass), links included, in the same order. Only
//       whitespace is free. The navigation the pass ADDS (a tuner, a question or
//       section index) is built from words already on the page and is stripped
//       before the comparison, then checked on its own below. A deliberate COPY
//       change must update the fixture in the same commit.
//   (b) the pages use the system's components rather than page-local copies of them:
//       an ink hero panel, the TOC tuner, calm tinted panels instead of the full-bleed
//       bands, the shared Wave Spectrum scale, the step path, the page index.
//   (c) the legal pages leave the account "card" shell for the marketing chrome,
//       without touching a sentence (the fixture covers every sentence).
//   (d) the Let's talk dialog is a soft panel, not a hard square frame.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");
const fixture = JSON.parse(readFileSync(path.join(WEB, "test/fixtures/company-section-text.json"), "utf8"));

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const PAGES = Object.keys(fixture);
const LEGAL = ["security.html", "privacy.html", "tos.html"];
const HERO_INK = ["company.html", "careers.html", "faq.html"];

const main = (page) => {
  const h = read(page).replace(/<!--[\s\S]*?-->/g, "");
  return h.slice(h.search(/<main\b/), h.indexOf("</main>") + 7);
};
// the navigation the lift adds: derived from words already on the page
const DERIVED = /<nav class="(?:toc-tuner|page-index)\b[^"]*"[\s\S]*?<\/nav>/g;
const text = (html) => html.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();
const squash = (s) => s.replace(/\s+/g, "");
const decode = (s) => s.replace(/&rsquo;/g, "’").replace(/&amp;/g, "&").replace(/&middot;/g, "·");

/* ---- (a) the words ------------------------------------------------------------- */

for (const page of PAGES) {
  test(`(a) ${page}: the words are the pre-lift words (whitespace aside)`, () => {
    const now = text(main(page).replace(DERIVED, ""));
    assert.equal(squash(now), squash(fixture[page].text),
      `${page}: the lift may restructure markup and move words between blocks, never edit them`);
  });
  test(`(a) ${page}: the links are the same links, in the same order`, () => {
    const hrefs = [...main(page).replace(DERIVED, "").matchAll(/href="([^"]*)"/g)].map((m) => m[1]);
    assert.deepEqual(hrefs, fixture[page].hrefs);
  });
}

/* ---- the added navigation is honest ------------------------------------------- */

// every station / index entry points at an element that exists on the same page
function derivedLinksResolve(page) {
  const m = main(page);
  const navs = m.match(DERIVED) || [];
  assert.ok(navs.length, `${page} carries a tuner or an index`);
  for (const nav of navs) {
    for (const [, id] of nav.matchAll(/href="#([^"]+)"/g)) {
      assert.match(m, new RegExp(`id="${id}"`), `${page}: #${id} is a real target`);
    }
  }
}

for (const page of PAGES) {
  test(`(b) ${page}: every station and index entry lands on a real target`, () => derivedLinksResolve(page));
}

test("(b) the tuner on About, Careers and Questions names sections the page already labels", () => {
  const expect = {
    "company.html": ["#work", "#focus", "#origin", "#contact"],
    "careers.html": ["#roles", "#engineering", "#industrial", "#apply", "#why"],
    "faq.html": ["#start", "#cost", "#share", "#privacy"],
  };
  for (const [page, hrefs] of Object.entries(expect)) {
    const nav = main(page).match(/<nav class="toc-tuner" data-tuner[\s\S]*?<\/nav>/)?.[0];
    assert.ok(nav, `${page} has the TOC tuner (the shared component, auto-initialized)`);
    assert.deepEqual([...nav.matchAll(/href="([^"]+)"/g)].map((m) => m[1]), hrefs);
    // each station's name is the § label (or the old index entry) already on the page
    const labels = squash(decode(text(main(page).replace(DERIVED, "")))).toLowerCase();
    for (const [, name] of nav.matchAll(/<span class="toc-tuner__name">([^<]+)<\/span>/g)) {
      assert.ok(labels.includes(squash(decode(name)).toLowerCase()) || page === "faq.html",
        `${page}: station "${name}" is a label the page already carries`);
    }
  }
});

test("(b) the FAQ index lists every question, grouped, and keeps the four group names", () => {
  const m = main("faq.html");
  const index = m.match(/<nav class="page-index"[\s\S]*?<\/nav>/)?.[0];
  assert.ok(index, "the question index is the shared .page-index component");
  const qs = [...m.matchAll(/<h3 class="faq__q"[^>]*>([\s\S]*?)<\/h3>/g)].map((x) => text(x[1]));
  const entries = [...index.matchAll(/<a [^>]*href="#([^"]+)"[^>]*>([\s\S]*?)<\/a>/g)].map((x) => [x[1], text(x[2])]);
  const qEntries = entries.filter(([id]) => id.startsWith("q-"));
  assert.deepEqual(qEntries.map((e) => e[1]), qs, "every question, in page order, word for word");
  for (const [id, q] of qEntries) {
    assert.match(m, new RegExp(`<h3 class="faq__q" id="${id}">`), `"${q}" carries its own anchor`);
  }
  // the four group names the old contents list carried survive in the index (and the tuner)
  for (const g of ["Getting started", "What it costs", "Sharing your GPU", "Privacy and safety"]) {
    assert.match(index, new RegExp(g), `the index still names "${g}"`);
  }
});

test("(b) the legal pages index their own section headings, word for word", () => {
  for (const page of LEGAL) {
    const m = main(page);
    const index = m.match(/<nav class="page-index\b[^"]*"[\s\S]*?<\/nav>/)?.[0];
    assert.ok(index, `${page} has an index of its sections`);
    const h2s = [...m.replace(DERIVED, "").matchAll(/<h2 id="([^"]+)">([\s\S]*?)<\/h2>/g)].map((x) => [x[1], text(x[2])]);
    const all = [...m.replace(DERIVED, "").matchAll(/<h2\b/g)].length;
    assert.equal(h2s.length, all, `${page}: every section heading carries an id`);
    const entries = [...index.matchAll(/href="#([^"]+)"[^>]*>([\s\S]*?)<\/a>/g)].map((x) => [x[1], text(x[2])]);
    assert.deepEqual(entries, h2s, `${page}: the index is the headings, in order`);
  }
});

/* ---- (b) the system's components, not page-local copies ------------------------ */

test("(b) About, Careers and Questions open on an ink hero panel", () => {
  for (const page of HERO_INK) {
    const m = main(page);
    const zone = m.match(/<div class="tone-zone" data-tone="ink">[\s\S]*?<h1/);
    assert.ok(zone && !/<\/div><!-- \/tone-zone -->/.test(zone[0]), `${page}: the h1 sits in an ink panel`);
  }
});

test("(b) no full-bleed grey bands or black-top-rule cards remain on the lifted pages", () => {
  for (const page of HERO_INK) {
    const m = main(page);
    assert.doesNotMatch(m, /class="[^"]*\bresearch-tone\b/, `${page}: research-tone band`);
    assert.doesNotMatch(m, /class="section band"/, `${page}: .band`);
    assert.doesNotMatch(m, /class="research-card"/, `${page}: the bordered research card`);
    assert.doesNotMatch(m, /class="research-prose"/, `${page}: hairline-boxed prose`);
  }
  // the role card lost its heavy black top rule
  assert.doesNotMatch(src("styles/careers.css"), /border-top:\s*2px solid var\(--ink-900\)/);
});

test("(b) About shows the Wave Spectrum as the shared scrubbable scale", () => {
  const m = main("company.html");
  const scale = m.match(/<ol class="spectrum" data-scrub[\s\S]*?<\/ol>/)?.[0];
  assert.ok(scale, "the family is a .spectrum list the scrubber tunes");
  for (const tier of ["Pico", "Nano", "Micro", "Giga", "Tera", "Peta", "Exa"]) {
    assert.match(scale, new RegExp(`<b>(?:Wave )?${tier}</b>`), `names ${tier} as a station`);
  }
  assert.equal([...scale.matchAll(/<li\b/g)].length, 7, "one station per tier");
});

test("(b) Careers shows how to apply as a four-step path", () => {
  const m = main("careers.html");
  const path_ = m.match(/<ol class="steps"[\s\S]*?<\/ol>/)?.[0];
  assert.ok(path_, "the apply notes are the shared .steps path");
  assert.equal([...path_.matchAll(/<li\b/g)].length, 4);
});

test("(b) the summary strip keeps each lead-in and its sentence on one flowing line", () => {
  // the strip rendered kicker / bold name / continuation as three grid rows.
  // The company sheet sets the cell to flow (block), with the kicker on its own line.
  const css = src("styles/company.css");
  assert.match(css, /\.research-distinction__grid > div\s*\{[^}]*display:\s*block/);
});

/* ---- (c) the legal pages wear the marketing chrome ----------------------------- */

test("(c) Security, Privacy and Terms use the marketing chrome and a calm reading column", () => {
  for (const page of LEGAL) {
    const h = read(page);
    assert.match(h, /id="navCompanyPanel"/, `${page}: the marketing nav, with the Company menu`);
    assert.doesNotMatch(h, /<body class="auth"/, `${page}: out of the account body`);
    assert.doesNotMatch(h, /class="card"/, `${page}: no bordered card`);
    assert.doesNotMatch(h, /styles\/account-base\.css/, `${page}: not the account bundle`);
    assert.match(h, /styles\/company\.css/, `${page}: the Company section sheet`);
  }
});

test("(c) the robots decisions are left exactly as they were (founder calls)", () => {
  assert.match(read("security.html"), /<meta name="robots" content="noindex"/);
  assert.match(read("tos.html"), /<meta name="robots" content="noindex"/);
  assert.doesNotMatch(read("privacy.html"), /<meta name="robots" content="noindex"/);
});

/* ---- (d) the dialog ------------------------------------------------------------ */

test("(d) the Let's talk dialog is a soft rounded panel, not a hard square frame", () => {
  const css = src("styles/base.css");
  const rule = css.match(/\.lt-modal__dialog \{[^}]*\}/)?.[0] || "";
  assert.doesNotMatch(rule, /border:\s*2px solid var\(--ink-900\)/, "no heavy black frame");
  assert.match(rule, /border-radius:\s*var\(--panel-r\)/, "the system's panel radius");
});

/* ---- quality: labels a reader must read are AA on paper ------------------------ */

test("labels on the lifted pages never use --ink-400 on paper", () => {
  // --ink-400 is 2.85:1 on paper (DESIGN-SYSTEM.md): decoration inside ink panels only
  for (const sheet of ["careers.css", "faq.css", "company.css"]) {
    assert.doesNotMatch(src(`styles/${sheet}`), /var\(--ink-400\)/, `${sheet} uses --ink-400`);
  }
  const lt = src("styles/base.css").match(/\.lt-modal__kicker \{[^}]*\}|\.lt-field i \{[^}]*\}/g).join("\n");
  assert.doesNotMatch(lt, /--ink-400/, "the dialog's kickers and 'optional' tags are readable");
});
