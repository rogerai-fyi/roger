// Polish round 4 (2026-10): the interaction-feel pass. One keyboard focus ring sitewide,
// one motion vocabulary (the --d-N durations), and form fields whose words fit. Static
// checks on the sheets; the rendered checks (focus rings on every page, placeholder fit at
// 390) were run with Playwright by hand, see the round's QA report.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const WEB = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const STYLES = path.join(WEB, "src/styles");
const css = (f) => readFileSync(path.join(STYLES, f), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
// the Playbox games keep their own palettes and their own motion (DESIGN-SYSTEM.md)
const GAMES = new Set(["playbox.css", "wave-patch.css", "wave-factory.css"]);
const SHEETS = readdirSync(STYLES).filter((f) => f.endsWith(".css") && !GAMES.has(f)).sort();
// innermost rules: [selector, body] (an @media prelude never sits in the selector)
const rules = (src) => [...src.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => [m[1].trim().replace(/\s+/g, " "), m[2]]);

/* ---- one focus ring ---------------------------------------------------------------- */

// Before: buttons showed a 2px red ring, nav and footer links the browser's own black ring
// (no base rule), and the chrome's icon buttons a soft 3-4px pink halo. One ring now.
test("focus: one ring token, applied to everything by base.css", () => {
  const t = css("tokens.css");
  assert.match(t, /--focus-ring:\s*2px solid var\(--live\);/, "the ring is 2px of the live red");
  assert.match(t, /--focus-offset:\s*2px;/);
  assert.match(css("base.css"), /:where\(:focus-visible\)\s*\{\s*outline:\s*var\(--focus-ring\);\s*outline-offset:\s*var\(--focus-offset\);\s*\}/,
    "a zero-specificity base ring every control inherits; components may still place their own");
});

test("focus: no soft halo (a glow, wash or volt-glow box-shadow) on a focused control", () => {
  const halos = [];
  for (const f of SHEETS) for (const [sel, body] of rules(css(f))) {
    if (!/:focus(-visible|-within)?\b/.test(sel)) continue;
    const bs = body.match(/box-shadow:([^;]*)/);
    if (bs && /--(live-glow|volt-glow|live-wash)\b/.test(bs[1])) halos.push(`${f}: ${sel}`);
  }
  assert.deepEqual(halos, [], `ring it (var(--focus-ring)), or for a field the 1px inset red edge:\n  ${halos.join("\n  ")}`);
});

test("focus: a rule that removes the outline puts a visible mark in its place", () => {
  const bare = [];
  for (const f of SHEETS) {
    const all = rules(css(f));
    for (const [sel, body] of all) {
    if (!/:focus-visible\b/.test(sel) || !/outline:\s*none/.test(body)) continue;
    // the mark may sit on a child (the homepage book link rings its cover)
    if (all.some(([s, b]) => s.startsWith(sel + " ") && /outline:\s*(?!\s|none)/.test(b))) continue;
    if (/box-shadow|border-(bottom-|left-|top-)?color|background|text-decoration|outline:\s*(?!\s|none)/.test(body)) continue;
    bare.push(`${f}: ${sel}`);
    }
  }
  assert.deepEqual(bare, [], `a colour change alone is not a focus mark:\n  ${bare.join("\n  ")}`);
});

/* ---- one motion vocabulary ------------------------------------------------------- */

// An interaction transition (hover, press, open, select) takes its duration from the token
// scale (--d-1 120ms ... --d-5 820ms), so every control answers in the same time. Named
// exceptions are motions with their own approved timing.
const OWN_TIMING = {
  "components.css .toc-tuner__hint": "the tuner hint's 250ms fade (approved spec, tuner-drag test)",
  "components.css .toc-tuner__needle": "the needle's 0.6s spring swing (approved spec)",
  "components.css .toc-tuner__name": "a station name coming into tune, 0.5s (text motion)",
  "base.css .nav__app-word": "the nav's cycling App word, 0.26s (text motion)",
};
test("motion: interaction transitions use the --d-N duration tokens", () => {
  const loose = [];
  for (const f of SHEETS) for (const [sel, body] of rules(css(f))) {
    for (const m of body.matchAll(/(?:^|;)\s*transition(?:-duration)?:([^;]*)/g)) {
      // drop token fallbacks (var(--d-2, .18s)) before looking for a literal time
      const v = m[1].replace(/var\(--d-\d,\s*[^)]*\)/g, "var(--d)");
      if (!/(^|[\s,])\.?\d*\.?\d+m?s\b/.test(v.replace(/\b0s\b/g, ""))) continue;
      if (OWN_TIMING[`${f} ${sel}`]) continue;
      loose.push(`${f}: ${sel} { transition:${m[1]} }`);
    }
  }
  assert.deepEqual(loose, [], `use var(--d-1..5) (or list a motion with its own timing):\n  ${loose.join("\n  ")}`);
});

/* ---- fields whose words fit ------------------------------------------------------ */

test("pricing: the empty market-rate field is wide enough for its placeholder", () => {
  // "set a rate" was clipped to "set a" in the 8ch rate fields, at every width
  assert.match(css("pricing.css"), /\.calc__field input\[type="number"\]\[placeholder\]\s*\{[^}]*width:\s*1[2-9]ch/);
});

test("models: the search placeholder steps down a size on a phone instead of being clipped", () => {
  // the field keeps 16px (no iOS focus zoom); only the placeholder text gets smaller
  assert.match(css("models.css"), /@media \(max-width: 480px\)\s*\{\s*\.tuner__input::placeholder\s*\{\s*font-size:\s*var\(--t-sm\);\s*\}\s*\}/);
});
