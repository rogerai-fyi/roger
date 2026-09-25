// The designer QA pass (2026-09): each finding fixed in the system, held here so it
// cannot quietly come back. Static checks on the built sheets; the pages themselves
// were checked by eye at 1440 and 390, light and dark (the qa report).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const WEB = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const css = (f) => readFileSync(path.join(WEB, "src/styles", f), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
const phone = (sheet) => [...sheet.matchAll(/@media \(max-width: 560px\) \{([\s\S]*?)\n\}/g)].map((m) => m[1]).join("\n");

test("a hero eyebrow wraps whole parts (no side-by-side columns); on a phone the separator is the line break", () => {
  const base = css("base.css");
  assert.match(base, /\.hero__eyebrow \{[^}]*flex-wrap: wrap/);
  assert.match(phone(base), /\.hero__eyebrow \.hero__sep \{[^}]*flex-basis: 100%[^}]*height: 0/);
});

test("the slim wave mark draws its resting waves on any page (Research loads no wave-family.css)", () => {
  const base = css("base.css");
  for (const w of ["wide", "live", "h2", "h3", "h4"]) {
    assert.match(base, new RegExp(`\\.wave-mark--slim \\.wave-mark__wave--${w} \\{[^}]*stroke: var\\(`), w);
  }
});

test("research cards keep the tinted panel ground (no page rule paints them paper)", () => {
  assert.doesNotMatch(css("research.css"), /\.deployment-grid article \{[^}]*background: var\(--paper\)/);
});

test("research on a phone: the four-card contract is one column and the group head stacks", () => {
  const p = phone(css("research.css"));
  assert.match(p, /\.device-contract:has\(> p:nth-child\(4\):last-child\)[^{]*\{[^}]*grid-template-columns: 1fr/);
  assert.match(p, /\.model-group-head \{[^}]*flex-direction: column/);
});

test("two inset bands in a row keep a gap between them", () => {
  assert.match(css("components.css"), /\.band--inset \+ \.band--inset \{[^}]*margin-top: var\(--s-/);
});

test("the live callout carries its red as the rule, not as a wash behind the words", () => {
  assert.match(css("components.css"), /\.man-note--live \{[^}]*border-left-color: var\(--live\)[^}]*background: var\(--paper-2\)/);
});

test("the running-head rail paints no ground (it notched the footer and full-bleed bands)", () => {
  assert.match(css("base.css"), /\.rail \{[^}]*background: transparent/);
});

test("footer: the colophon sets on the column on a phone; links are 44px targets on touch", () => {
  const base = css("base.css");
  assert.match(base, /@media \(max-width: 640px\) \{ \.footer__colophon \{ text-align: left; \} \}/);
  assert.match(base, /@media \(pointer: coarse\) \{[^@]*\.footer__links a \{ padding-block: 11px; line-height: 22px; \}/);
});

test("article figures that swap ink and paper stay a dark room on the dark site", () => {
  assert.match(css("broadcast-agent-governance.css"), /:root\[data-theme="dark"\] :is\(\.gov-compare__col--live, \.gov-flow__step--live\) \{[^}]*background: var\(--white\)/);
  assert.match(css("broadcast-gpu-isolation.css"), /:root\[data-theme="dark"\] :is\(\.gpu-machines__col--live, \.gpu-flow__step--live\) \{[^}]*background: var\(--white\)/);
});

test("the governance timeline has one column per stop (a fixed five left a hole for four)", () => {
  const g = css("broadcast-agent-governance.css");
  assert.doesNotMatch(g, /\.gov-timeline \{[^}]*repeat\(5/);
  assert.match(g, /\.gov-timeline \{[^}]*grid-auto-flow: column/);
});

test("a scrolling code line shows its edge (a shade while there is more), on the block's own ground", () => {
  const c = css("components.css");
  assert.match(c, /\.code-block \{[^}]*--cb-ground: var\(--paper-2\)/);
  assert.match(c, /\.code-block pre \{[^}]*no-repeat local,[^}]*no-repeat scroll/);
  assert.match(c, /\.tint-panel \.code-block \{ --cb-ground: var\(--paper\); \}/);
});

test("a page that ends on a panel leaves the footer room", () => {
  assert.match(css("components.css"), /main > :is\(\.tone-zone, \.band--inset\):not\(:has\(~ :not\(\[hidden\], script\)\)\) \{ margin-bottom: var\(--s-16\); \}/);
});

test("the lean bar (account pages, no burger) keeps its links and theme switch on a phone", () => {
  const base = css("base.css");
  assert.match(base, /\.nav:not\(:has\(\.nav__burger\)\) \.nav__menu \{[^}]*position: static[^}]*visibility: visible/);
});

test("the models dial needle is a hairline: no glow, no red wash behind it", () => {
  const m = css("models.css");
  assert.doesNotMatch(m, /\.dial__pointer \{[^}]*box-shadow: (?!none)/);
  assert.doesNotMatch(m, /\.dial__glass \{[^}]*--live-wash/);
});

test("phone fixes on App and the Playbox: the handheld clears its caption, tier rows get the width", () => {
  assert.match(phone(css("app.css")), /\.app-hero__devices \{ padding-top: var\(--s-12\); \}/);
  assert.match(phone(css("playbox.css")), /\.dk__shelfrow \{ grid-template-columns: 1fr;/);
});
