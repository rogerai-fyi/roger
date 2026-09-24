// Printing a page. Reveals are scroll-linked (the scroll lift) or settled by site.js, so a
// page printed from the top used to come out blurred and offset wherever the reader had not
// scrolled. The shared layer's print section makes every page print whole, still and
// legible: no motion, every reveal at rest and full opacity, black on white in both themes
// and inside ink panels, the site chrome and the interactive controls left off, long code
// and tables unclipped, and an external link's address written after it.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const css = (f) => readFileSync(path.join(WEB, "src/styles", f), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
// the body of every @media print block in a sheet
function printBlocks(sheet) {
  const out = [];
  for (const m of sheet.matchAll(/@media print\s*\{/g)) {
    let d = 1, j = m.index + m[0].length;
    while (j < sheet.length && d) { if (sheet[j] === "{") d++; else if (sheet[j] === "}") d--; j++; }
    out.push(sheet.slice(m.index + m[0].length, j - 1));
  }
  return out.join("\n");
}
const rule = (block, re) => [...block.matchAll(/([^{}]+)\{([^{}]*)\}/g)].filter((m) => re.test(m[1])).map((m) => m[2]).join(";");

const shared = printBlocks(css("components.css"));

test("print lives in the shared layer, so every page prints the same way", () => {
  assert.ok(shared.length > 0, "components.css carries an @media print section");
});

test("print: nothing moves, every reveal is at rest and fully visible", () => {
  const still = rule(shared, /\*/);
  assert.match(still, /animation:\s*none\s*!important/);
  assert.match(still, /transition:\s*none\s*!important/);
  const reveal = rule(shared, /\[data-reveal\]/);
  assert.match(reveal, /opacity:\s*1\s*!important/);
  assert.match(reveal, /transform:\s*none\s*!important/);
  assert.match(reveal, /filter:\s*none\s*!important/);
  // the lift's own selector and the JS-settled one are both covered
  const sels = [...shared.matchAll(/([^{}]+)\{[^{}]*opacity:\s*1\s*!important/g)].map((m) => m[1]).join(",");
  for (const s of ["[data-reveal]", "[data-lift] [data-reveal]", "html.js [data-reveal]"]) {
    assert.ok(sels.includes(s), `${s} is reset for print`);
  }
});

test("print: words painted with a gradient print as ink (a printer drops the background they are cut from)", () => {
  const carrier = rule(shared, /\.carrier/);
  assert.match(carrier, /background:\s*none\s*!important/);
  assert.match(carrier, /color:\s*inherit\s*!important/);
  // every sheet that clips text to a gradient has its class listed here
  const sel = [...shared.matchAll(/([^{}]+)\{[^{}]*color:\s*inherit\s*!important/g)].map((m) => m[1]).join(",");
  for (const f of ["base.css", "wave-family.css", "home.css", "components.css"]) {
    const src = css(f);
    for (const m of src.matchAll(/([^{}]+)\{[^{}]*background-clip:\s*text/g)) {
      const cls = m[1].trim().split(/\s+/).pop().match(/\.[\w-]+/)?.[0];
      assert.ok(cls && sel.includes(cls), `${f}: ${m[1].trim()} clips text to a gradient; add ${cls} to the print rule`);
    }
  }
});

test("print: black on white in both themes and inside ink panels", () => {
  const tokens = printBlocks(css("tokens.css"));
  for (const scope of [":root", ':root[data-theme="dark"]', ".tone-zone", ':root[data-theme="dark"] .tone-zone']) {
    assert.ok(tokens.includes(scope), `the print tokens cover ${scope}`);
  }
  assert.match(tokens, /--paper:\s*#FFFFFF/i, "white paper");
  assert.match(tokens, /--ink-900:\s*#000000/i, "black ink");
});

test("print: the site chrome and the interactive controls are left off the page", () => {
  const hidden = [...shared.matchAll(/([^{}]+)\{[^{}]*display:\s*none\s*!important/g)].map((m) => m[1]).join(",");
  for (const s of [".nav", ".promo", ".rail", ".toc-tuner", ".scrub", ".range-twin", ".toast", ".lt-modal", ".lt-scrim", ".code-block__copy"]) {
    assert.ok(hidden.includes(s), `${s} does not print`);
  }
});

test("print: long code and wide tables print whole, and blocks do not split across pages", () => {
  assert.match(rule(shared, /pre|\.scroll-box/), /overflow:\s*visible/);
  assert.match(rule(shared, /pre/), /white-space:\s*pre-wrap/);
  assert.match(rule(shared, /\.figure|\.tint-panel/), /break-inside:\s*avoid/);
});

test("print: an external link in prose prints its address, internal links do not", () => {
  const link = [...shared.matchAll(/([^{}]+)\{([^{}]*)\}/g)].find((m) => /::after/.test(m[1]) && /attr\(href\)/.test(m[2]));
  assert.ok(link, "a rule prints attr(href)");
  assert.match(link[1], /a\[href\^="http"\]/, "only absolute (external) links");
});
