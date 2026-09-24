// The homepage's ink panels (.tone-zone): the cover, §1-§2 (the console, the
// band) and §7-§9 (monetize, privacy, go) sit in calm, inset, rounded ink
// panels on the paper page. (Rounds 4-7 also gave them a door, a red bloom
// and an LED field; round 8 removed all three.) These tests pin the
// structure, the motion guards, the layout rules that grew up alongside, and
// the thing that must never break: AA text contrast, both site themes.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
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
  assert.match(z[2], /id="monetize"[\s\S]*id="operator"[\s\S]*<section class="cta"( id="go")?>/);
  const html = read("index.html");
  assert.match(html, /<div class="tone-zone" data-tone="ink">\s*<section class="hero">/, "the cover is an ink panel");
  assert.doesNotMatch(html, /data-onload|data-bloom/, "round 8: no load tune-in field, no bloom placement");
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

test("zone text stays AA (4.5:1) on the zone ground, ink-900 to ink-400, light and dark site", () => {
  const tokens = read("styles/tokens.css");
  // the zone's text inks (a zone-only lift of ink-400/-500) sit on top of the shared ink set
  const zoneText = block(tokens, /\.tone-zone\s*(?=\{\s*--ink-500)/);
  const lightZone = { ...block(tokens, /:root\[data-theme="dark"\],\s*\.tone-zone/), ...zoneText };
  const darkZone = { ...lightZone, ...block(tokens, /:root\[data-theme="dark"\] \.tone-zone/), ...zoneText };
  for (const [name, z] of [["light site", lightZone], ["dark site", darkZone]]) {
    for (const ground of [z["--paper"], z["--paper-2"]]) {
      // ink-400 too: .sectionno, the eyebrow, FIG. labels, .demo__fine, .market__foot
      for (const ink of ["--ink-900", "--ink-700", "--ink-500", "--ink-400"]) {
        const r = ratio(z[ink], ground);
        assert.ok(r >= 4.5, `${name}: ${ink} on the zone ground is ${r.toFixed(2)}:1`);
      }
    }
  }
});

test("zones re-derive their own text colour and ground from the ink tokens", () => {
  // the ink panel is a shared component (components.css, loaded on every page)
  const home = read("styles/components.css");
  assert.match(home, /\.tone-zone\s*\{[^}]*color:\s*var\(--ink-700\)[^}]*background:\s*var\(--paper\)/s);
});

// Round 8 (founder: "no grid looking thing ... or the lamp looking color thing,
// keep it simple and more subtle and more cool"): the ink zones stay, as calm
// inset panels on the paper page. No LED field, no red bloom, no door.
test("zones are calm inset panels: rounded, inset from the page edge, nothing animated on them", () => {
  // the panel is defined in components.css; neither it nor the homepage may dress it up
  const home = read("styles/components.css") + read("styles/home.css");
  const zone = home.match(/\.tone-zone\s*\{([^}]*margin[^}]*)\}/)?.[1] || "";
  assert.match(zone, /margin:[^;]*var\(--panel-inset\)/, "inset from the page edge");
  assert.match(zone, /border-radius:\s*var\(--panel-r\)/, "rounded");
  assert.doesNotMatch(home, /tone-door|tone-bloom|tone-field|bloom-peak|\.tone-zone::before/, "no door, bloom or field");
  assert.doesNotMatch(home, /\.tone-zone[^{]*\{[^}]*clip-path/, "never clipped");
});

test("no background field of any kind: the LED canvas and its script are gone", () => {
  assert.ok(!existsSync(path.join(WEB, "src/js/radiomap.js")), "radiomap.js deleted");
  for (const p of ["index.html", "models.html", "voices.html"]) {
    const html = readFileSync(path.join(WEB, "src", p), "utf8");
    assert.doesNotMatch(html, /radiomap|blipmap|tone-field/, `${p}: nothing left of the fields`);
  }
  assert.doesNotMatch(read("styles/base.css"), /\.blipmap/);
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
  // (round 10: the scrubber marks the tuned tier in red; the tune-in settles every wave to ink)
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

// ---- round 7: motion that a reader can stop in the middle of ----
// Scroll-linked animation has no "mid-transition": wherever the reader stops IS
// the resting state. So nothing scroll-linked may ever sit below 0.9 opacity,
// and any blur must be over before a block is 85% of the way up the screen.
function rules(css) {
  // flat list of [selector, body] for every non-keyframe rule, however nested
  const out = [];
  const walk = (src) => {
    let i = 0;
    while (i < src.length) {
      const open = src.indexOf("{", i);
      if (open < 0) break;
      const pre = src.slice(i, open).trim();
      let depth = 1, j = open + 1;
      for (; j < src.length && depth; j++) { if (src[j] === "{") depth++; else if (src[j] === "}") depth--; }
      const body = src.slice(open + 1, j - 1);
      const sel = pre.slice(pre.lastIndexOf("}") + 1).trim();
      if (/^@keyframes/.test(sel)) { /* skip */ } else if (/^@/.test(sel)) walk(body); else out.push([sel, body]);
      i = j;
    }
  };
  walk(css.replace(/\/\*[\s\S]*?\*\//g, ""));
  return out;
}
const keyframes = (css) => Object.fromEntries([...css.replace(/\/\*[\s\S]*?\*\//g, "").matchAll(/@keyframes\s+([\w-]+)\s*\{((?:[^{}]*\{[^}]*\})*)[^}]*\}/g)].map((m) => [m[1], m[2]]));

test("nothing scroll-linked rests below 0.9 opacity, and blur is over by 85% of the viewport", () => {
  const css = read("styles/home.css");
  const kf = keyframes(css);
  // scroll-linked = the rule binds a scroll/view timeline or sets a scroll range
  // (the touch override swaps names on timeline-bound blocks); time-based
  // entrances (the hero on load, the market rows) are not resting states
  const scrollLinked = rules(css).filter(([sel, body]) => /animation-timeline:\s*(view\(|scroll\(|--)|animation-range:/.test(body))
    .filter(([sel]) => !/::(before|after)/.test(sel));       // decorative layers (the bloom) carry no text
  const names = new Set();
  for (const [, body] of scrollLinked) {
    for (const m of body.matchAll(/animation(?:-name)?:\s*([\w-]+)/g)) if (kf[m[1]]) names.add(m[1]);
  }
  assert.ok(names.size >= 5, `found the scroll-linked keyframes (${[...names].join(", ")})`);
  for (const n of names) {
    for (const o of kf[n].matchAll(/opacity:\s*([\d.]+)/g)) assert.ok(Number(o[1]) >= 0.9, `${n} rests at opacity ${o[1]}`);
    if (/blur\(/.test(kf[n])) {
      for (const [sel, body] of scrollLinked.filter(([, b]) => new RegExp(`\\b${n}\\b`).test(b))) {
        const end = body.match(/animation-range:\s*entry 0% entry (\d+)vh/);
        assert.ok(end && Number(end[1]) <= 15, `${sel}: ${n} blurs until entry ${end && end[1]}vh (must be <= 15vh)`);
      }
    }
  }
});

test("no choreography rule is shadowed: its targets are not [data-reveal] blocks (whose rule wins)", () => {
  const css = read("styles/home.css");
  const html = read("index.html");
  const revealed = new Set([...html.matchAll(/<[^>]*\bdata-reveal\b[^>]*>/g)]
    .flatMap((m) => (m[0].match(/class="([^"]*)"/)?.[1] || "").split(/\s+/)));
  for (const [sel, body] of rules(css)) {
    if (!/animation(-name)?:/.test(body)) continue;   // every animated rule (round 10: no variant scoping)
    for (const part of sel.split(/,(?![^(]*\))/)) {
      const last = part.trim().split(/\s+/).pop();
      const classes = [...last.matchAll(/\.([\w-]+)/g)].map((m) => m[1]);
      for (const c of classes) assert.ok(!revealed.has(c) || /data-reveal/.test(part), `${part.trim()} is shadowed by the [data-reveal] rule on .${c}`);
    }
  }
});

test("no adaptive chrome: on a paper page with inset panels the nav simply stays paper", () => {
  assert.doesNotMatch(read("styles/tokens.css"), /data-chrome/);
  assert.doesNotMatch(read("styles/home.css"), /data-chrome/);
  assert.doesNotMatch(readFileSync(path.join(WEB, "src/js/scroll-stage.js"), "utf8"), /data-chrome/);
});

