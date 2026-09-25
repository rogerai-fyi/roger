// The designer QA pass (2026-09): each finding fixed in the system, held here so it
// cannot quietly come back. Static checks on the built sheets; the pages themselves
// were checked by eye at 1440 and 390, light and dark (the qa report).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const WEB = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const css = (f) => readFileSync(path.join(WEB, "src/styles", f), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
const phone = (sheet) => [...sheet.matchAll(/@media \(max-width: 560px\) \{([\s\S]*?)\n\}/g)].map((m) => m[1]).join("\n");

test("a hero eyebrow wraps whole parts (no side-by-side columns); on a phone the separator is the line break", () => {
  const base = css("base.css");
  assert.match(base, /\.hero__eyebrow \{[^}]*flex-wrap: wrap/);
  assert.match(phone(base), /\.hero__eyebrow \.hero__sep \{[^}]*flex-basis: 100%[^}]*height: 0/);
});

test("the slim wave mark draws its resting waves on any page (Research loads no wave-family.css)", () => {
  const base = css("base.css");
  for (const w of ["wide", "live", "h2", "h3", "h4"]) {
    assert.match(base, new RegExp(`\\.wave-mark--slim \\.wave-mark__wave--${w} \\{[^}]*stroke: var\\(`), w);
  }
});

test("research cards keep the tinted panel ground (no page rule paints them paper)", () => {
  assert.doesNotMatch(css("research.css"), /\.deployment-grid article \{[^}]*background: var\(--paper\)/);
});

test("research on a phone: the four-card contract is one column and the group head stacks", () => {
  const p = phone(css("research.css"));
  assert.match(p, /\.device-contract:has\(> p:nth-child\(4\):last-child\)[^{]*\{[^}]*grid-template-columns: 1fr/);
  assert.match(p, /\.model-group-head \{[^}]*flex-direction: column/);
});

test("two inset bands in a row keep a gap between them", () => {
  assert.match(css("components.css"), /\.band--inset \+ \.band--inset \{[^}]*margin-top: var\(--s-/);
});
