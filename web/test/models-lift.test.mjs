// Models-menu lift (2026-09-24): a PRESENTATION-only pass that moves the pages under the
// "Models" menu (Pricing first, then Models, Voices, Tower, Integrations, Playbox) onto the
// homepage design system (web/DESIGN-SYSTEM.md). The promises it made:
//   (a) the words did not change: each page's <main> text and its links, in order, are the
//       pre-lift copy captured into fixtures/models-menu-text.json (whitespace aside). The
//       in-page TOC tuner is left out of the comparison: its station names repeat the
//       section labels that are already on the page. A deliberate COPY change must update
//       the fixture in the same commit, with founder approval.
//   (b) the page uses the shared components (tuner, panels, ink panel, copy tick, range
//       twin, scrubber) by markup, never a page-local fork.
//   (c) the boxed notebook look is gone where the page was lifted: no 2px ink rules, no
//       bordered card grids; and nothing that must be read uses --ink-400 on paper.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");
const dist = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const fixture = JSON.parse(readFileSync(path.join(WEB, "test/fixtures/models-menu-text.json"), "utf8"));
const stripCss = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const mainOf = (html) => {
  const s = html.replace(/<!--[\s\S]*?-->/g, "");
  const m = s.match(/<main\b[\s\S]*<\/main>/)?.[0] || "";
  return m.replace(/<nav class="toc-tuner"[\s\S]*?<\/nav>/g, "").replace(/<(script|style)\b[\s\S]*?<\/\1>/g, "");
};
const sectionOf = (html, id) => html.match(new RegExp(`<section[^>]*id="${id}"[\\s\\S]*?</section>`))?.[0] || "";

/* ---- (a) words and links unchanged, every page in the menu ------------------------ */

for (const page of Object.keys(fixture)) {
  test(`(a) ${page}: the visible words are the pre-lift copy, in the same order`, () => {
    const now = mainOf(src(page)).replace(/<[^>]+>/g, " ").replace(/\s+/g, "");
    assert.equal(now, fixture[page].text, `${page}: the lift may move words between elements, never edit them`);
  });
  test(`(a) ${page}: the links are the same links, in the same order`, () => {
    const hrefs = [...mainOf(src(page)).matchAll(/href="([^"]*)"/g)].map((m) => m[1]);
    assert.deepEqual(hrefs, fixture[page].hrefs);
  });
}

/* ---- the TOC tuner: stations are the page's own section labels -------------------- */

function tunerOf(page) {
  const html = src(page);
  const nav = html.match(/<nav class="toc-tuner" data-tuner aria-label="[^"]+">[\s\S]*?<\/nav>/)?.[0];
  assert.ok(nav, `${page} has a TOC tuner`);
  const st = [...nav.matchAll(/<a class="toc-tuner__st" href="#([\w-]+)"><b>([^<]+)<\/b><span class="toc-tuner__name">([^<]+)<\/span><\/a>/g)]
    .map((m) => ({ id: m[1], num: m[2], name: m[3] }));
  assert.ok(st.length >= 2 && st.length <= 12, `${page}: 2 to 12 stations (${st.length})`);
  assert.match(nav, /<div class="wrap toc-tuner__band">/, "the band carries .wrap");
  assert.match(nav, /<span class="toc-tuner__needle" aria-hidden="true"><\/span>/);
  // every station lands on a section of the page, named by that section's own label
  let last = -1;
  for (const s of st) {
    const at = html.indexOf(`id="${s.id}"`);
    assert.ok(at > html.indexOf(nav), `${page}: #${s.id} is below the tuner`);
    assert.ok(at > last, `${page}: stations run in page order (#${s.id})`);
    last = at;
    const label = html.slice(at).match(/<span class="sectionno">([^<]*)<\/span>/)?.[1] || "";
    assert.equal(label, `${s.num} / ${s.name}`, `${page}: station #${s.id} reads the section's own label`);
  }
  return st;
}

/* ---- PRICING --------------------------------------------------------------------- */

test("pricing: a TOC tuner under the hero, one station per numbered section", () => {
  const st = tunerOf("pricing.html");
  assert.deepEqual(st.map((s) => s.id), ["why", "paths", "pay", "safety", "earn", "calc", "free", "ask"]);
  const html = src("pricing.html");
  assert.ok(html.indexOf('class="toc-tuner"') > html.indexOf("</section>"), "after the hero, never above it");
});

test("pricing: the hero offers one primary action and quiet links, never a stack of outlines", () => {
  const hero = src("pricing.html").match(/<section class="research-hero">[\s\S]*?<\/section>/)[0];
  const row = hero.match(/<div class="research-actions research-actions--lead">[\s\S]*?<\/div>/)?.[0] || "";
  assert.ok(row, "the hero row is a lead row");
  assert.equal((row.match(/research-button--primary/g) || []).length, 1);
  assert.equal((row.match(/class="research-button research-button--quiet"/g) || []).length, 2);
  const close = sectionOf(src("pricing.html"), "ask");
  assert.match(close, /<div class="research-actions research-actions--lead">/, "the closing row too");
});

test("pricing: the calculators are the page's one ink panel", () => {
  const html = src("pricing.html");
  const zones = [...html.matchAll(/<div class="tone-zone" data-tone="ink">([\s\S]*?)<!-- \/tone-zone -->/g)].map((m) => m[1]);
  assert.equal(zones.length, 1, "one ink panel on the page");
  assert.match(zones[0], /^\s*(<!--[\s\S]*?-->\s*)*<section class="section" id="calc"/, "it holds the calculator section");
});

test("pricing: the cards and plates are tinted panels, not boxed grids", () => {
  const html = src("pricing.html");
  assert.equal((sectionOf(html, "paths").match(/<article class="tint-panel">/g) || []).length, 6, "six path panels");
  assert.equal((sectionOf(html, "safety").match(/<article class="tint-panel">/g) || []).length, 4, "four guard panels");
  assert.equal((html.match(/<dl class="pplate tint-panel"[ >]/g) || []).length, 2, "both rule plates sit on a panel");
  const css = stripCss(src("styles/pricing.css"));
  assert.doesNotMatch(css, /2px solid var\(--ink-900\)/, "no 2px ink rules left on the pricing sheet");
  assert.doesNotMatch(css, /gap:\s*1px;\s*background:\s*var\(--hairline\)/, "no divider-by-gap box grids");
});

test("pricing: labels that must be read are AA on paper (--ink-500, never --ink-400)", () => {
  const css = stripCss(src("styles/pricing.css"));
  for (const sel of [".pplate dt", ".whyrows__k", ".whyfine"]) {
    const rule = css.match(new RegExp(`${sel.replace(/[.]/g, "\\.")}\\s*\\{([^}]*)\\}`))?.[1] || "";
    assert.ok(rule, `${sel} is styled`);
    assert.doesNotMatch(rule, /--ink-400/, `${sel} uses an AA ink on paper`);
  }
});

test("pricing: the payout lifecycle is a scrubbable scale over its four stages", () => {
  const earn = sectionOf(src("pricing.html"), "earn");
  const ol = earn.match(/<ol class="lifecycle" data-scrub aria-label="[^"]+">([\s\S]*?)<\/ol>/)?.[1];
  assert.ok(ol, "the lifecycle is an <ol data-scrub> with a name");
  assert.deepEqual([...ol.matchAll(/<li><b>([^<]+)<\/b>/g)].map((m) => m[1]), ["Accrued", "Held", "Payable", "Paid"]);
});

test("pricing: the operator's hours and busy inputs get a range twin; typed inputs stay", () => {
  const calc = sectionOf(src("pricing.html"), "calc");
  for (const id of ["opHours", "opBusy"]) {
    assert.match(calc, new RegExp(`<input type="number" id="${id}"[^>]*data-range-twin`), `#${id} has a range twin`);
  }
  assert.doesNotMatch(calc, /id="cnHere"[^>]*data-range-twin/, "the live band rate never gets a slider (it ships empty)");
});

test("pricing: the two commands in the plates copy with the shared tick", () => {
  const html = src("pricing.html");
  for (const cmd of ["roger topup 25", "roger share --curated"]) {
    assert.match(html, new RegExp(`<button class="copy-code" type="button" data-copy-target aria-label="Copy ${cmd}"><code>${cmd}</code></button>`), cmd);
  }
});

test("pricing: sections land under the nav and reveal through the shared lift", () => {
  const html = src("pricing.html");
  assert.match(html, /<main id="top" data-lift>/);
  assert.ok((html.match(/class="section__head section__head--left" data-reveal/g) || []).length >= 6);
  assert.match(stripCss(src("styles/pricing.css")), /section\[id\][^{]*\{[^}]*scroll-margin-top/);
});

/* ---- the components the rollout added (components.css + modules) ------------------ */

test("system: the new components are defined once, in components.css, with a behaviour module", () => {
  const css = stripCss(src("styles/components.css"));
  for (const sel of [".tint-panel", ".research-actions--lead", ".research-button--quiet", ".copy-code", ".range-twin"]) {
    assert.ok(css.includes(sel), `components.css defines ${sel}`);
  }
  const js = src("js/range-twin.js");
  assert.match(js, /querySelectorAll\("\[data-range-twin\]"\)/, "range-twin.js initializes from its data- hook");
  assert.match(src("_partials/site-js.html"), /js\/range-twin\.js/, "it ships with the page runtime");
  assert.match(dist("pricing.html"), /<script src="js\/range-twin\.js\?v=/);
  const doc = readFileSync(path.join(WEB, "DESIGN-SYSTEM.md"), "utf8");
  for (const name of ["### Tinted panel", "### Range twin", "copy-code", "research-actions--lead"]) {
    assert.ok(doc.includes(name), `DESIGN-SYSTEM.md documents ${name}`);
  }
});

test("system: every new motion is opt-in or switched off under reduced motion", () => {
  const css = stripCss(src("styles/components.css"));
  const block = css.slice(css.indexOf(".tint-panel"));
  assert.doesNotMatch(block.replace(/@media \(prefers-reduced-motion: no-preference\)\s*\{[\s\S]*?\n\}/g, ""), /animation:\s*(?!none)[\w-]+\s+[\d.]+m?s/, "animations live under no-preference");
});

/* ---- MODELS ---------------------------------------------------------------------- */

test("models: the hero offers one primary action and quiet links to the rest of the menu", () => {
  const html = src("models.html");
  const row = html.match(/<div class="research-actions research-actions--lead"[^>]*>[\s\S]*?<\/div>/)?.[0] || "";
  assert.ok(row, "the hero row is a lead row");
  assert.equal((row.match(/research-button--primary/g) || []).length, 1);
  assert.equal((row.match(/class="research-button research-button--quiet"/g) || []).length, 4);
});

test("models: the live directory sits in an ink panel, like the homepage's band", () => {
  const html = src("models.html");
  const zone = html.match(/<div class="tone-zone" data-tone="ink">([\s\S]*?)<!-- \/tone-zone -->/)?.[1] || "";
  assert.match(zone, /<section class="section bands-dir" id="directory">/);
  const css = stripCss(src("styles/models.css"));
  assert.doesNotMatch(css, /border-top:\s*2px solid var\(--ink-900\)/, "no 2px ink rules on the dial or the directory");
});

test("models: a curated-only band's on-air cell shows its curated count, never a bare 0", () => {
  const js = src("js/bands.js");
  const stn = js.match(/var stn = [\s\S]*?;\n/)?.[0] || "";
  assert.match(stn, /b\.curated/, "the on-air cell knows about curated stations");
  assert.match(stn, /&raquo; /, "and marks them with the curated sign");
});

test("models: the band the dial locks is marked in the directory with the red needle", () => {
  const js = src("js/bands.js");
  assert.match(js, /classList\.toggle\("is-tuned", [^)]*\)/, "bands.js marks the tuned row");
  assert.match(stripCss(src("styles/models.css")), /\.band-row\.is-tuned\s*\{[^}]*var\(--live\)/, "styled with the one red");
});

/* ---- VOICES ---------------------------------------------------------------------- */

test("voices: the roster and how to speak are one ink panel", () => {
  const zone = src("voices.html").match(/<div class="tone-zone" data-tone="ink">([\s\S]*?)<!-- \/tone-zone -->/)?.[1] || "";
  assert.match(zone, /<section class="section bands-dir" id="directory">/);
  assert.match(zone, /class="voice-tune"/, "the command sits in the same panel");
  const css = stripCss(src("styles/voices.css"));
  assert.doesNotMatch(css, /border-top:\s*2px solid var\(--ink-900\)/, "no 2px ink rule on the roster");
});

test("voices: roster rows never start invisible, and their entrance is opt-in motion", () => {
  const css = stripCss(src("styles/voices.css"));
  const row = css.match(/\.voice-row\s*\{([^}]*)\}/)?.[1] || "";
  assert.doesNotMatch(row, /opacity:\s*0/, "a row is readable from its first frame");
  assert.doesNotMatch(css.match(/@keyframes voiceIn\s*\{[^}]*\}\s*\}?/)?.[0] || "", /opacity/, "the entrance moves, it does not fade");
});

/* ---- TOWER ----------------------------------------------------------------------- */

test("tower: a TOC tuner over its five sections", () => {
  const st = tunerOf("tower.html");
  assert.deepEqual(st.map((s) => s.id), ["path", "patch", "tape", "run", "back"]);
});

test("tower: the signal path and the patch are one ink panel; the tape is a rounded plate", () => {
  const html = src("tower.html");
  const zones = [...html.matchAll(/<div class="tone-zone" data-tone="ink">([\s\S]*?)<!-- \/tone-zone -->/g)].map((m) => m[1]);
  assert.equal(zones.length, 1);
  assert.match(zones[0], /id="path"[\s\S]*id="patch"/);
  assert.doesNotMatch(zones[0], /id="tape"/);
  const css = stripCss(src("styles/tower.css"));
  assert.doesNotMatch(css, /2px solid var\(--ink-900\)/, "no 2px ink rules");
  assert.doesNotMatch(css, /var\(--s-7\)/, "no spacing token that does not exist");
  assert.doesNotMatch(css, /\.signal__note\s*\{[^}]*--ink-300/, "no text in decoration ink");
  for (const sel of [".tape__id", ".tape__hash", ".tower__head", ".run__k", ".tape li[data-void] .tape__tok"]) {
    const rule = css.match(new RegExp(`${sel.replace(/[.[\]]/g, "\\$&")}\\s*\\{([^}]*)\\}`))?.[1] || "";
    assert.ok(rule, `${sel} is styled`);
    assert.doesNotMatch(rule, /--ink-(400|300)/, `${sel} is read on paper: AA ink`);
  }
});

test("tower: no inline styles; every action row leads with one primary", () => {
  const main = mainOf(src("tower.html"));
  assert.doesNotMatch(main, /style="/);
  const rows = [...src("tower.html").matchAll(/<div class="research-actions([^"]*)">([\s\S]*?)<\/div>/g)];
  assert.equal(rows.length, 3);
  for (const [, mod, row] of rows) {
    assert.equal(mod, " research-actions--lead");
    assert.equal((row.match(/research-button--primary/g) || []).length, 1);
    assert.doesNotMatch(row, /class="research-button"/, "the rest are quiet links");
  }
});

/* ---- INTEGRATIONS ---------------------------------------------------------------- */

test("integrations: a TOC tuner over its five sections", () => {
  const st = tunerOf("integrations.html");
  assert.deepEqual(st.map((s) => s.id), ["oneline", "serve", "desk", "anything", "ask"]);
});

test("integrations: the one-line migration is the ink panel, a diff in ink and red, no inline style", () => {
  const html = src("integrations.html");
  const zones = [...html.matchAll(/<div class="tone-zone" data-tone="ink">([\s\S]*?)<!-- \/tone-zone -->/g)].map((m) => m[1]);
  assert.equal(zones.length, 1);
  assert.match(zones[0], /<section class="section" id="oneline">/);
  assert.match(zones[0], /<pre class="mono intg-diff"><code><span class="intg-diff__del">- base_url/);
  assert.match(zones[0], /<span class="intg-diff__add">\+ base_url/);
  assert.doesNotMatch(mainOf(html), /style="/);
});

test("integrations: each guest's install line copies with the shared tick", () => {
  const html = src("integrations.html");
  const n = (html.match(/data-guest="/g) || []).length;
  const btns = [...html.matchAll(/<button class="copy-code copy-code--block" type="button" data-copy-target aria-label="Copy ([^"]+)"><code class="guests__install">([^<]+)<\/code><\/button>/g)];
  assert.equal(btns.length, n, "one per guest");
  for (const [, label, cmd] of btns) assert.equal(label, cmd, "the label names the command it copies");
});

test("integrations: panels, not 2px rules; AA inks on paper", () => {
  const css = stripCss(src("styles/integrations.css"));
  assert.doesNotMatch(css, /2px solid var\(--ink-900\)/);
  // (the table's header row is the shared .data-table: contrast.test holds every ink AA)
  for (const sel of [".guests__meta", ".ways__fine"]) {
    const rule = css.match(new RegExp(`${sel.replace(".", "\\.")}\\s*\\{([^}]*)\\}`))?.[1] || "";
    assert.ok(rule, sel);
    assert.doesNotMatch(rule, /--ink-400/, `${sel}: AA ink on paper`);
  }
  const html = src("integrations.html");
  assert.equal((html.match(/<div class="research-actions research-actions--lead">/g) || []).length, 2);
});

test("contrast: the pricing cost chips and the dial's meter keys are AA at rest", () => {
  assert.match(stripCss(src("styles/pricing.css")), /\.paths__cost\s*\{[^}]*background:\s*var\(--paper\)/, "the red chip sits on paper, not the tinted panel");
  assert.doesNotMatch(stripCss(src("styles/models.css")), /\.dial__chip \.meter__k\s*\{[^}]*--ink-400/);
});
