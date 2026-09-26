// Performance guards: pin the wins of the 2026-09 performance + accessibility pass.
//
// The measuring is done in a browser (a cold load at 390px with a 4x CPU throttle and a
// 1.6 Mbps / 150 ms network; see scripts/a11y-sweep.py for the accessibility half). This
// tree runs dependency-free under `node --test`, so this file is the static twin: it reads
// the built pages and holds the markup and asset rules that made the numbers move.
//
//   1. Images: every <img> declares width + height (no layout shift) and a loading policy;
//      the lead image of a page is never lazy (it is the LCP) and asks for high priority;
//      nothing below it loads eagerly; no image a page shows weighs more than 300 KB.
//   2. The article illustrations ship as WebP at two widths, derived from the PNG masters
//      by scripts/derive-webp.mjs, and keep the master's shape.
//   3. Fonts are served from this origin (no render-blocking third-party stylesheet), with
//      font-display: swap, and the text face's latin file is preloaded.
//   4. No parser-blocking script except the no-flash theme setter in <head> (tiny); the
//      two legacy-address redirect stubs are exempt (leaving at once is their job).
//   5. Video: nothing downloads a film before it is asked for.
//   6. Per-page weight budgets for the first load (fixtures/page-weight-budget.json).
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { MASTERS, WEBP_DIR, WIDE, NARROW, pngSize } from "../scripts/derive-webp.mjs";
import { noComments, attr, local, bytes, candidates, images, firstLoad, BUDGET_FILE } from "../scripts/page-weight.mjs";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = path.join(WEB, "src");
const DIST = path.join(WEB, "dist");
const PAGES = readdirSync(SRC).filter((f) => f.endsWith(".html")).sort();
const page = (name) => readFileSync(path.join(DIST, name), "utf8");
// legacy-address stubs (bands, playground): a meta refresh whose only job is to leave at
// once, so a synchronous redirect script is right and they paint no text worth a font
const isStub = (name) => /<meta[^>]+http-equiv="refresh"/i.test(page(name));
before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// the first <h2> inside <main>: everything before it is the page's opening screen
function firstH2(html) {
  const h = noComments(html);
  const main = h.search(/<main\b/);
  const i = h.slice(main).search(/<h2\b/);
  return i < 0 ? Infinity : main + i;
}

/* ---- 1. images -------------------------------------------------------------------- */
test("every <img> declares width and height (reserves its box, no layout shift)", () => {
  const bad = [];
  for (const p of PAGES) for (const { tag } of images(page(p)))
    if (!attr(tag, "width") || !attr(tag, "height")) bad.push(`${p}: ${tag.slice(0, 90)}`);
  assert.deepEqual(bad, []);
});

test("every <img> states a loading policy: lazy below the opening screen, never lazy in it", () => {
  const bad = [];
  for (const p of PAGES) {
    const html = page(p), fold = firstH2(html);
    for (const { tag, at, standin } of images(html)) {
      if (standin) continue;
      const lazy = attr(tag, "loading") === "lazy";
      if (at < fold && lazy) bad.push(`${p}: opening-screen image is lazy (it is the LCP): ${attr(tag, "src")}`);
      if (at > fold && !lazy) bad.push(`${p}: below-the-fold image loads eagerly: ${attr(tag, "src")}`);
    }
  }
  assert.deepEqual(bad, []);
});

test("an article's lead image asks for high fetch priority", () => {
  const bad = [];
  for (const p of PAGES.filter((f) => f.startsWith("broadcasts-"))) {
    const html = page(p), fold = firstH2(html);
    const lead = images(html).find(({ at, standin }) => at < fold && !standin);
    if (lead && attr(lead.tag, "fetchpriority") !== "high") bad.push(`${p}: ${attr(lead.tag, "src")}`);
  }
  assert.deepEqual(bad, []);
});

test("no image a page shows weighs more than 300 KB (any srcset candidate, any poster)", () => {
  const bad = [];
  for (const p of PAGES) {
    const html = noComments(page(p));
    const refs = images(html).flatMap(({ tag }) => candidates(tag));
    for (const m of html.matchAll(/<video\b[^>]*>/g)) refs.push(local(attr(m[0], "poster")));
    for (const r of refs.filter(Boolean)) if (bytes(r) > 300 * 1024) bad.push(`${p}: ${r} ${Math.round(bytes(r) / 1024)} KB`);
  }
  assert.deepEqual(bad, []);
});

/* ---- 2. derived WebP ---------------------------------------------------------------- */
// width x height of a WebP (VP8, VP8L or VP8X)
function webpSize(file) {
  const b = readFileSync(file);
  const kind = b.toString("ascii", 12, 16);
  if (kind === "VP8X") return { w: 1 + b.readUIntLE(24, 3), h: 1 + b.readUIntLE(27, 3) };
  if (kind === "VP8L") { const v = b.readUInt32LE(21); return { w: 1 + (v & 0x3fff), h: 1 + ((v >> 14) & 0x3fff) }; }
  return { w: b.readUInt16LE(26) & 0x3fff, h: b.readUInt16LE(28) & 0x3fff };
}

test("every PNG master has its two WebP copies, in the master's shape", () => {
  for (const name of MASTERS) {
    const m = pngSize(path.join(WEBP_DIR, `${name}.png`));
    for (const [file, w] of [[`${name}.webp`, Math.min(m.w, WIDE)], [`${name}-${NARROW}.webp`, NARROW]]) {
      const f = path.join(WEBP_DIR, file);
      assert.ok(existsSync(f), `${file} missing: node web/scripts/derive-webp.mjs ${name}`);
      const s = webpSize(f);
      assert.equal(s.w, w, `${file}: width`);
      assert.ok(Math.abs(s.h - Math.round((w * m.h) / m.w)) <= 1, `${file}: ${s.w}x${s.h} is not the master's ${m.w}x${m.h} shape`);
    }
  }
});

test("pages show the WebP copies, not the PNG masters, with a srcset and sizes", () => {
  const bad = [];
  for (const p of PAGES) for (const { tag } of images(page(p))) {
    const src = local(attr(tag, "src")) || "";
    if (/^assets\/broadcasts\/.+\.png$/.test(src)) bad.push(`${p}: ${src}`);
    if (/^assets\/broadcasts\/.+\.webp$/.test(src) && MASTERS.some((n) => src.endsWith(`/${n}.webp`))
        && !(attr(tag, "srcset") && attr(tag, "sizes"))) bad.push(`${p}: ${src} has no srcset/sizes`);
  }
  assert.deepEqual(bad, []);
});

/* ---- 3. fonts ----------------------------------------------------------------------- */
const FONT_DIR = "assets/fonts";
test("fonts come from this origin: no third-party font stylesheet on any page", () => {
  for (const p of PAGES) assert.doesNotMatch(noComments(page(p)), /fonts\.(googleapis|gstatic)\.com/, p);
});

test("every @font-face uses font-display: swap and a local woff2", () => {
  const css = readFileSync(path.join(SRC, "styles", "base.css"), "utf8");
  const faces = [...css.matchAll(/@font-face\s*{([^}]*)}/g)].map((m) => m[1]);
  assert.ok(faces.length >= 2, "the two families are declared");
  for (const f of faces) {
    assert.match(f, /font-display:\s*swap/);
    const url = (f.match(/url\("?\.\.\/(assets\/fonts\/[^")]+\.woff2)"?\)/) || [])[1];
    assert.ok(url && existsSync(path.join(SRC, url)), `a local woff2: ${f.trim().slice(0, 80)}`);
  }
  for (const fam of ["Space Grotesk", "JetBrains Mono"]) assert.ok(faces.some((f) => f.includes(`"${fam}"`)), fam);
});

test("every page preloads the text face's latin file, and only that font", () => {
  // Measured at 390 / 4x CPU / 1.6 Mbps: preloading Space Grotesk takes the font-swap
  // reflow off the headline (CLS 0.12-0.15 -> 0 on the homepage and App); preloading
  // JetBrains Mono as well added another ~120 ms to first paint for little more.
  for (const p of PAGES.filter((f) => !isStub(f))) {
    const pre = [...noComments(page(p)).matchAll(/<link\b[^>]*rel="preload"[^>]*>/g)].map((m) => m[0])
      .filter((t) => attr(t, "as") === "font");
    assert.deepEqual(pre.map((t) => local(attr(t, "href"))), [`${FONT_DIR}/space-grotesk-latin.woff2`], p);
    for (const t of pre) {
      assert.equal(attr(t, "type"), "font/woff2", `${p}: ${t}`);
      assert.match(t, /\scrossorigin[\s>=/]/, `${p}: font preload needs crossorigin or it is fetched twice`);
    }
  }
});

test("the font licences ship beside the fonts", () => {
  assert.ok(existsSync(path.join(SRC, FONT_DIR, "OFL-SpaceGrotesk.txt")));
  assert.ok(existsSync(path.join(SRC, FONT_DIR, "OFL-JetBrainsMono.txt")));
});

/* ---- 4. scripts --------------------------------------------------------------------- */
test("no parser-blocking script anywhere except the tiny no-flash theme setter in <head>", () => {
  const bad = [];
  for (const p of PAGES.filter((f) => !isStub(f))) {
    const html = noComments(page(p)), head = (html.match(/<head\b[\s\S]*?<\/head>/) || [""])[0];
    for (const m of html.matchAll(/<script\b[^>]*\bsrc="([^"]+)"[^>]*>/g)) {
      if (/\s(defer|async)[\s>=]/.test(m[0]) || /type="module"/.test(m[0])) continue;
      const rel = local(m[1]);
      if (rel === "js/theme-init.js" && bytes(rel) < 1024 && head.includes(m[0])) continue;
      bad.push(`${p}: ${m[1]}`);
    }
  }
  assert.deepEqual(bad, []);
});

/* ---- 5. video ----------------------------------------------------------------------- */
test("no <video> preloads a film: preload is none, or metadata on a muted autoplay loop", () => {
  const bad = [];
  for (const p of PAGES) for (const m of noComments(page(p)).matchAll(/<video\b[^>]*>/g)) {
    const pre = attr(m[0], "preload");
    if (pre === "none") continue;
    if (pre === "metadata" && /\sautoplay[\s>]/.test(m[0]) && /\smuted[\s>]/.test(m[0])) continue;
    bad.push(`${p}: ${m[0].slice(0, 100)}`);
  }
  assert.deepEqual(bad, []);
});

/* ---- 6. weight budgets -------------------------------------------------------------- */
// The first-load weight (scripts/page-weight.mjs: the page, its CSS and scripts, preloads,
// eager images at their largest candidate, posters, an autoplaying film; raw bytes). The
// budget is the weight after this pass + 10%; a page that grows past it sheds the weight
// or raises its budget (node web/scripts/page-weight.mjs --budget) in the same commit.
test("every page stays inside its first-load weight budget", () => {
  const budget = JSON.parse(readFileSync(BUDGET_FILE, "utf8"));
  const over = [];
  for (const p of PAGES) {
    assert.ok(budget.pages[p], `${p} has no budget: node web/scripts/page-weight.mjs --budget`);
    const w = firstLoad(p);
    if (w > budget.pages[p]) over.push(`${p}: ${Math.round(w / 1024)} KB > budget ${Math.round(budget.pages[p] / 1024)} KB`);
  }
  assert.deepEqual(over, []);
});

/* ---- 7. layout shift: the promo strip --------------------------------------------- */
// The strip ships [hidden] and promo.js (deferred) revealed it after the page had painted,
// pushing every page down by its height: the largest layout shift on the long pages
// (manual 0.17, Wave family 0.16 at 390 throttled). The synchronous theme setter already
// runs before first paint, so it now marks a not-dismissed visitor (html.promo-early) and
// the strip is laid out from the first frame. Without JS, or once dismissed, it stays hidden.
const js = (n) => readFileSync(path.join(SRC, "js", n), "utf8");
test("the promo strip is laid out before first paint for a visitor who has not dismissed it", () => {
  const init = js("theme-init.js"), promo = js("promo.js");
  const key = (promo.match(/STORE_KEY\s*=\s*"([^"]+)"/) || [])[1];
  assert.ok(key, "promo.js names its storage key");
  assert.ok(init.includes(`"${key}"`), "theme-init.js reads the same dismissal key");
  assert.match(init, /classList\.add\("promo-early"\)/, "theme-init.js marks the root");
  const base = readFileSync(path.join(SRC, "styles", "base.css"), "utf8");
  assert.match(base, /html\.promo-early \.promo\[hidden\]\s*{\s*display:\s*block;?\s*}/, "base.css lays the strip out early");
  // every place promo.js hides the strip also drops the early mark, or the CSS keeps it up
  assert.equal((promo.match(/bar\.hidden = true/g) || []).length, 1, "one place hides the strip: hide()");
  assert.ok((promo.match(/\bhide\(\)/g) || []).length >= 3, "hide() is defined and used on dismiss and on an inactive offer");
  assert.match(promo, /function hide\(\)\s*{[^}]*bar\.hidden = true;[^}]*classList\.remove\("promo-early"\)/, "hide() drops the mark");
});
