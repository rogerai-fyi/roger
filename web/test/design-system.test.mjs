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

// every selector of a stylesheet (keyframe steps and at-rule preludes skipped)
function selectors(css) {
  const out = [];
  for (const m of stripCss(css).matchAll(/([^{}]+)\{/g)) {
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
const OWN_RUNTIME = {
  "device.html": "CLI sign-in approval: auth.js + device.js only",
  "stations.html": "operator station roll-up: auth.js + stations.js only",
};

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
  const shared = ["lets-talk", "site", "session", "promo", "tuner", "scrub", "anchor-hold"];
  for (const page of PAGES) {
    const src = stripHtml(read(page));
    for (const js of shared) {
      assert.doesNotMatch(src, new RegExp(`<script src="js/${js}\\.js"`), `${page} loads js/${js}.js by hand; use <!-- include: site-js.html -->`);
    }
    if (STUBS[page] || OWN_RUNTIME[page]) continue;
    assert.match(read(page), /<!--\s*include:\s*site-js\.html/, `${page} includes site-js.html`);
    // ...so every component module ships with it, and a page gets a behaviour by markup alone
    const built = dist(page);
    for (const js of ["site", "tuner", "scrub", "anchor-hold"]) {
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
    ".research-button": "research-shell pages stack their buttons full-width on phones; decide per page in the rollout",
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
  for (const b of ["section", "research-button", "man-note", "install", "tone-zone", "toc-tuner", "scrub", "figure", "scroll-box", "code-block", "inset-panel"]) {
    assert.ok(blocks.has(b), `components.css defines .${b}`);
  }
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
  const STATES = new Set(["is-copied", "is-current", "is-tuned", "inline", "fig", "tok"]);
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
  "broadcast-routing.css": 9,          // the routing diagram's palette
  "device.css": 3,                     // fallbacks of an undefined --rule token
  "home.css": 24,                      // the reel's film black, book shadows, the wave mask
  "models.css": 2,                     // modal scrim
  "payouts.css": 2,                    // fallbacks of an undefined --warn token
  "playbox.css": 56,                   // Playbox deck palette
  "private.css": 15,                   // private-station console palette
  "research-hardware.css": 3,          // photo credit plate over photography
  "research.css": 3,                   // photo credit plate over photography
  "stations.css": 3,                   // fallbacks of undefined --rule/--beacon/--warn tokens
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
  "--t-lg": "account-base.css #code-input font-size",
  "--hover-bg": "base.css nav panel hover, research.css",
  "--s-7": "research.css, tower.css spacing (the scale has no 7)",
  "--t-base": "home.css privacy card heading size",
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
  const HOOKS = { "[data-fold-narrow]": "site.js", "[data-tuner]": "tuner.js", "[data-scrub]": "scrub.js", "[data-anchor-hold]": "anchor-hold.js", "[data-copy-target]": "site.js" };
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
  assert.match(read("js/site.js"), /closest\("\[data-copy-target\]"\)/);
  assert.match(read("js/site.js"), /querySelectorAll\("details\[data-fold-narrow\]"\)/);
});
