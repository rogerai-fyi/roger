// Accessibility guards: pin the fixes of the 2026-09 performance + accessibility pass.
//
// The full check is a browser sweep with axe-core over every page, light and dark, at 1440
// and 390 (scripts/a11y-sweep.py; axe is not a dependency of this tree, so it runs by
// hand). This file is the static twin: it holds the markup, CSS and script contracts that
// took the sweep's serious violations away, plus the structural rules any page must keep.
//
//   1. Text never uses the decoration ink (the running-head rev label read 1.75:1).
//   2. A link inside running text is marked by more than its colour, wherever the text is.
//   3. A widget's controls are never hidden from assistive tech by an ancestor's role
//      (the homepage console was role="img" around nine buttons; the Hardware ladder's
//      stops become buttons inside a role="img" svg) or by aria-hidden.
//   4. A box that scrolls sideways can be scrolled from the keyboard.
//   5. Headings never skip a level going down; every image has an alt; every page has
//      a language and shows one <main>.
//   6. Small stacked link lists leave room between targets (24px, WCAG 2.2 target size).
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const DIST = path.join(WEB, "dist");
const PAGES = readdirSync(SRC).filter((f) => f.endsWith(".html")).sort();
const page = (n) => readFileSync(path.join(DIST, n), "utf8");
const css = (n) => readFileSync(path.join(SRC, "styles", n), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
const js = (n) => readFileSync(path.join(SRC, "js", n), "utf8");
const noComments = (h) => h.replace(/<!--[\s\S]*?-->/g, "");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

/* ---- 1. text ink --------------------------------------------------------------------- */
test("the running-head labels are set in a text ink, not the decoration ink", () => {
  const rule = (css("base.css").match(/(?:^|\n)\.rail__rev\s*{[^}]*}/) || [""])[0];
  assert.ok(rule, "the rev rule exists");
  assert.doesNotMatch(rule, /color:\s*var\(--ink-300\)/, ".rail__rev reads 1.75:1 in --ink-300; the shared rail rule gives it --ink-400");
});

/* ---- 2. links in running text -------------------------------------------------------- */
test("a link set straight in an article's sign-off text wears the in-prose mark (.tlink)", () => {
  // base.css gives the house hairline to a classless <a> directly in a <p> or <li>; the
  // sign-off panels set some sentences straight in the <aside>, where a bare link read
  // 1.41:1 against its sentence with no other mark. .tlink is the documented opt-in.
  const bad = [];
  for (const p of PAGES.filter((f) => f.startsWith("broadcasts-"))) {
    for (const m of noComments(page(p)).matchAll(/<aside class="[^"]*bc-signoff[^"]*">([\s\S]*?)<\/aside>/g)) {
      const loose = m[1].replace(/<(p|li|div)\b[\s\S]*?<\/\1>/g, "");
      for (const a of loose.matchAll(/<a\b[^>]*>/g)) if (!/\sclass="/.test(a[0])) bad.push(`${p}: ${a[0]}`);
    }
  }
  assert.deepEqual(bad, []);
});

test("the Voices quiet line's link is marked at rest, not only on hover", () => {
  const rule = (css("voices.css").match(/\.voice-quiet a\s*{[^}]*}/) || [""])[0];
  assert.match(rule, /border-bottom:/);
  assert.doesNotMatch(rule, /border-bottom:\s*\S+\s+solid\s+transparent\s*;/, "a transparent rule is no mark at all");
});

/* ---- 3. controls reachable ------------------------------------------------------------ */
test("the homepage console is a named group, so its demo buttons reach assistive tech", () => {
  const h = noComments(page("index.html"));
  const term = (h.match(/<div class="term"[^>]*id="term"[^>]*>/) || [""])[0];
  assert.ok(term, "the console");
  assert.match(term, /role="group"/);
  assert.match(term, /aria-label="[^"]{40,}"/, "it keeps its description as the group name");
  const transport = (h.match(/<div class="term__transport"[^>]*>/) || [""])[0];
  assert.ok(transport, "the transport row");
  assert.doesNotMatch(transport, /aria-hidden/, "the play and replay buttons are real controls");
  assert.match(h, /<span class="term__bar-track" aria-hidden="true">/, "the progress bar is decoration");
});

test("the Hardware ladder becomes a group when its stops become buttons", () => {
  const src = js("hardware.js");
  assert.match(src, /setAttribute\("role", "button"\)/, "the stops are buttons");
  assert.match(src, /ladder\.setAttribute\("role", "group"\)/, "so the svg around them is no longer an image");
});

/* ---- 4. keyboard scrolling ----------------------------------------------------------- */
test("a box that scrolls sideways takes keyboard focus (site.js), with a visible ring", () => {
  const s = js("site.js");
  assert.match(s, /data-kbd-scroll/, "site.js marks the boxes it makes focusable");
  assert.match(s, /\.scroll-box/, "scroll boxes");
  assert.match(s, /\bpre\b/, "command blocks");
  assert.match(s, /aria-hidden/, "never inside something hidden from assistive tech");
  assert.match(s, /addEventListener\("resize"/, "rechecked when the width changes");
  assert.match(css("components.css"), /\[data-kbd-scroll\]:focus-visible\s*{[^}]*outline:\s*2px solid var\(--live\)/);
});

/* ---- 5. structure --------------------------------------------------------------------- */
test("headings never skip a level going down, inside <main>", () => {
  const bad = [];
  for (const p of PAGES) {
    const h = noComments(page(p)).replace(/<(script|style|svg)\b[\s\S]*?<\/\1>/g, "");
    const main = [...h.matchAll(/<main\b[\s\S]*?<\/main>/g)].map((m) => m[0]).join("");
    let prev = 0;
    for (const m of main.matchAll(/<h([1-6])\b/g)) {
      const n = Number(m[1]);
      if (prev && n > prev + 1) bad.push(`${p}: h${prev} -> h${n}`);
      prev = n;
    }
  }
  assert.deepEqual([...new Set(bad)], []);
});

test("every image has an alt attribute", () => {
  const bad = [];
  for (const p of PAGES) for (const m of noComments(page(p)).matchAll(/<img\b[^>]*>/g))
    if (!/\salt="/.test(m[0])) bad.push(`${p}: ${m[0].slice(0, 80)}`);
  assert.deepEqual(bad, []);
});

test("every page declares its language and shows exactly one <main>", () => {
  for (const p of PAGES) {
    const h = noComments(page(p));
    assert.match(h, /<html[^>]*\slang="en"/, p);
    if (/http-equiv="refresh"/i.test(h) && !/<main\b/.test(h)) continue;   // a bare redirect stub
    // HTML allows several <main> when all but one are hidden: keys and usage ship a card
    // and a sign-in gate, both [hidden], and the script shows exactly one
    const mains = h.match(/<main\b[^>]*>/g) || [];
    assert.ok(mains.length >= 1, `${p}: has a <main>`);
    assert.ok(mains.filter((m) => !/\shidden[\s>]/.test(m)).length <= 1, `${p}: at most one visible <main>`);
    if (mains.length > 1) assert.ok(mains.every((m) => /\shidden[\s>]/.test(m)), `${p}: alternates are all hidden`);
  }
});

/* ---- 6. target spacing ---------------------------------------------------------------- */
test("the governance article's source list spaces its links at least 24px apart", () => {
  const c = css("broadcast-agent-governance.css");
  assert.match(c, /\.gov-sources li \+ li\s*{\s*margin-top:\s*var\(--s-2\);?\s*}/);
});

/* ---- 7. focus you can see ------------------------------------------------------------- */
test("the broadcast video frame rings when its player has focus", () => {
  // the player fills a clipped frame (overflow: hidden), so its own focus outline is cut
  // off; the Tab sweep found a focused player with nothing to show for it. focus-within,
  // because focus inside the native controls sits in the player's shadow tree
  assert.match(css("broadcasts.css"), /\.bc-video__frame:focus-within\s*{[^}]*outline:\s*2px solid var\(--live\)/);
});
