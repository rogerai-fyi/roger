// Guard rails for the design system (web/DESIGN-SYSTEM.md): the layers stay layered, the
// shared components are defined once, the repeated chrome comes from partials, and no new
// hard-coded colour or page-local copy of a shared behaviour slips in. Each rule has a
// written exception list; an entry there is a known debt with its reason, never a
// silent pass. Shrinking a list (or a budget) is always welcome; growing one needs a reason.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const read = (p) => readFileSync(path.join(SRC, p), "utf8");
const dist = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const stripCss = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
const stripHtml = (s) => s.replace(/<!--[\s\S]*?-->/g, "");
const PAGES = readdirSync(SRC).filter((f) => f.endsWith(".html")).sort();
const SHEETS = readdirSync(path.join(SRC, "styles")).filter((f) => f.endsWith(".css")).sort();
before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// the print section is a whole-page override of the chrome and the components
// (test/print.test.mjs), not a definition of any of them: leave it out here
function noPrint(css) {
  let out = stripCss(css);
  for (let i; (i = out.search(/@media print\s*\{/)) >= 0;) {
    let d = 0, j = out.indexOf("{", i);
    do { if (out[j] === "{") d++; else if (out[j] === "}") d--; j++; } while (d && j < out.length);
    out = out.slice(0, i) + out.slice(j);
  }
  return out;
}
// every selector of a stylesheet (keyframe steps and at-rule preludes skipped)
function selectors(css) {
  const out = [];
  for (const m of noPrint(css).matchAll(/([^{}]+)\{/g)) {
    const s = m[1].trim();
    if (s.startsWith("@") || /^(from|to|[\d.]+%)(\s*,|$)/.test(s)) continue;
    for (const part of s.split(/,(?![^(]*\))/)) out.push(part.trim());
  }
  return out;
}
const blockOf = (cls) => cls.replace(/(__|--).*/, "");
const leadClass = (sel) => sel.split(/[\s>+~]+/)[0].match(/^\.([\w-]+)/)?.[1];

/* ---- pages: every page is built from the shared chrome --------------------------- */

// Pages that are not full pages: a redirect or pointer stub renders no chrome at all.
const STUBS = {
  "bands.html": "redirect stub to /models.html",
  "playground.html": "redirect stub to /playbox.html",
  "why.html": "pointer stub to /pricing.html, out of the index",
};
// Pages with the chrome but their own minimal runtime (no site.js on purpose).
// (device.html and stations.html were here; they run the shared runtime now.)
const OWN_RUNTIME = {};

test("every page is assembled from the head, nav and footer partials", () => {
  for (const page of PAGES) {
    if (STUBS[page]) continue;
    const src = read(page);
    for (const partial of ["head.html", "nav.html", "footer.html"]) {
      assert.match(src, new RegExp(`<!--\\s*include:\\s*${partial.replace(".", "\\.")}`), `${page} includes ${partial}`);
    }
  }
});

test("every page runs the shared page runtime from the site-js partial, never a hand-copied tail", () => {
  const shared = ["lets-talk", "site", "session", "promo", "tuner", "scrub", "anchor-hold", "range-twin"];
  for (const page of PAGES) {
    const src = stripHtml(read(page));
    for (const js of shared) {
      assert.doesNotMatch(src, new RegExp(`<script src="js/${js}\\.js"`), `${page} loads js/${js}.js by hand; use <!-- include: site-js.html -->`);
    }
    if (STUBS[page] || OWN_RUNTIME[page]) continue;
    assert.match(read(page), /<!--\s*include:\s*site-js\.html/, `${page} includes site-js.html`);
    // ...so every component module ships with it, and a page gets a behaviour by markup alone
    const built = dist(page);
    for (const js of ["site", "tuner", "scrub", "anchor-hold", "range-twin"]) {
      assert.match(built, new RegExp(`<script src="js/${js}\\.js\\?v=`), `${page} ships js/${js}.js`);
    }
  }
});

test("repeated chrome comes from its partial: the rail, the on-air mark, the install pill", () => {
  for (const page of PAGES) {
    const src = stripHtml(read(page));
    assert.doesNotMatch(src, /<div class="rail"/, `${page}: use <!-- include: rail.html head=... rev=... -->`);
    assert.doesNotMatch(src, /<span class="onair" aria-hidden="true">\(\(/, `${page}: use <!-- include: onair.html -->`);
    // the copy icon's path: an icon pill is the install-box partial
    assert.doesNotMatch(src, /M5 15V5a2 2 0 0 1 2-2h10/, `${page}: use <!-- include: install-box.html id=... cmd='...' -->`);
  }
});

/* ---- stylesheets: the layers ------------------------------------------------------ */

test("every page leads with the three shared layers, in order, before any page sheet", () => {
  for (const page of PAGES) {
    const links = [...dist(page).matchAll(/<link rel="stylesheet" href="styles\/([\w-]+\.css)/g)].map((m) => m[1]);
    if (!links.length) { assert.ok(STUBS[page], `${page} links no stylesheet`); continue; }
    assert.deepEqual(links.slice(0, 3), ["tokens.css", "base.css", "components.css"], `${page}: ${links.join(", ")}`);
  }
});

test("every stylesheet is loaded by some page (no orphan sheet)", () => {
  const manifest = read("../build.mjs");
  for (const sheet of SHEETS) {
    assert.match(manifest, new RegExp(`"${sheet.replace(".", "\\.")}"`), `${sheet} is in build.mjs CSS_SHARED/CSS_BUNDLES`);
  }
});

test("tokens.css declares tokens only: custom properties and color-scheme, nothing else", () => {
  const css = stripCss(read("styles/tokens.css"));
  for (const m of css.matchAll(/\{([^{}]*)\}/g)) {
    for (const decl of m[1].split(";").map((d) => d.trim()).filter(Boolean)) {
      assert.match(decl, /^(--[\w-]+\s*:|color-scheme\s*:)/, `tokens.css: "${decl.slice(0, 60)}" is not a token`);
    }
  }
});

// A shared component is DEFINED in components.css: a rule whose leading compound is the
// component's own class. Page sheets may PLACE it in their own context
// (".app-install__inner .install__box") but not restyle it bare. tokens.css is exempt: it
// only scopes tokens to .tone-zone.
const PAGE_SHEET_COMPONENT_RULES = {
  "research.css": {
    ".section--edge .device-contract": "a research-models modifier of .section",
    ".section--edge .model-roadmap": "a research-models modifier of .section",
    ".section--edge .model-list": "a research-models modifier of .section",
    ".section--edge .model-group-head": "a research-models modifier of .section",
  },
  "home.css": {
    ".section--plate": "the homepage spec plate's tighter section",
  },
};

test("each shared component is defined in components.css and nowhere else", () => {
  const blocks = new Set(selectors(read("styles/components.css")).map(leadClass).filter(Boolean).map(blockOf));
  for (const b of ["section", "research-button", "man-note", "install", "tone-zone", "toc-tuner", "scrub",
                   "tint-panel", "spectrum", "steps", "page-index", "figure", "scroll-box", "code-block"]) {
    // (band--inset is a modifier of .section's .band; see the next assertion)
    assert.ok(blocks.has(b), `components.css defines .${b}`);
  }
  assert.match(read("styles/components.css"), /\.band--inset\s*\{/, "components.css defines .band--inset");
  for (const sheet of SHEETS) {
    if (sheet === "components.css" || sheet === "tokens.css") continue;
    const allowed = PAGE_SHEET_COMPONENT_RULES[sheet] || {};
    for (const sel of selectors(read(`styles/${sheet}`))) {
      const lead = leadClass(sel);
      if (!lead || !blocks.has(blockOf(lead)) || sel in allowed) continue;
      assert.fail(`${sheet} restyles the shared component .${blockOf(lead)} with "${sel}"; change it in components.css, or scope it to a page context`);
    }
  }
});

test("components.css styles only components: no page class rides on the shared layer", () => {
  // components.css must not carry page-specific selectors: every class in it is a component's
  // (or a state/hook it documents), so no page's markup is styled from the shared layer by accident
  const STATES = new Set(["is-copied", "is-current", "is-tuned", "is-dragging", "is-tuning", "is-selected", "is-live", "is-off", "is-quiet", "inline", "fig", "tok", "beacon"]);
  const blocks = new Set(selectors(read("styles/components.css")).map(leadClass).filter(Boolean).map(blockOf));
  for (const sel of selectors(read("styles/components.css"))) {
    for (const [, cls] of sel.matchAll(/\.([\w-]+)/g)) {
      assert.ok(blocks.has(blockOf(cls)) || STATES.has(cls), `components.css: "${sel}" styles .${cls}, which is not a component`);
    }
  }
});

/* ---- colour: tokens only ---------------------------------------------------------- */

// A colour literal (hex, rgb(), hsl()) outside tokens.css. The budget is today's count per
// file; it may only go down. New sheets and new pages get zero. The Playbox decks
// (playbox/wave-patch/wave-factory) are self-contained games with their own palettes; the
// rest are debts to move onto tokens.
const COLOR = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?)\(/g;
const CSS_COLOR_BUDGET = {
  "app.css": 9,                        // phone/screenshot drop shadows per theme
  "base.css": 2,                       // the OC orange's drop shadow, the nav panel shadow
  "billing.css": 1,                    // modal scrim
  "broadcast-routing.css": 5,          // the routing diagram's palette
  "home.css": 20,                      // the reel's film black, book shadows
  "models.css": 2,                     // modal scrim
  "playbox.css": 56,                   // Playbox deck palette
  "research.css": 3,                   // photo credit plate over photography
  "wave-factory.css": 86,              // Playbox factory deck (self-contained game)
  "wave-patch.css": 168,               // Playbox mesh deck (self-contained game)
};
// inline style="" and <style> colours in page sources (the article figures' drift)
const HTML_COLOR_BUDGET = {
  // (empty: the articles' inline figure colours were paid down in the research rollout;
  // their SVG charts colour with var(--live) / var(--paper))
};

test("colours are tokens: no new colour literal outside tokens.css", () => {
  for (const sheet of SHEETS) {
    if (sheet === "tokens.css") continue;
    const n = (stripCss(read(`styles/${sheet}`)).match(COLOR) || []).length;
    const budget = CSS_COLOR_BUDGET[sheet] ?? 0;
    assert.ok(n <= budget, `${sheet} has ${n} colour literals (budget ${budget}); use a token from tokens.css`);
  }
  for (const page of PAGES) {
    const src = stripHtml(read(page));
    const inline = [...src.matchAll(/style="([^"]*)"/g)].map((m) => m[1]).join(";")
      + [...src.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((m) => stripCss(m[1])).join("\n");
    const n = (inline.match(COLOR) || []).length;
    const budget = HTML_COLOR_BUDGET[page] ?? 0;
    assert.ok(n <= budget, `${page} has ${n} inline colour literals (budget ${budget}); use a class and a token`);
  }
  for (const partial of readdirSync(path.join(SRC, "_partials"))) {
    const src = stripHtml(read(`_partials/${partial}`));
    const inline = [...src.matchAll(/style="([^"]*)"/g)].map((m) => m[1]).join(";");
    assert.doesNotMatch(inline, COLOR, `_partials/${partial} has an inline colour literal`);
  }
});

test("the colour budgets are tight: a paid-down debt lowers its budget", () => {
  for (const [sheet, budget] of Object.entries(CSS_COLOR_BUDGET)) {
    const n = (stripCss(read(`styles/${sheet}`)).match(COLOR) || []).length;
    assert.equal(n, budget, `${sheet}: ${n} colour literals now; set its budget to ${n}`);
  }
});

// A var() of a property that nothing defines is invalid at computed-value time: the
// declaration silently falls back to the property's initial value. Each of these is a live
// bug on its page today; defining the token would change how the page looks, so each is a
// decision for that page's rollout, not for this guard.
const UNDEFINED_TOKENS = {
  "--kept": "research.css bar, never set",
  "--fill": "research.css bar, never set",
};

test("every var() names a defined property or carries its own fallback", () => {
  const sources = [
    ...SHEETS.map((f) => stripCss(read(`styles/${f}`))),
    ...readdirSync(path.join(SRC, "js")).map((f) => read(`js/${f}`)),
    ...PAGES.map(read),
  ].join("\n");
  const defined = new Set([...sources.matchAll(/(--[\w-]+)\s*:/g)].map((m) => m[1]));
  for (const m of sources.matchAll(/setProperty\(\s*["'](--[\w-]+)["']/g)) defined.add(m[1]);
  for (const sheet of SHEETS) {
    for (const m of stripCss(read(`styles/${sheet}`)).matchAll(/var\((--[\w-]+)\)/g)) {
      if (defined.has(m[1]) || UNDEFINED_TOKENS[m[1]]) continue;
      assert.fail(`${sheet}: var(${m[1]}) is defined nowhere and has no fallback`);
    }
  }
  for (const name of Object.keys(UNDEFINED_TOKENS)) {
    assert.ok(!defined.has(name), `${name} is defined now: drop it from UNDEFINED_TOKENS`);
  }
});

/* ---- one thing, one name ---------------------------------------------------------- */

// Every innermost rule of a sheet: [selector text, declarations as Map(prop -> value)].
function rules(css) {
  return [...stripCss(css).matchAll(/([^{}]+)\{([^{}]*)\}/g)].map(([, sel, body]) => [
    sel.trim().replace(/\s+/g, " "),
    new Map(body.split(";").map((d) => d.trim()).filter((d) => /^[\w-]+\s*:/.test(d))
      .map((d) => [d.slice(0, d.indexOf(":")).trim(), d.slice(d.indexOf(":") + 1).trim().replace(/\s+/g, " ")])),
  ]);
}

// The tinted panel's ground (--paper-2 on the panel radius) is declared by ONE rule: the
// shared one in components.css (.tint-panel and the components built on the same ground).
// Anything else wanting the ground takes class="tint-panel". Parallel page work invented
// this panel four times over (.tint-panel x3, .inset-panel); this is the tripwire.
const TINT_GROUND_ELSEWHERE = {
  "home.css .pcards article": "the privacy cards turn into tinted panels only as a touch-phone gallery; the class cannot be conditional",
};
test("the tinted panel ground is declared once; every other panel takes .tint-panel", () => {
  const owners = [], seen = new Set();
  for (const sheet of SHEETS) {
    for (const [sel, d] of rules(read(`styles/${sheet}`))) {
      const bg = d.get("background") ?? d.get("background-color");
      if (bg !== "var(--paper-2)" || d.get("border-radius") !== "var(--panel-r)") continue;
      if (TINT_GROUND_ELSEWHERE[`${sheet} ${sel}`]) { seen.add(`${sheet} ${sel}`); continue; }
      owners.push(`${sheet} ${sel}`);
    }
  }
  assert.equal(owners.length, 1, `the tinted panel ground is declared by ${owners.length} rules:\n  ${owners.join("\n  ")}\n(use class="tint-panel", or extend the one shared rule)`);
  assert.match(owners[0], /^components\.css \.tint-panel\b/);
  assert.deepEqual(Object.keys(TINT_GROUND_ELSEWHERE).filter((k) => !seen.has(k)), [], "a listed exception no longer exists: drop it");
});

// Two differently named classes with the same declarations are one component under two
// names. Compared: each sheet's top-level rules that style a bare class (custom properties
// aside). Declarations two classes take from ONE grouped rule (".a, .b { }") are shared, the
// DRY way to give two names one look, so only independently written declarations count.
// Flagged: an identical set of 5+ declarations, or 6+ independently repeated declarations
// making up 85%+ of both. Each pair below is a known twin, with its reason.
const ACCOUNT_KIT = "the signed-in pages' form and chart kit, drawn per page before a shared account kit existed; merge into account-base.css when those pages are lifted";
const LABEL_TYPE = "the site's mono label type set (mono, micro, tracked, uppercase) on an element of another kind: the same type, not the same component";
const LAYOUT = "a layout coincidence (the same grid or bar), not one component";
const DIALOG = "two dialogs of one design (billing help, report a station); a shared dialog component is the follow-up (Let's talk is a third)";
const FLOW = "one step-flow figure drawn in two articles; promote it to the article kit (broadcasts.css) when a third article needs it";
const TWIN_EXCEPTIONS = {
  "account.css .ac-field__hint = billing.css .bx-limit__hint": ACCOUNT_KIT,
  "account.css .ac-field__in = keys.css .kf__in": ACCOUNT_KIT,
  "account.css .ac-field__k = billing.css .bx-limit__k": ACCOUNT_KIT,
  "account.css .ac-field__k = payouts.css .po-ladder__title": ACCOUNT_KIT,
  "account.css .ac-wallet__amt = dashboard.css .ds-hero__amt": ACCOUNT_KIT,
  "account.css .ac-wallet__eyebrow = dashboard.css .ds-hero__eyebrow": ACCOUNT_KIT,
  "account.css .ac-wallet__sub = dashboard.css .ds-hero__sub": ACCOUNT_KIT,
  "billing.css .bx-limit__k = payouts.css .po-ladder__title": ACCOUNT_KIT,
  "billing.css .bx-limit__track = payouts.css .po-meter__track": ACCOUNT_KIT,
  "billing.css .bx-save__cap = dashboard.css .ds-chart__title": ACCOUNT_KIT,
  "billing.css .bx-save__cap = metrics.css .mx-chart__title": ACCOUNT_KIT,
  "billing.css .bx-save__cap = metrics.css .mx-ts__title": ACCOUNT_KIT,
  "billing.css .bx-vel__span = dashboard.css .ds-chart__title": ACCOUNT_KIT,
  "billing.css .bx-vel__span = metrics.css .mx-chart__title": ACCOUNT_KIT,
  "billing.css .bx-vel__span = metrics.css .mx-ts__title": ACCOUNT_KIT,
  "billing.css .bx-save__lab = dashboard.css .ds-mbars__label": ACCOUNT_KIT,
  "billing.css .bx-save__lab = metrics.css .mx-bars__label": ACCOUNT_KIT,
  "billing.css .bx-save__val = dashboard.css .ds-mbars__val": ACCOUNT_KIT,
  "billing.css .bx-save__val = metrics.css .mx-bars__val": ACCOUNT_KIT,
  "dashboard.css .ds-chart__head = metrics.css .mx-chart__head": ACCOUNT_KIT,
  "dashboard.css .ds-chart__title = metrics.css .mx-chart__title": ACCOUNT_KIT,
  "dashboard.css .ds-chart__title = metrics.css .mx-ts__title": ACCOUNT_KIT,
  "dashboard.css .ds-legend = metrics.css .mx-legend": ACCOUNT_KIT,
  "dashboard.css .ds-legend__sw = metrics.css .mx-legend__sw": ACCOUNT_KIT,
  "dashboard.css .ds-mbars__label = metrics.css .mx-bars__label": ACCOUNT_KIT,
  "dashboard.css .ds-mbars__track = metrics.css .mx-bars__track": ACCOUNT_KIT,
  "dashboard.css .ds-mbars__val = metrics.css .mx-bars__val": ACCOUNT_KIT,
  "base.css .fig = keys.css .kf__paste-k": LABEL_TYPE,
  "billing.css .bx-help = models.css .reportmodal": DIALOG,
  "billing.css .bx-help__x = models.css .reportmodal__x": DIALOG,
  "billing.css .bx-help__done = models.css .reportform__submit": DIALOG,
  "base.css .fig = broadcasts.css .bc-answer__tag": LABEL_TYPE,
  "base.css .fig = components.css .page-index__head": LABEL_TYPE,
  "broadcasts.css .bc-answer__tag = components.css .page-index__head": LABEL_TYPE,
  "broadcasts.css .bc-answer__tag = components.css .tint-panel__tag": LABEL_TYPE,
  "broadcasts.css .bc-post__meta = manual.css .man-cover__meta": "the document meta line of an article and of the manual's cover: two documents, one typesetting",
  "broadcast-agent-governance.css .gov-flow = broadcast-gpu-isolation.css .gpu-flow": FLOW,
  "broadcast-agent-governance.css .gov-flow__no = broadcast-gpu-isolation.css .gpu-flow__no": FLOW,
  "broadcast-agent-governance.css .gov-flow__arrow = broadcast-gpu-isolation.css .gpu-flow__arrow": FLOW,
  "broadcast-agent-governance.css .gov-ic = research.css .uc-ic": "a line icon's box on two pages: " + LAYOUT,
  "broadcast-economics.css .ec-answer-grid = broadcast-routing.css .route-process__steps": LAYOUT,
  "components.css .install = voices.css .voice-tune": "the /voices command block is laid out like .install but keeps its own figure spacing; adopting .install would move its FIG. label",
  "models.css .band-row--head = voices.css .voice-row--head": "the two directory tables' header rows: one type, each table's own columns",
  "tower.css .tower__head = wave-family.css .wf-rail__head": "a figure's title bar on two different instruments: " + LAYOUT,
  "tower.css .run__note = wave-family.css .wf-bandnote": "a small note under a figure on two pages: " + LAYOUT,
  "wave-factory.css .cl-say__prov = wave-factory.css .cl-auto__note": "inside the Playbox factory game (self-contained; its type-floor test pins each rule)",
};
function classDecls() {
  const out = [];
  for (const sheet of SHEETS) {
    const byClass = new Map();
    const css = stripCss(read(`styles/${sheet}`));
    // top-level rules only (anything inside an at-rule is a variation, not a definition)
    let i = 0;
    while (i < css.length) {
      const open = css.indexOf("{", i);
      if (open < 0) break;
      const sel = css.slice(i, open).trim();
      let d = 1, j = open + 1;
      while (j < css.length && d) { if (css[j] === "{") d++; else if (css[j] === "}") d--; j++; }
      if (!sel.startsWith("@")) {
        for (const part of sel.split(/,(?![^(]*\))/).map((p) => p.trim())) {
          if (!/^\.[\w-]+$/.test(part)) continue;
          const m = byClass.get(part) || new Map();
          for (const [, decls] of rules(`x{${css.slice(open + 1, j - 1)}}`)) for (const [p, v] of decls) if (!p.startsWith("--")) m.set(p, { v, src: `${sheet}@${open}` });
          byClass.set(part, m);
        }
      }
      i = j;
    }
    for (const [cls, decls] of byClass) out.push({ key: `${sheet} ${cls}`, cls, decls });
  }
  return out;
}
test("no component exists twice under two names (identical or near-identical declarations)", () => {
  const all = classDecls().filter((c) => c.decls.size >= 5);
  const twins = [], found = new Set();
  for (let a = 0; a < all.length; a++) {
    for (let b = a + 1; b < all.length; b++) {
      const A = all[a], B = all[b];
      if (A.cls === B.cls) continue;
      const equal = [...A.decls].filter(([p, a]) => B.decls.get(p)?.v === a.v);
      if (equal.every(([p, a]) => B.decls.get(p).src === a.src)) continue;   // one grouped rule: shared, not twinned
      const shared = equal.length;
      const same = shared === A.decls.size && shared === B.decls.size;
      const near = shared >= 6 && shared / A.decls.size >= 0.85 && shared / B.decls.size >= 0.85;
      if (!same && !near) continue;
      const pair = `${A.key} = ${B.key}`;
      found.add(pair);
      if (!TWIN_EXCEPTIONS[pair]) twins.push(pair);
    }
  }
  assert.deepEqual(twins, [], `twin classes (merge them into one component, or list the pair with its reason):\n  ${twins.join("\n  ")}`);
  const stale = Object.keys(TWIN_EXCEPTIONS).filter((pair) => !found.has(pair));
  assert.deepEqual(stale, [], "these pairs are no longer twins: drop them from TWIN_EXCEPTIONS");
});

/* ---- no red glows ------------------------------------------------------------------ */

// The founder rejected glows: a blurred red halo on a surface (the FIG. 1 panel's hover glow
// was the last). Red may RING (a 0-blur spread: focus, an on-air dot's halo) and may be a
// LAMP: a small light source that is itself the indicator. Every blurred red shadow in any
// sheet must be one of these lamps, listed with its reason; nothing unlisted blurs past 12px.
const RED = /var\(--live(-glow|-wash|-tint)?\b|224,\s*35,\s*28|255,\s*68,\s*56|#E0231C|#FF4438/i;
const RED_LAMPS = {
  "base.css .brand__pulse": "the brand mark's on-air beacon (3px)",
  "base.css .ping__eye": "Ping's eye lamp (3px)",
  "home.css .pingband__grille": "the LED banner's 6px grille dot (5px)",
  'home.css .pingdeck[data-ping-state="onair"] .pingdeck__led, .pingdeck[data-ping-state="transmit"] .pingdeck__led': "the deck's on-air / transmit LED (6px)",
  "home.css .twoway__beam::after": "the 6px dot travelling the beam (6px)",
  "playbox.css .dk__lamp.is-lit": "Playbox (a self-contained game): the deck's lit lamp",
  "playbox.css .dk__dial i::before": "Playbox (a self-contained game): the dial's needle lamp",
  "playbox.css .dk__spine.is-loaded": "Playbox (a self-contained game): the loaded cassette's lift",
  "wave-patch.css from": "Playbox mesh deck (a self-contained game): a one-shot alarm flash",
};
function shadowParts(value) {
  // split a shadow list on top-level commas; drop-shadow() arguments too
  const out = [];
  for (const m of value.matchAll(/drop-shadow\(([^()]*(?:\([^()]*\)[^()]*)*)\)/g)) out.push(m[1]);
  if (!/drop-shadow/.test(value)) {
    let depth = 0, cur = "";
    for (const ch of value) { if (ch === "(") depth++; if (ch === ")") depth--; if (ch === "," && !depth) { out.push(cur); cur = ""; } else cur += ch; }
    out.push(cur);
  }
  return out.map((p) => p.trim());
}
const blurOf = (part) => {
  const lens = part.replace(/\binset\b/, "").replace(/(color-mix|rgba?|var)\([^()]*(\([^()]*\))?[^()]*\)/g, "").trim()
    .split(/\s+(?![^(]*\))/).filter((t) => /^(-?[\d.]+(px|rem|em)?|calc\(.*\))$/.test(t));
  const b = lens[2];
  if (!b) return 0;
  return /^calc/.test(b) ? Infinity : parseFloat(b);
};
test("no red glows: red rings and small lamps only, every lamp listed", () => {
  const glows = [], seen = new Set();
  for (const sheet of SHEETS) {
    for (const [sel, d] of rules(read(`styles/${sheet}`))) {
      for (const prop of ["box-shadow", "filter", "text-shadow"]) {
        const v = d.get(prop);
        if (!v || !RED.test(v)) continue;
        for (const part of shadowParts(v)) {
          if (!RED.test(part)) continue;
          const blur = blurOf(part);
          if (!blur) continue;                                   // a ring
          const key = `${sheet} ${sel}`;
          assert.ok(blur <= 12 || RED_LAMPS[key], `${key}: a ${blur}px red blur is a glow`);
          if (RED_LAMPS[key]) { seen.add(key); continue; }
          glows.push(`${key} { ${prop}: ${v} }`);
        }
      }
    }
  }
  assert.deepEqual(glows, [], `red glows (the founder rejected glows; ring it, or list a lamp):\n  ${glows.join("\n  ")}`);
  assert.deepEqual(Object.keys(RED_LAMPS).filter((k) => !seen.has(k)), [], "a listed lamp no longer glows: drop it");
});

/* ---- behaviour: one implementation per shared behaviour --------------------------- */

// The copy tick lives in site.js (every .install__box, and [data-copy-target]). These
// scripts still copy on their own, with a different affordance; each is a known debt.
const OWN_CLIPBOARD = {
  "site.js": "the shared copy tick",
  "keys.js": "copies a one-time secret and relabels its button (keys page)",
  "private.js": "copies a private station's share link (private console)",
  "playbox.js": "copies the deck's CLI line into the deck's own toast",
};

test("no page script re-implements a shared behaviour", () => {
  const js = readdirSync(path.join(SRC, "js")).filter((f) => f.endsWith(".js"));
  const HOOKS = { "[data-fold-narrow]": "site.js", "[data-tuner]": "tuner.js", "[data-scrub]": "scrub.js", "[data-anchor-hold]": "anchor-hold.js", "[data-copy-target]": "site.js", "[data-range-twin]": "range-twin.js" };
  for (const f of js) {
    const src = read(`js/${f}`);
    if (/navigator\.clipboard|execCommand\(["']copy/.test(src)) {
      assert.ok(OWN_CLIPBOARD[f], `${f} copies to the clipboard itself; use the shared copy tick (.install__box or [data-copy-target])`);
    }
    for (const [hook, owner] of Object.entries(HOOKS)) {
      if (f !== owner) assert.ok(!src.includes(hook), `${f} handles ${hook}, which belongs to ${owner}`);
    }
  }
});

test("the component modules initialize from their data- hook, so markup alone opts a page in", () => {
  assert.match(read("js/tuner.js"), /querySelectorAll\("\[data-tuner\]"\)/);
  assert.match(read("js/scrub.js"), /querySelectorAll\("\[data-scrub\]"\)/);
  assert.match(read("js/anchor-hold.js"), /querySelector\("\[data-anchor-hold\]"\)/);
  assert.match(read("js/range-twin.js"), /querySelectorAll\("\[data-range-twin\]"\)/);
  assert.match(read("js/site.js"), /closest\("\[data-copy-target\]"\)/);
  assert.match(read("js/site.js"), /querySelectorAll\("details\[data-fold-narrow\]"\)/);
});
