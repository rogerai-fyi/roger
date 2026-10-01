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
