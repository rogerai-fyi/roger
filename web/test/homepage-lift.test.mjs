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
import { readFileSync, readdirSync } from "node:fs";
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
  assert.match(visibleText(hero), /curl -fsSL https:\/\/rogerai\.fm\/install\.sh \| sh/);
  for (const marker of ["hero__eyebrow", "hero__title", "hero__sub", "hero__proof", 'data-frame="panel"']) {
    const tag = hero.match(new RegExp(`<[^>]*${marker.startsWith("data-") ? marker : `class="[^"]*${marker}[^"]*"`}[^>]*>`))?.[0] || "";
    assert.ok(tag, `${marker} is present`);
    assert.doesNotMatch(tag, /\b(data-reveal|hidden)\b/, `${marker} is not hidden before JS`);
  }
  // the framed panel is a shared component (components.css); the lift motion is home.css
  assert.match(read("styles/components.css"), /\.install\[data-frame="panel"\]\s*\{[^}]*border:/, "the panel is framed");
  const css = read("styles/home.css");
  const lift = mediaBlocks(css, /prefers-reduced-motion:\s*no-preference/).map((b) => b.body).join("\n");
  assert.doesNotMatch(lift, /html\.js/, "no lift motion depends on the JS class");
});

test("(c) the mobile hero cannot be pushed wider than the viewport by its nowrap headline", () => {
  const css = read("styles/home.css");
  assert.match(css, /\.hero__grid\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)\s*;/,
    "the single column is minmax(0,1fr), not 1fr (whose min-content floor let the grid blow out)");
});

// ---- round 2 (2026-09-23): alignment, the Ping dock, dead CSS ----

// Round 3: round 2 kept the left edge but let the hero widen to 1300px on the
// right, so FIG. 1 ran ~220px past the nav at 1920. The hero now lives in the
// same content box as the nav and every section, on BOTH sides, at every width.
test("the hero sits in the page's content box on both sides at every width", () => {
  const css = read("styles/home.css");
  assert.doesNotMatch(css, /\.hero__inner\s*\{[^}]*max-width/, "no wider-than-.wrap hero box");
  assert.doesNotMatch(css, /\.hero__inner\s*\{[^}]*margin-left/, "no hand-placed hero edge");
  const hero = heroSection();
  assert.match(hero, /<div class="wrap hero__inner">/, "the hero IS a .wrap, the same box as the nav");
  // head spans the box; copy + tools share the row beneath it
  assert.match(hero, /<div class="hero__head">[\s\S]*class="hero__title"[\s\S]*<div class="hero__copy">[\s\S]*class="hero__sub"/);
  const wide = mediaBlocks(css, /min-width:\s*1100px/).map((b) => b.body).join("\n");
  assert.match(wide, /\.hero__head\s*\{[^}]*grid-column:\s*1 \/ -1/);
});

test("the headline is sized by its own box, so it can never overflow it", () => {
  const css = read("styles/home.css");
  assert.match(css, /\.hero__head\s*\{[^}]*container-type:\s*inline-size/);
  assert.match(css, /\.hero__title\s*\{[^}]*font-size:\s*min\(var\(--t-display\),\s*9\.5cqi\)/);
});

test("the Ping dock spans the panel's column: the LED band fills what the mascot leaves", () => {
  const css = read("styles/home.css");
  assert.match(css, /\.pingband\s*\{[^}]*flex:\s*1 1 auto/);
  assert.doesNotMatch(css, /\.pingband\s*\{[^}]*width:\s*clamp/, "no fixed-width band left floating beside the panel");
  assert.equal((css.match(/^\.hero__ping\s*\{/gm) || []).length, 1, "one .hero__ping rule, not a rule and a later override");
});

test("home.css carries no rules for classes the homepage never uses", () => {
  const strip = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");
  const css = strip(readFileSync(path.join(WEB, "src/styles/home.css"), "utf8"));
  const dir = (d) => readdirSync(path.join(WEB, d)).map((f) => readFileSync(path.join(WEB, d, f), "utf8")).join("\n");
  const used = readFileSync(path.join(WEB, "src/index.html"), "utf8") + dir("src/_partials") + dir("src/js");
  // classes built by string concatenation in JS ("pingdeck__line--" + who)
  const dynamic = [/^pingdeck__line--/];
  const classes = [...new Set([...css.matchAll(/\.([a-zA-Z][\w-]*)/g)].map((m) => m[1]))];
  const dead = classes.filter((c) => !used.includes(c) && !dynamic.some((re) => re.test(c)));
  assert.deepEqual(dead, [], `dead selectors in home.css: ${dead.join(", ")}`);
});

test("homepage sections let the background network through (a tint, not a solid fill)", () => {
  // Inside an ink zone the bloom and the LED field sit behind the sections, so a
  // solid band fill would cut them off at the band's edge. A tint of the local
  // ink keeps the same tone on paper and lets the zone show through.
  const css = read("styles/home.css");
  assert.match(css, /main \.band\s*\{[^}]*background:\s*color-mix\(in srgb, var\(--ink-900\) [\d.]+%, transparent\)/);
  assert.doesNotMatch(css, /\.company\s*\{[^}]*background:\s*var\(--paper\)/);
});

test("sideways choreography can never widen the page", () => {
  // variant B slides the two sides of the tune in from +-56px; before they
  // arrive, that offset overhangs the viewport, and a phone widens its layout
  // viewport to fit it (a 426px page on a 390px phone). Sections clip x.
  const css = read("styles/home.css");
  assert.match(css, /main > section\s*\{[^}]*overflow-x:\s*clip/);
});

test("the market price cell never splits its tag: the chip and the $ tier stay whole", () => {
  const css = read("styles/home.css");
  // round 11: the chip AND the "$" tier wrap together (homepage-fun.test); each stays whole
  assert.match(css, /\.mkt-cell--price \.band-tag\s*\{[^}]*white-space:\s*nowrap/);
  assert.match(css, /\.mkt-cell--price \.price-tier\s*\{[^}]*white-space:\s*nowrap/);
});

test("lean: every @keyframes in home.css is actually used", () => {
  const css = readFileSync(path.join(WEB, "src/styles/home.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const js = readdirSync(path.join(WEB, "src/js")).map((f) => readFileSync(path.join(WEB, "src/js", f), "utf8")).join("\n");
  const names = [...css.matchAll(/@keyframes\s+([\w-]+)/g)].map((m) => m[1]);
  const body = css.replace(/@keyframes[^{]*\{(?:[^{}]*\{[^}]*\})*[^}]*\}/g, "");
  const unused = names.filter((n) => !new RegExp(`animation(-name)?:[^;]*\\b${n}\\b`).test(body) && !js.includes(n));
  assert.deepEqual(unused, [], `unused keyframes: ${unused.join(", ")}`);
});
