// Sitewide text contrast, computed from tokens.css: every text ink (ink-900 down to the
// tertiary ink-400 that the shared labels use - .sectionno, eyebrows, FIG. captions,
// dates) holds WCAG AA (4.5:1) on every ground text sits on: --paper, --paper-2 and the
// raised --white, on the light site, the dark site, and inside an ink panel on each.
// Fixing it here, once, is what lets no page carry its own "labels take --ink-500" patch.
// (--ink-300 is decoration only: ticks and disabled bars, never text.)
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const tokens = readFileSync(path.join(WEB, "src/styles/tokens.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
const hex = (h) => { h = h.replace("#", ""); return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)); };
const lum = ([r, g, b]) => { const f = (c) => { c /= 255; return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b); };
const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
// the hex custom properties of the first rule whose selector list is exactly `sel`
function scope(sel) {
  for (const m of tokens.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    if (m[1].trim().replace(/\s+/g, " ") !== sel) continue;
    return Object.fromEntries([...m[2].matchAll(/(--[\w-]+):\s*(#[0-9A-Fa-f]{6})\b/g)].map((x) => [x[1], hex(x[2])]));
  }
  assert.fail(`tokens.css has no rule "${sel}"`);
}
const light = scope(":root");
const dark = { ...light, ...scope(':root[data-theme="dark"], .tone-zone') };
const zoneInks = scope(".tone-zone");
const zoneOnLight = { ...dark, ...zoneInks };
const zoneOnDark = { ...zoneOnLight, ...scope(':root[data-theme="dark"] .tone-zone') };
const CONTEXTS = { "light site": light, "dark site": dark, "ink panel, light site": zoneOnLight, "ink panel, dark site": zoneOnDark };
const INKS = ["--ink-900", "--ink-700", "--ink-500", "--ink-400"];
const GROUNDS = ["--paper", "--paper-2", "--white"];

for (const [name, t] of Object.entries(CONTEXTS)) {
  test(`${name}: every text ink is AA on every ground`, () => {
    for (const ground of GROUNDS) {
      for (const ink of INKS) {
        const r = ratio(t[ink], t[ground]);
        assert.ok(r >= 4.5, `${name}: ${ink} on ${ground} is ${r.toFixed(2)}:1`);
      }
    }
  });
}

test("the ink ladder keeps its order: each step is at least as strong as the one below", () => {
  for (const [name, t] of Object.entries(CONTEXTS)) {
    for (let i = 0; i + 1 < INKS.length; i++) {
      assert.ok(ratio(t[INKS[i]], t["--paper"]) >= ratio(t[INKS[i + 1]], t["--paper"]),
        `${name}: ${INKS[i]} reads at least as strongly as ${INKS[i + 1]}`);
    }
  }
});

test("no page carries its own contrast patch for the shared labels", () => {
  // Once the tertiary ink is AA, re-scoping it (".x { --ink-400: ... }") or lifting a
  // shared label to --ink-500 in a page sheet is dead weight that drifts. tokens.css may
  // re-scope it only for the ink panel (and the plate: its wells sit on --paper-3).
  const dir = path.join(WEB, "src/styles");
  for (const sheet of readdirSync(dir).filter((f) => f.endsWith(".css"))) {
    const css = readFileSync(path.join(dir, sheet), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
    for (const m of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
      const sel = m[1].trim().replace(/\s+/g, " ");
      if (/--ink-400\s*:/.test(m[2])) {
        // .card: the signed-in plate's command wells sit on --paper-3 (see tokens.css)
        assert.ok(sheet === "tokens.css" && [":root", ':root[data-theme="dark"], .tone-zone', ".tone-zone", ".card"].includes(sel),
          `${sheet} "${sel}" re-scopes --ink-400`);
      }
      const label = sel.split(/,(?![^(]*\))/).some((part) => /\.(sectionno|fig|hero__eyebrow|eyebrow|research-kicker)\b[^\s>+~]*$/.test(part.trim()));
      if (label && /(^|;)\s*color:\s*var\(--ink-500\)/.test(m[2])) {
        assert.fail(`${sheet} "${sel}" lifts a shared label to --ink-500; the label's own ink is AA now`);
      }
    }
  }
});
