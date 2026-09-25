// Post-launch polish (2026-09-24): the follow-ups the launch QA report left open.
// Static checks on sources and sheets; the rendered checks (label size, tap targets)
// live in scripts/overflow-sweep.py's companions and were run at 390 by hand.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
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
