// Post-launch polish (2026-09-24): the follow-ups the launch QA report left open.
// Static checks on sources and sheets; the rendered checks (label size, tap targets)
// live in scripts/overflow-sweep.py's companions and were run at 390 by hand.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const WEB = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (f) => readFileSync(path.join(WEB, f), "utf8");
const css = (f) => read(path.join("src/styles", f)).replace(/\/\*[\s\S]*?\*\//g, "");

test("the OS lock is documented for both of its jobs: a one-platform command and a cross-platform non-installer one", () => {
  const js = read("src/js/site.js");
  const lock = js.slice(js.indexOf("[data-os-lock] boxes"), js.indexOf("document.querySelectorAll(\".install__box:not"));
  assert.match(lock, /data-os-lock="linux"/, "site.js comment names the one-platform lock");
  assert.match(lock, /data-os-lock="any"/, "site.js comment names the cross-platform lock");
  assert.match(lock, /roger use/);
  const ds = read("DESIGN-SYSTEM.md");
  assert.match(ds, /oslock=any/);
  assert.match(ds, /roger say/);
});

// Item 2: the inline SVG diagrams that shrank to ~5px labels on a phone. Each sits in the
// shared diagram scroll box, which keeps the drawing at least --diagram-min wide and
// scrolls it sideways (with the edge shade) on a narrower column. The floor is set so the
// figure's smallest label renders at 11px or more: 11 * viewBox width / smallest label
// size in SVG units (measured at 390 with the page's own phone sizes). Where a page sets
// more than one floor (a desktop one and a phone one), the widest is the one a phone gets.
const LABEL_FLOOR = 11;
const DIAGRAMS = [
  // page, sheet, the svg's class hook, viewBox width, smallest label (SVG units)
  { page: "tower.html", sheet: "tower.css", svg: "signal__svg", vbw: 720, smallest: 9 },
  { page: "tower.html", sheet: "tower.css", svg: "tower__svg", vbw: 640, smallest: 8.5 },
  { page: "pricing.html", sheet: "pricing.css", svg: "rwire__svg", vbw: 958, smallest: 16 },
  { page: "research-industry.html", sheet: "research.css", svg: "purdue__svg", vbw: 720, smallest: 9 },
  { page: "research-wave-family.html", sheet: "wave-family.css", svg: "wf-orbit__svg", vbw: 900, smallest: 16 },
];

test("the diagram scroll box is one shared component: a floor width and the edge shade", () => {
  const c = css("components.css");
  assert.match(c, /\.scroll-box--diagram > svg \{[^}]*min-width: var\(--diagram-min/);
  assert.match(c, /\.scroll-box--diagram, \.code-block pre \{[^}]*no-repeat local,[^}]*no-repeat scroll/,
    "the diagram box shares the code block's edge shade (one rule, not a copy)");
});

for (const d of DIAGRAMS) {
  test(`${d.page} ${d.svg}: in the diagram scroll box, at a floor that keeps labels >= ${LABEL_FLOOR}px`, () => {
    const html = read(path.join("src", d.page));
    const re = new RegExp(`<div class="scroll-box scroll-box--diagram"[^>]*>\\s*<svg class="${d.svg}"`);
    assert.match(html, re, "the svg is the direct child of the diagram scroll box");
    const hook = d.svg.replace("__svg", "");
    const floors = [...css(d.sheet).matchAll(new RegExp(`\\.${hook}[^{}]*\\{[^}]*--diagram-min: (\\d+)px`, "g"))].map((m) => Number(m[1]));
    assert.ok(floors.length, `${d.sheet} sets --diagram-min in the .${hook} context`);
    const need = Math.ceil((LABEL_FLOOR * d.vbw) / d.smallest);
    assert.ok(Math.max(...floors) >= need, `${d.svg}: floor ${Math.max(...floors)}px < ${need}px`);
  });
}

// Item 3: every standalone control is a 44px target on a touch screen. The sweep (Playwright
// at 390, touch emulation) listed each control under 44x44; each is held by one of three
// rules: the pattern is documented in components.css's touch block, the math is the
// --hit-inset token, and each sheet lists the small controls it owns. Links inside running text (a
// sentence, a callout, a spec plate) are exempt, as WCAG's target-size rule exempts them.
// the bodies of a sheet's @media (pointer: coarse) blocks, braces balanced
const coarseBlocks = (sheet) => {
  const out = [];
  for (const m of sheet.matchAll(/@media \(pointer: coarse\) \{/g)) {
    let i = m.index + m[0].length, depth = 1;
    const start = i;
    while (depth && i < sheet.length) { if (sheet[i] === "{") depth++; else if (sheet[i] === "}") depth--; i++; }
    out.push(sheet.slice(start, i - 1));
  }
  return out.join("\n");
};
const HIT_GROW = [ // keep their drawn size; an invisible ::before grows the tap area
  ".brand", ".promo__close", ".promo__cta", ".upgrade__toggle", ".nav__burger", ".theme-toggle",
  ".install__alt", ".bc-post__back", ".wf-back", ".dir-sibling > a", ".company__links a",
  ".home-spectrum__foot a", ".tlink", ".faq__more", ".term__preset", ".market__refresh",
  ".tuner__chip", ".company__primary", ".route-sim__next", ".scope__mode", ".wj__chip",
  ".model-note", ".copy-code", ".dk__spine", ".code-block__copy", ".reel__mute", ".dk__pos",
  ".bc-tg__head", ".pg-mode",
];
test("touch: small controls keep their look and grow an invisible 44px hit area", () => {
  assert.match(css("tokens.css"), /--hit: 44px;/);
  assert.match(css("tokens.css"), /--hit-inset: min\(0px, calc\(\(100% - var\(--hit\)\) \/ 2\)\);/);
  const sheets = readdirSync(path.join(WEB, "src/styles")).filter((f) => f.endsWith(".css"));
  const rules = sheets.flatMap((f) => [...coarseBlocks(css(f)).matchAll(/([^{}]+)\{[^}]*content: "";[^}]*position: absolute;[^}]*inset: var\(--hit-inset\)/g)].map((m) => m[1]));
  const grown = rules.flatMap((sel) => sel.split(/,(?![^(]*\))/).flatMap((part) => {
    const m = part.trim().match(/^(?::is\((.*)\)|(.*?))::(before|after)$/);
    return m ? (m[1] || m[2]).split(",").map((x) => x.trim()) : [];
  }));
  for (const sel of HIT_GROW) assert.ok(grown.includes(sel), `${sel} grows a hit area`);
  // a static control becomes the pseudo-element's containing block at zero specificity,
  // so a control a page positions (absolute, fixed) keeps its own position
  for (const f of sheets) for (const m of coarseBlocks(css(f)).matchAll(/([^{}]*)\{ position: relative; \}/g)) {
    assert.match(m[1].trim(), /^:where\(/, `${f}: the containing-block rule is zero-specificity`);
  }
});

test("touch: lists of text links get 44px rows, and form fields are 44px tall", () => {
  const c = coarseBlocks(css("components.css"));
  assert.match(c, /:is\(\.page-index__link, \.page-index__head\) \{[^}]*display: block;[^}]*padding-block: calc\(\(var\(--hit\) - 1lh\) \/ 2\)/);
  assert.match(c, /:where\(input:not\(\[type="checkbox"\], \[type="radio"\], \[type="range"\], \[type="hidden"\]\), select, textarea\) \{ min-height: var\(--hit\); \}/);
  // the contents tuner's stations grow downward only (the dial's ticks and readout are laid
  // out against the band, so a station must not become a containing block or move its numeral)
  assert.match(c, /\.toc-tuner__st \{ min-height: var\(--hit\); \}/);
});

