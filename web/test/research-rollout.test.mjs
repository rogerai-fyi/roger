// Research rollout (site-lift, 2026-09): the research hub, the program pages, the
// broadcasts index and every broadcast move onto the shared design system. A
// PRESENTATION-only pass, so these tests pin its promises:
//   (a) the words did not change: each page's visible <main> text and its links are the
//       pre-rollout copy (fixtures/research-words.json, captured before the pass). Only
//       whitespace is free. A deliberate copy change must refresh that fixture in the same
//       commit (node test/research-rollout.test.mjs --update is NOT offered on purpose:
//       regenerate it by hand and say why). The in-page contents tuner is excluded from
//       the text because it only repeats section labels the page already shows (pinned
//       separately below).
//   (b) the article figure, code block, scroll box and inset panel are shared components
//       (components.css), not article-only styles.
//   (c) no article carries a hard-coded colour or an inline style.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const dist = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const src = (p) => readFileSync(path.join(SRC, p), "utf8");
const css = (p) => src(`styles/${p}`).replace(/\/\*[\s\S]*?\*\//g, "");
export const PAGES = readdirSync(SRC).filter((f) => /^(broadcasts|research)[\w-]*\.html$/.test(f)).sort();
const ARTICLES = PAGES.filter((f) => f.startsWith("broadcasts-"));
const fixture = JSON.parse(readFileSync(path.join(WEB, "test/fixtures/research-words.json"), "utf8"));

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// The reader's words: <main> without comments, scripts, styles, the contents tuner and
// the install pill's decorative "$" prompt. Entities are left as written.
export function mainOf(html) {
  return (html.match(/<main\b[\s\S]*<\/main>/) || [""])[0]
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/<script\b[\s\S]*?<\/script>/g, "")
    .replace(/<style\b[\s\S]*?<\/style>/g, "")
    .replace(/<nav class="toc-tuner"[\s\S]*?<\/nav>/g, "");
}
export function wordsOf(html) {
  const text = mainOf(html)
    .replace(/<span class="install__prompt"[^>]*>\$<\/span>/g, "")
    .replace(/<[^>]+>/g, " ").replace(/\s+/g, "");
  return { sha: createHash("sha256").update(text).digest("hex"), length: text.length,
    hrefs: [...mainOf(html).matchAll(/href="([^"]*)"/g)].map((m) => m[1]) };
}

test("(a) every research-section page keeps its words and links (whitespace aside)", () => {
  assert.deepEqual(Object.keys(fixture).sort(), PAGES, "the fixture covers every research-section page");
  for (const page of PAGES) {
    const now = wordsOf(dist(page));
    assert.deepEqual(now.hrefs, fixture[page].hrefs, `${page}: the links changed`);
    assert.equal(now.length, fixture[page].length, `${page}: the visible text changed length`);
    assert.equal(now.sha, fixture[page].sha, `${page}: the visible text changed`);
  }
});

test("(a) a contents tuner only repeats labels its page already shows, and points at real sections", () => {
  for (const page of PAGES) {
    const html = dist(page);
    const tuner = html.match(/<nav class="toc-tuner"[\s\S]*?<\/nav>/)?.[0];
    if (!tuner) continue;
    const body = mainOf(html).replace(/<[^>]+>/g, " ").replace(/&[#\w]+;/g, " ").replace(/\s+/g, " ").toUpperCase();
    for (const [, href, no, name] of tuner.matchAll(/<a class="toc-tuner__st" href="#([\w-]+)"><b>([^<]*)<\/b><span class="toc-tuner__name">([^<]*)<\/span>/g)) {
      assert.match(html, new RegExp(`id="${href}"`), `${page}: the tuner station #${href} exists`);
      const label = name.replace(/&[#\w]+;/g, " ").replace(/\s+/g, " ").trim().toUpperCase();
      assert.ok(body.includes(label), `${page}: the station name "${name}" is a label the page already shows`);
    }
  }
});

/* ---- (b) the shared components ------------------------------------------------------ */

const componentBlocks = () => new Set([...css("components.css").matchAll(/(^|[},\s])\.([a-z][\w-]*)/g)].map((m) => m[2].replace(/(__|--).*/, "")));

test("(b) figure, scroll box, code block and inset panel are components.css components", () => {
  const blocks = componentBlocks();
  for (const b of ["figure", "scroll-box", "code-block", "inset-panel"]) assert.ok(blocks.has(b), `components.css defines .${b}`);
  const c = css("components.css");
  assert.match(c, /\.figure\s*\{[^}]*max-width:\s*100%/, "a figure never outgrows its column");
  assert.match(c, /\.figure > img, \.figure > video, \.figure > svg\s*\{[^}]*max-width:\s*100%[^}]*height:\s*auto/, "nor does its media");
  assert.match(c, /\.scroll-box\s*\{[^}]*overflow-x:\s*auto/, "a scroll box scrolls in itself");
  assert.match(c, /\.code-block pre\s*\{[^}]*overflow-x:\s*auto/, "a long code line scrolls inside the block");
  assert.match(c, /\.code-block__copy\.is-copied/, "the code copy button shows the copy tick");
  const doc = src("../DESIGN-SYSTEM.md");
  for (const h of ["### Figure", "### Code block", "### Scroll box", "### Inset panel"]) assert.ok(doc.includes(h), `DESIGN-SYSTEM.md documents ${h}`);
});

test("(b) the article sheets no longer define figures or code blocks of their own", () => {
  const b = css("broadcasts.css");
  assert.doesNotMatch(b, /\.bc-figure|\.bc-scroll|(^|[\s,}])pre\b/m, "broadcasts.css: figures and code blocks are components now");
  for (const page of PAGES) assert.doesNotMatch(src(page), /\bbc-(figure|scroll|code)\b/, `${page}: migrated to .figure / .scroll-box / .code-block`);
});

test("(b) every code block on a research-section page is a .code-block with the shared copy tick", () => {
  let n = 0;
  for (const page of PAGES) {
    const html = mainOf(dist(page));
    const pres = html.match(/<pre\b/g) || [];
    const wrapped = html.match(/<div class="code-block">\s*<pre\b/g) || [];
    assert.equal(wrapped.length, pres.length, `${page}: every <pre> sits in a .code-block`);
    n += pres.length;
  }
  assert.ok(n >= 5, `found ${n} code blocks`);
  const site = src("js/site.js");
  assert.match(site, /querySelectorAll\("\.code-block"\)/, "site.js gives every code block a copy control");
  assert.match(site, /code-block__copy/, "...the .code-block__copy button");
  assert.match(site, /data-copy-target/, "...which uses the shared [data-copy-target] copy tick");
});

/* ---- (c) articles: tokens, no inline styles ----------------------------------------- */

test("(c) no article carries an inline style or a colour literal", () => {
  const COLOR = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?)\(/;
  for (const page of ARTICLES) {
    const s = src(page).replace(/<!--[\s\S]*?-->/g, "").replace(/&#\w+;/g, "");
    assert.doesNotMatch(s, /\sstyle="/, `${page}: an inline style; use a class`);
    for (const block of s.match(/<style\b[\s\S]*?<\/style>/g) || []) assert.doesNotMatch(block, COLOR, `${page}: a colour literal in an SVG style; use a token`);
  }
});

test("(c) chart text stays readable: no SVG text class set below 0.7 opacity", () => {
  for (const page of ARTICLES) {
    for (const block of src(page).match(/<style\b[\s\S]*?<\/style>/g) || []) {
      for (const [rule] of block.matchAll(/\.[\w-]+\{[^}]*font-family[^}]*\}/g)) {
        const op = rule.match(/(?:^|[;{])opacity:\s*([\d.]+)/);
        if (op) assert.ok(Number(op[1]) >= 0.7, `${page}: "${rule.slice(0, 60)}" fades chart text below AA`);
      }
    }
  }
});

test("(c) the Quick Answer is set in the prose face as .bc-answer", () => {
  for (const page of ARTICLES) {
    const html = src(page);
    if (!/<b>Quick answer\.<\/b>/.test(html)) continue;
    assert.match(html, /<blockquote class="bc-answer"[^>]*>\s*<b>Quick answer\.<\/b>/, `${page}: the Quick Answer is a .bc-answer`);
  }
});

test("(c) every article sign-off is an inset panel; a closing install command is the shared pill", () => {
  let panels = 0, pills = 0;
  for (const page of ARTICLES) {
    const html = src(page);
    assert.doesNotMatch(html, /man-note--live/, `${page}: the pink sign-off box is gone`);
    panels += (html.match(/<aside class="inset-panel bc-signoff"/g) || []).length;
    pills += (html.match(/include: install-box\.html id=signoffInstall/g) || []).length;
  }
  assert.ok(panels >= 14, `${panels} articles sign off in an inset panel`);
  assert.ok(pills >= 9, `the install command is the shared pill in ${pills} sign-offs`);
});

