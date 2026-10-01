// Polish round 3 (2026-10): the small things the post-perf QA left. Static checks on the
// built pages and the sheets; the rendered checks (where a heading wraps, label sizes at
// 390) were run with Playwright by hand at 390 / 768 / 1440.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
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
