// Round 4 (2026-09-23): the homepage background carries the scroll story in
// TONE, not in lines. Founder: "i don't like the lines on the background
// animation, and the background changes are not powerful enough".
//
// Two "on air" zones turn the page from paper to ink and back:
//   zone 1: §1 the console + §2 the band (live traffic)
//   zone 2: §7 monetize + §8 privacy + §9 go (the transmitter, the finale)
// Each zone opens like a studio door (an inset card that widens to full bleed
// as it arrives), carries one red on-air bloom that grows as you enter, and an
// LED station field (radiomap.js) whose stations light up as you scroll
// through it. These tests pin the structure, the motion guards, and the thing
// that must never break: AA text contrast in every state, both site themes.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const read = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const zones = () => [...read("index.html").matchAll(/<div class="tone-zone" data-tone="ink"[^>]*>([\s\S]*?)<!-- \/tone-zone -->/g)].map((m) => m[1]);

test("three ink zones: the hero (the cover), the console + the band, then monetize + privacy + go", () => {
  const z = zones();
  assert.equal(z.length, 3);
  assert.match(z[0], /^\s*<section class="hero">/, "round 5: the first screen is on air");
  assert.match(z[1], /<section class="demo" id="demo">[\s\S]*<section class="market" id="market">/);
  assert.doesNotMatch(z[1], /id="company"/, "the company section is back on paper");
  assert.match(z[2], /id="monetize"[\s\S]*id="operator"[\s\S]*<section class="cta">/);
  const html = read("index.html");
  assert.match(html, /<div class="tone-zone" data-tone="ink" data-onload>\s*<section class="hero">/, "the hero zone tunes in on load");
  assert.ok(html.indexOf('class="institution-strip"') > html.indexOf("<!-- /tone-zone -->"), "the paper strip divides the cover from zone 1");
});

// ---- contrast: parse the zone's token scope and the bloom, then compute ----
const hex = (h) => { h = h.replace("#", ""); return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)); };
const lum = ([r, g, b]) => { const f = (c) => { c /= 255; return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b); };
const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
const over = (fg, a, bg) => fg.map((c, i) => Math.round(c * a + bg[i] * (1 - a)));
function block(css, selRe) {
  const m = css.match(new RegExp(selRe.source + "[^{]*\\{([^}]*)\\}"));   // the scope may list more selectors (the chrome)
  assert.ok(m, `found ${selRe}`);
  return Object.fromEntries([...m[1].matchAll(/(--[\w-]+):\s*(#[0-9A-Fa-f]{6})/g)].map((x) => [x[1], hex(x[2])]));
}

test("zone text stays AA (4.5:1) on the zone ground AND under the bloom at its peak, light and dark site", () => {
  const tokens = read("styles/tokens.css");
  const home = read("styles/home.css");
  const lightZone = block(tokens, /:root\[data-theme="dark"\],\s*\.tone-zone/);
  const darkZone = { ...lightZone, ...block(tokens, /:root\[data-theme="dark"\] \.tone-zone/) };
  const live = hex(tokens.match(/:root\[data-theme="dark"\],\s*\.tone-zone[^{]*\{[^}]*--live:\s*(#[0-9A-Fa-f]{6})/)[1]);
  const peak = Number(home.match(/--bloom-peak:\s*([\d.]+)/)[1]);
  const darkPeak = Number(home.match(/:root\[data-theme="dark"\] \.tone-zone\s*\{[^}]*--bloom-peak:\s*([\d.]+)/)?.[1] || peak);
  assert.ok(peak > 0.1, "the bloom is actually visible");
  for (const [name, z, pk] of [["light site", lightZone, peak], ["dark site", darkZone, darkPeak]]) {
    for (const ground of [z["--paper"], z["--paper-2"]]) {
      const bloomed = over(live, pk, ground);
      for (const ink of ["--ink-900", "--ink-700", "--ink-500"]) {
        for (const [where, bg] of [["ground", ground], ["peak bloom", bloomed]]) {
          const r = ratio(z[ink], bg);
          assert.ok(r >= 4.5, `${name}: ${ink} on ${where} is ${r.toFixed(2)}:1`);
        }
      }
    }
  }
});

test("zones re-derive their own text colour and ground from the ink tokens", () => {
  const home = read("styles/home.css");
  assert.match(home, /\.tone-zone\s*\{[^}]*color:\s*var\(--ink-700\)[^}]*background:\s*var\(--paper\)/s);
});

test("the door, the bloom and the section motion are opt-in; reduced motion gets a still, full-bleed zone", () => {
  const home = read("styles/home.css");
  const noPref = home.indexOf("prefers-reduced-motion: no-preference");
  for (const name of ["tone-door", "tone-bloom"]) {
    const use = home.search(new RegExp(`animation[^;]*\\b${name}\\b`));
    assert.ok(use > noPref && noPref > 0, `${name} is only applied under no-preference`);
  }
  assert.doesNotMatch(home.replace(/@keyframes[^{]*\{(?:[^{}]*\{[^}]*\})*[^}]*\}/g, ""), /\.tone-zone\s*\{[^}]*clip-path/,
    "at rest (and without motion) the zone is not clipped");
});

test("the network lines are gone: the map is LEDs in the zones, not a page-wide canvas", () => {
  const js = readFileSync(path.join(WEB, "src/js/radiomap.js"), "utf8");
  assert.doesNotMatch(js, /lineTo|links/, "no links, no lines");
  for (const p of ["index.html", "models.html", "voices.html"]) {
    assert.doesNotMatch(readFileSync(path.join(WEB, "src", p), "utf8"), /id="blipmap"/, `${p}: the page-wide canvas is gone`);
  }
  assert.doesNotMatch(read("styles/base.css"), /\.blipmap/, "and its CSS");
});

test("commands only break at spaces, never inside a flag", () => {
  const html = read("index.html");
  for (const m of html.matchAll(/<code class="(twoway__cmd|install__code)">([\s\S]*?)<\/code>/g)) {
    const bare = m[2].replace(/<span class="tok">[^<]*<\/span>/g, "");
    assert.doesNotMatch(bare, /\S-|-\S/, `every hyphenated token is kept whole in: ${m[2]}`);
  }
  assert.match(read("styles/base.css"), /\.tok\s*\{[^}]*white-space:\s*nowrap/);
});

test("wide screens (1600+): a wider page box, the panel alongside the headline", () => {
  const home = read("styles/home.css");
  const wide = home.match(/@media \(min-width: 1600px\)\s*\{([\s\S]*?)\n\}/)?.[1] || "";
  assert.match(wide, /:root\s*\{[^}]*--maxw:/, "the whole page box (nav included) widens together");
  assert.match(wide, /\.hero__tools\s*\{[^}]*grid-row:\s*1 \/ span 2/);
});

test("dark site: the zones land harder (a stronger bloom and a lit door edge), still AA (checked above)", () => {
  const home = read("styles/home.css");
  const dark = home.match(/:root\[data-theme="dark"\] \.tone-zone\s*\{([^}]*)\}/)?.[1] || "";
  const light = Number(home.match(/\.tone-zone\s*\{[^}]*--bloom-peak:\s*([\d.]+)/)[1]);
  assert.ok(Number(dark.match(/--bloom-peak:\s*([\d.]+)/)?.[1]) > light, "a stronger bloom on a dark site");
  assert.match(dark, /box-shadow:[^;]*var\(--live/, "a red-lit edge where the zone begins and ends");
});

test("1100-1599: FIG. 1 gets an equal column and a command size that fits one line", () => {
  const home = read("styles/home.css");
  const mid = home.match(/@media \(min-width: 1100px\)\s*\{([\s\S]*?)\n\}/)?.[1] || "";
  assert.match(mid, /\.hero__grid\s*\{[^}]*grid-template-columns:\s*minmax\(0, 1fr\) minmax\(0, 1fr\)/);
  assert.match(mid, /\.hero__tools \.install__code\s*\{[^}]*font-size:\s*0\.75rem/);
});

test("the middle gets a scroll moment: the Wave Spectrum tunes in, the spec plate ticks in", () => {
  const home = read("styles/home.css");
  const noPref = home.indexOf("prefers-reduced-motion: no-preference");
  for (const name of ["spec-wave", "plate-tick"]) {
    assert.ok(home.search(new RegExp(`animation[^;]*\\b${name}\\b`)) > noPref, `${name} is used, opt-in`);
    assert.match(home, new RegExp(`@keyframes ${name}`));
  }
  // one red only for the tier that is actually trained (Pico); the rest settle to ink
  assert.match(home, /\.home-spectrum li:first-child i\s*\{[^}]*animation-name:\s*spec-wave-on/);
});

test("touch, phones and tablets: reveals never blur and never rest below 0.9 opacity", () => {
  const home = read("styles/home.css");
  // phones AND tablets (round 6: an iPad at rest showed blurred cards at 768 / 1024)
  const touch = home.match(/@media \(max-width: 1024px\), \(pointer: coarse\)\s*\{([\s\S]*?)\n    \}/)?.[1] || "";
  assert.match(touch, /animation-name:\s*lift-touch/, "one plain reveal for every block");
  assert.match(touch, /animation-range:\s*entry 0% entry 15vh/, "finished by 85% of the viewport");
  const kf = home.match(/@keyframes lift-touch\s*\{[\s\S]*?\}\s*\}/)?.[0] || "";
  assert.ok(kf, "lift-touch keyframes");
  assert.doesNotMatch(kf, /filter/);
  for (const m of kf.matchAll(/opacity:\s*([\d.]+)/g)) assert.ok(Number(m[1]) >= 0.9);
});

test("phones: short commands get a smaller mono so they fit on one line", () => {
  const home = read("styles/home.css");
  assert.match(home, /@media \(max-width: 560px\)\s*\{[^}]*\.twoway__cmd\s*\{[^}]*font-size:\s*0\.72rem/);
});
