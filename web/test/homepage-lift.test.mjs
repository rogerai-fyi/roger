// Homepage "lift" (2026-09-23): a PRESENTATION-only pass on the hero, the
// FIG. 1 download panel and the section entrances. These tests pin the three
// promises that pass made:
//   (a) the words did not change - the hero's visible text (and its links) is
//       character-for-character the pre-lift copy, captured from origin/main
//       into fixtures/homepage-hero-text.json. Only whitespace is free, since
//       re-typesetting lines (spans instead of <br>) moves line breaks around.
//       A deliberate COPY change must update that fixture in the same commit.
//   (b) every new motion is opt-in: it lives inside a
//       `prefers-reduced-motion: no-preference` block, so reduced motion never
//       even sees a starting frame.
//   (c) no content hides behind JavaScript: the hero, the framed download
//       panel and its command are in the HTML, and no lift rule is gated on
//       the html.js class.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist");
const read = (p) => readFileSync(path.join(DIST, p), "utf8");
const fixture = JSON.parse(readFileSync(path.join(WEB, "test/fixtures/homepage-hero-text.json"), "utf8"));

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const heroSection = () => read("index.html").match(/<section class="hero">[\s\S]*?<\/section>/)?.[0] || "";
const visibleText = (html) =>
  html.replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();
const squash = (s) => s.replace(/\s+/g, "");

// Return the bodies of every `@media (...)` block whose prelude matches `re`,
// by brace matching (regex alone cannot find the end of a nested block).
function mediaBlocks(css, re) {
  const out = [];
  const at = /@media[^{]*\{/g;
  let m;
  while ((m = at.exec(css))) {
    if (!re.test(m[0])) continue;
    let depth = 1, i = at.lastIndex;
    for (; i < css.length && depth; i++) {
      if (css[i] === "{") depth++;
      else if (css[i] === "}") depth--;
    }
    out.push({ start: m.index, end: i, body: css.slice(at.lastIndex, i - 1) });
  }
  return out;
}

test("(a) the hero words are unchanged from the pre-lift copy (whitespace aside)", () => {
  const now = visibleText(heroSection());
  assert.ok(now.length > 500, "the hero section was found");
  assert.equal(squash(now), squash(fixture.text), "hero text changed: the lift may move words between lines, never edit them");
});

test("(a) the hero links are the same links, in the same order", () => {
  const hrefs = [...heroSection().matchAll(/href="([^"]*)"/g)].map((m) => m[1]);
  assert.deepEqual(hrefs, fixture.hrefs);
});

test("(a) the headline keeps a word boundary where each <br> used to be", () => {
  // Lines became block spans. Without whitespace between them the accessible
  // name and copied text would read "world'sdecentralized".
  const h1 = heroSection().match(/<h1 class="hero__title"[\s\S]*?<\/h1>/)?.[0] || "";
  assert.doesNotMatch(h1, /<br\s*\/?>/, "lines are spans, not <br>");
  assert.match(h1.replace(/<[^>]+>/g, ""), /world&rsquo;s\s+decentralized/);
  assert.match(h1.replace(/<[^>]+>/g, ""), /Models\.\s+In under 30 seconds\./);
});

test("(b) the lift's keyframes exist and are only ever used under no-preference", () => {
  const css = read("styles/home.css");
  const frames = [...css.matchAll(/@keyframes\s+(lift-[\w-]+)/g)].map((m) => m[1]);
  assert.ok(frames.length >= 2, "the hero entrance and the scroll reveal have keyframes");
  const safe = mediaBlocks(css, /prefers-reduced-motion:\s*no-preference/);
  assert.ok(safe.length >= 1, "there is a no-preference motion block");
  let outside = css;
  for (const b of [...safe].reverse()) outside = outside.slice(0, b.start) + outside.slice(b.end);
  assert.doesNotMatch(outside.replace(/@keyframes[^{]*\{(?:[^{}]*\{[^}]*\})*[^}]*\}/g, ""), /\blift-[\w-]+/,
    "no lift animation is applied outside a prefers-reduced-motion: no-preference block");
  const inside = safe.map((b) => b.body).join("\n");
  for (const f of frames) assert.match(inside, new RegExp(`animation[^;]*\\b${f}\\b`), `${f} is used inside the guard`);
});

test("(b) the scroll reveal is progressive: CSS scroll timelines only where supported", () => {
  const css = read("styles/home.css");
  const inside = mediaBlocks(css, /prefers-reduced-motion:\s*no-preference/).map((b) => b.body).join("\n");
  assert.match(inside, /@supports\s*\(animation-timeline:\s*view\(\)\)[\s\S]*animation-timeline:\s*view\(\)/);
  // the reveal never fades content out: opacity is not part of it
  const reveal = css.match(/@keyframes\s+lift-rise\s*\{[\s\S]*?\}\s*\}/)?.[0] || "";
  assert.ok(reveal, "lift-rise keyframes found");
  assert.doesNotMatch(reveal, /opacity/, "the scroll reveal moves and sharpens, it never hides");
});

test("(c) hero content and the framed download panel are in the HTML without JavaScript", () => {
  const hero = heroSection();
  // The hook is an attribute, not a modifier class: class="install" is a
  // locator other homepage tests rely on, and it stays exactly as it was.
  assert.match(hero, /<div class="install" data-frame="panel"/, "the download panel carries the framed hook");
  assert.match(hero, /curl -fsSL https:\/\/rogerai\.fm\/install\.sh \| sh/);
  for (const marker of ["hero__eyebrow", "hero__title", "hero__sub", "hero__proof", 'data-frame="panel"']) {
    const tag = hero.match(new RegExp(`<[^>]*${marker.startsWith("data-") ? marker : `class="[^"]*${marker}[^"]*"`}[^>]*>`))?.[0] || "";
    assert.ok(tag, `${marker} is present`);
    assert.doesNotMatch(tag, /\b(data-reveal|hidden)\b/, `${marker} is not hidden before JS`);
  }
  const css = read("styles/home.css");
  assert.match(css, /\.install\[data-frame="panel"\]\s*\{[^}]*border:/, "the panel is framed");
  const lift = mediaBlocks(css, /prefers-reduced-motion:\s*no-preference/).map((b) => b.body).join("\n");
  assert.doesNotMatch(lift, /html\.js/, "no lift motion depends on the JS class");
});

test("(c) the mobile hero cannot be pushed wider than the viewport by its nowrap headline", () => {
  const css = read("styles/home.css");
  assert.match(css, /\.hero__grid\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)\s*;/,
    "the single column is minmax(0,1fr), not 1fr (whose min-content floor let the grid blow out)");
});
