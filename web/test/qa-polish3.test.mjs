// Polish round 3 (2026-10): the small things the post-perf QA left. Static checks on the
// built pages and the sheets; the rendered checks (where a heading wraps, label sizes at
// 390) were run with Playwright by hand at 390 / 768 / 1440.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import { createHash } from "node:crypto";
import path from "node:path";
import { fileURLToPath } from "node:url";

const WEB = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (f) => readFileSync(path.join(WEB, f), "utf8");
const css = (f) => read(path.join("src/styles", f)).replace(/\/\*[\s\S]*?\*\//g, "");
const PAGES = readdirSync(path.join(WEB, "src")).filter((f) => f.endsWith(".html")).sort();
const dist = (p) => read(path.join("dist", p));
before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// Item 1: a heading that wrapped right before its " - " left the dash at the start of the
// next line. The build glues the word before the dash to it (`<span class="nobr">word
// -</span>`), so the line can only break AFTER the dash. Text is unchanged: only markup.
const TITLE_RE = /<(h[1-6])\b[^>]*>([\s\S]*?)<\/\1>|<span class="bc-row__title">((?:[^<]|<span class="nobr">[^<]*<\/span>)*)<\/span>/g;
const textOf = (h) => h.replace(/<[^>]+>/g, "").replace(/\s+/g, " ");

test("headings: the shared no-wrap glue is one small rule in components.css", () => {
  assert.match(css("components.css"), /\.nobr \{ white-space: nowrap; \}/);
  assert.match(read("DESIGN-SYSTEM.md"), /`\.nobr`/, "documented in the component index");
});

test("headings: no heading or index title can break a line right before its spaced dash", () => {
  const bad = [];
  let glued = 0;
  for (const p of PAGES) {
    for (const m of dist(p).matchAll(TITLE_RE)) {
      const inner = m[2] ?? m[3];
      glued += (inner.match(/<span class="nobr">[^<\s]+ -<\/span>/g) || []).length;
      const rest = inner.replace(/<span class="nobr">[^<\s]+ -<\/span>/g, "");
      if (/\s-\s/.test(textOf(rest))) bad.push(`${p}: ${textOf(inner).trim()}`);
    }
  }
  assert.deepEqual(bad, []);
  assert.ok(glued >= 16, `the known titles are glued (${glued})`);
});

test("headings: the glue never changes a heading's words", () => {
  for (const p of ["broadcasts.html", "broadcasts-run-a-tower.html", "manual.html", "tos.html"]) {
    const src = read(path.join("src", p));
    for (const m of dist(p).matchAll(TITLE_RE)) {
      const t = textOf(m[2] ?? m[3]).trim();
      if (/ - /.test(t)) assert.ok(textOf(src).includes(t.split(" - ")[0].split(" ").pop() + " - "), `${p}: ${t}`);
    }
  }
});

// Item 2: the fonts were the one self-hosted asset without a versioned URL, so the edge's
// long-cache rule for versioned assets (cf-edge.mjs: a `v=` query on a woff2) never applied
// and every visit revalidated them. The build now stamps the font URLs in the stylesheets
// with the font's content hash, the preload in <head> with the same one (a mismatch would
// download the face twice), and versions a stylesheet by its BUILT bytes, so a new font
// also gives base.css a new URL.
const sha8 = (buf) => createHash("sha256").update(buf).digest("hex").slice(0, 8);
const srcHash = (rel) => sha8(readFileSync(path.join(WEB, "src", rel)));

test("fonts: every @font-face url in the built stylesheet carries the font's content hash", () => {
  const built = read("dist/styles/base.css");
  const urls = [...built.matchAll(/url\("?\.\.\/(assets\/fonts\/[^")?]+\.woff2)(\?v=[0-9a-f]{8})?"?\)/g)];
  assert.ok(urls.length >= 9, `the nine faces (${urls.length})`);
  for (const [, rel, v] of urls) assert.equal(v, `?v=${srcHash(rel)}`, rel);
});

test("fonts: every local url() in a built stylesheet is versioned, like the page's own asset urls", () => {
  const bad = [];
  for (const f of readdirSync(path.join(WEB, "dist/styles")).filter((f) => f.endsWith(".css"))) {
    for (const m of read(path.join("dist/styles", f)).matchAll(/url\("?\.\.\/(assets\/[^")?]+?)(\?v=([0-9a-f]{8}))?"?\)/g)) {
      if (m[3] !== srcHash(m[1])) bad.push(`${f}: ${m[0]}`);
    }
  }
  assert.deepEqual(bad, []);
});

test("fonts: the preload is byte-for-byte the url the stylesheet asks for (no double download)", () => {
  const css = read("dist/styles/base.css");
  const fromCss = css.match(/url\("?\.\.\/(assets\/fonts\/space-grotesk-latin\.woff2[^")]*)"?\)/)[1];
  for (const p of PAGES) {
    const pre = dist(p).match(/<link rel="preload" href="([^"]+)" as="font"/);
    if (!pre) continue;
    assert.equal(new URL(pre[1], "https://x/").href, new URL(`../${fromCss}`, "https://x/styles/base.css").href, p);
    assert.match(pre[1], /\?v=[0-9a-f]{8}$/, `${p}: the preload is versioned`);
  }
});

test("fonts: a stylesheet's ?v= is the hash of the BUILT sheet, so a new font gives it a new url", () => {
  const html = dist("index.html");
  for (const m of html.matchAll(/href="(styles\/[^"?]+\.css)\?v=([0-9a-f]{8})"/g)) {
    assert.equal(m[2], sha8(readFileSync(path.join(WEB, "dist", m[1]))), m[1]);
  }
});

// Item 3: the App page's screenshots shipped at one size (780px phones, 1200-1280px desktop
// shots) to every screen. Each now has a smaller copy derived from it (scripts/derive-webp.mjs,
// APP_SHOTS: phones at 400w, desktop shots at 640w) and a srcset/sizes, so a 1x desktop or a
// 2x phone takes the small one. The hero pair stays eager; everything below it stays lazy.
test("app: every screenshot has a smaller derived copy, the master's shape, fewer bytes", async () => {
  const { APP_DIR, APP_SHOTS, appNarrow, webpSize } = await import("../scripts/derive-webp.mjs");
  assert.ok(APP_SHOTS.length >= 20, `the 20 screenshots (${APP_SHOTS.length})`);
  for (const name of APP_SHOTS) {
    const m = webpSize(path.join(APP_DIR, `${name}.webp`));
    const n = appNarrow(m.w);
    const f = path.join(APP_DIR, `${name}-${n}.webp`);
    const d = webpSize(f);
    assert.equal(d.w, n, `${name}-${n}.webp is ${n} wide`);
    assert.ok(Math.abs(d.h / d.w - m.h / m.w) < 0.01, `${name}: keeps the shape`);
    assert.ok(readFileSync(f).length < readFileSync(path.join(APP_DIR, `${name}.webp`)).length, `${name}: smaller`);
  }
});

test("app: every screenshot <img> offers the small copy through srcset + sizes", async () => {
  const { APP_DIR, APP_SHOTS, appNarrow, webpSize } = await import("../scripts/derive-webp.mjs");
  const html = read("src/app.html");
  const tags = [...html.matchAll(/<img\b[^>]*src="assets\/app\/([\w-]+)\.webp"[^>]*>/g)];
  assert.ok(tags.length >= 22, `every screenshot on the page (${tags.length})`);
  for (const [tag, name] of tags) {
    assert.ok(APP_SHOTS.includes(name), `${name} is in APP_SHOTS`);
    const { w } = webpSize(path.join(APP_DIR, `${name}.webp`));
    const n = appNarrow(w);
    assert.match(tag, new RegExp(`srcset="assets/app/${name}-${n}\\.webp ${n}w, assets/app/${name}\\.webp ${w}w"`), name);
    assert.match(tag, /sizes="\(max-width: \d+px\) \d+vw, [^"]*\d+px"/, `${name}: sizes`);
    assert.match(tag, new RegExp(`width="${w}"`), `${name}: width is the master's`);
  }
});

// Item 4: the last SVG words under 11px at 390. The two diagrams (Hardware ladder, Research
// scope) joined the shared diagram scroll box (test/qa-polish2.test.mjs). The rest are
// illustrations, where scrolling makes no sense, so their words grow inside the drawing:
//  - the App hero's REAL APP stamp (160 x 44 units, 11-unit type) was shrunk to 120px on
//    a tablet or phone, 8.3px type; it now keeps its drawn size everywhere (it clears the
//    FIG. caption and still sits over the handheld's corner at 390);
//  - the Playbox cassette (320 units drawn at ~250px on a phone, 0.78): its sub line and
//    side letter are 10-11 units, 7.8px; on a phone they set at 14.5 units (11.3px), the
//    sub line without tracking so the longest one ("pick a cassette from the shelf", 30
//    mono characters) still fits the 272-unit label;
//  - the Wave family mark's ROGERAI.FM ident, 11 units at 0.97 (10.7px): 12 units.
const phoneBlock = (sheet) => [...css(sheet).matchAll(/@media \(max-width: (\d+)px\) \{((?:[^{}]*\{[^}]*\})*[^{}]*)\}/g)]
  .filter((m) => Number(m[1]) <= 640 && Number(m[1]) >= 390).map((m) => m[2]).join("\n");

test("labels: the App stamp keeps its drawn size (11-unit type = 11px) at every width", () => {
  const sheet = css("app.css");
  assert.doesNotMatch(sheet, /\.app-stamp \{[^}]*width: (?!160px)\d+px/, "no rule shrinks the stamp");
  assert.match(read("src/app.html"), /<svg class="app-stamp" viewBox="0 0 160 44" width="160" height="44"/);
  assert.match(sheet, /\.app-stamp \{[^}]*font-size: 11px/);
});

test("labels: the Playbox cassette's small words set at >= 14.5 units on a phone (>= 11px)", () => {
  const phone = phoneBlock("playbox.css");
  for (const cls of ["dk-c__sub", "dk-c__side"]) {
    const m = phone.match(new RegExp(`\\.${cls}[^{]*\\{[^}]*font-size: ([\\d.]+)px`));
    assert.ok(m && Number(m[1]) >= 14.5, `${cls} on a phone: ${m && m[1]}`);
  }
  assert.match(phone, /\.dk-c__sub[^{]*\{[^}]*letter-spacing: 0/, "the sub line fits its label untracked");
  // the longest sub line, 30 mono characters at 0.6em, fits the 272-unit label
  const longest = Math.max(...[...read("src/js/playbox.js").matchAll(/"([^"]+)"/g)]
    .filter((m) => /pick a cassette|on air via|certified contracts/.test(m[1])).map((m) => m[1].length));
  assert.ok(longest * 0.6 * 14.5 <= 272, `${longest} characters fit`);
});

test("labels: the Wave family mark's station ident is 12 units (11.6px at 390)", () => {
  assert.match(read("src/js/wave-mark-spectrum.js"), /ident\.setAttribute\("style",\s*"font-family: var\(--font-mono\); font-size: 12px;/);
});
