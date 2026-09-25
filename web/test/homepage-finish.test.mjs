// Homepage finishing pass (2026-09-24): presentation only. These pin
//   (a) the words: the whole built <body> reads exactly as it did before the
//       pass (fixtures/homepage-page-text.json), links included, in order;
//   (b) the alignment rules the pass enforced: every block sits on the
//       content box, the paper-middle bands are inset panels like the ink
//       ones, the footer's lines sit on the same box;
//   (c) the finish itself: dark-mode depth, the tuner's resting tick, the
//       finale's command, the spectrum as a phone gallery the scrubber drives;
//   (d) every new motion is opt-in (prefers-reduced-motion: no-preference).
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import vm from "node:vm";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const dist = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");
const css = (f) => src(`styles/${f}`).replace(/\/\*[\s\S]*?\*\//g, "");
const fixture = JSON.parse(readFileSync(path.join(WEB, "test/fixtures/homepage-page-text.json"), "utf8"));

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const body = () => dist("index.html").match(/<body[\s\S]*<\/body>/)[0]
  .replace(/<script[\s\S]*?<\/script>/g, "").replace(/<style[\s\S]*?<\/style>/g, "");

// the rule bodies for a selector (exact prelude match, anywhere in the sheet)
function rules(sheet, selector) {
  const esc = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return [...sheet.matchAll(new RegExp(`(?:^|[}\\s,])${esc}\\s*\\{([^}]*)\\}`, "g"))].map((m) => m[1]).join(";");
}
// bodies of @media blocks whose prelude matches re (brace-matched)
function media(sheet, re) { return mediaList(sheet, re).join("\n"); }
function mediaList(sheet, re) {
  const out = [];
  const at = /@media[^{]*\{/g;
  let m;
  while ((m = at.exec(sheet))) {
    if (!re.test(m[0])) continue;
    let depth = 1, i = at.lastIndex;
    for (; i < sheet.length && depth; i++) { if (sheet[i] === "{") depth++; else if (sheet[i] === "}") depth--; }
    out.push(sheet.slice(at.lastIndex, i - 1));
  }
  return out;
}

// ---- (a) the words ----
test("(a) every word on the homepage is unchanged (whitespace aside)", () => {
  const text = body().replace(/<!--[\s\S]*?-->/g, "").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();
  assert.equal(text.replace(/\s+/g, ""), fixture.text.replace(/\s+/g, ""), "a presentation pass never edits copy");
});

test("(a) every link on the homepage is the same link, in the same order", () => {
  assert.deepEqual([...body().matchAll(/href="([^"]*)"/g)].map((m) => m[1]), fixture.hrefs);
});

// ---- (b) alignment ----
test("(b) the panel geometry is a token, so any tinted panel frames the content like the ink panels", () => {
  assert.match(css("tokens.css"), /--panel-mx:\s*max\(var\(--panel-inset\)/);
  assert.match(rules(css("components.css"), ".tone-zone"), /margin:\s*var\(--panel-inset\) var\(--panel-mx\)/);
});

test("(b) the paper-middle bands are inset, rounded tinted panels; inside an ink panel space separates", () => {
  // the paper-middle bands are the shared inset band (components.css .band--inset)
  const home = css("home.css");
  for (const id of ["what", "books"]) assert.match(dist("index.html"), new RegExp(`<section class="section band band--inset" id="${id}">`));
  const band = rules(css("components.css"), ".band--inset");
  assert.match(band, /margin:\s*var\(--panel-inset\) var\(--panel-mx\)/, "the ink panel's own margins");
  assert.match(band, /border-radius:\s*var\(--panel-r\)/);
  assert.match(band, /border-block:\s*0/);
  assert.match(rules(home, ".tone-zone .band"), /background:\s*none/);
});

test("(b) the doors, the console, the band and the meter all run to the content box's right edge", () => {
  const home = css("home.css");
  assert.doesNotMatch(rules(home, ".institution-strip__grid"), /padding-inline:\s*0/, "the doors sit inside the gutter, not on it");
  assert.doesNotMatch(rules(home, ".term"), /max-width:\s*\d+px/);
  assert.doesNotMatch(rules(home, ".market__panel"), /max-width:\s*\d+px/);
  assert.doesNotMatch(rules(home, ".meter"), /max-width:\s*\d+px/);
});

test("(b) the company cards stand their link rows on the card floor, level with each other", () => {
  const home = css("home.css");
  assert.match(rules(home, ".company__card"), /display:\s*flex[^;]*;[\s\S]*flex-direction:\s*column|flex-direction:\s*column[\s\S]*display:\s*flex/);
  assert.match(rules(home, ".company__links"), /margin-top:\s*auto/);
});

test("(b) the footer's colophon and bottom rule sit on the content box (every page)", () => {
  const base = css("base.css");
  assert.match(rules(base, ".footer__colophon"), /margin:\s*0 auto/, "a .wrap keeps its auto side margins");
  assert.doesNotMatch(rules(base, ".footer__bar"), /border-top/, "a border on .wrap runs through the gutters");
  assert.match(rules(base, ".footer__bar::before"), /left:\s*var\(--gutter\)[\s\S]*right:\s*var\(--gutter\)/);
});

// ---- (c) the finish ----
test("(c) dark mode: an ink panel on a dark page is outlined, so the room still reads as a room", () => {
  assert.match(rules(css("components.css"), ':root[data-theme="dark"] .tone-zone'), /box-shadow:\s*inset 0 0 0 1px var\(--hairline-2\)/);
});

test("(c) the tuner: the resting station's major tick is the red indicator", () => {
  assert.match(rules(css("components.css"), ".toc-tuner a.is-current b::before"), /background:\s*var\(--live\)/);
});

test("(c) the tuner's readout comes into tune (blur and tracking settle), only when motion is welcome", () => {
  const calm = media(css("components.css"), /prefers-reduced-motion:\s*no-preference/);
  assert.match(calm, /\.toc-tuner__name\s*\{[^}]*transition:[^}]*filter[^}]*letter-spacing/);
  const sheet = css("components.css");
  const rest = mediaList(sheet, /prefers-reduced-motion:\s*no-preference/).reduce((s, b) => s.split(b).join(""), sheet);
  assert.doesNotMatch(rules(rest, ".toc-tuner__name"), /transition/, "no transition outside no-preference");
});

test("(c) the finale: the command is the page's last big moment", () => {
  const home = css("home.css");
  assert.match(rules(home, "#go h2"), /font-size:\s*min\(var\(--t-display\)/, "the closing line at the hero's scale");
  assert.match(rules(home, "#go .install__box--lg :is(.install__code, .install__prompt)"), /font-size:\s*clamp\(/);
});

test("(c) phones: the Wave Spectrum is a snapping gallery, and the scrubber drives it", () => {
  const phone = media(css("home.css"), /max-width:\s*720px/);
  assert.match(phone, /\.home-spectrum\s*\{[^}]*grid-auto-flow:\s*column[^}]*scroll-snap-type:\s*x mandatory/);
});

test("(c) the market on phones: the model name gets the whole row, the figures sit under it", () => {
  const phone = media(css("home.css"), /max-width:\s*680px/);
  assert.match(phone, /\.mkt-cell--model\s*\{[^}]*grid-column:\s*1\s*\/\s*-1/);
});

// scrub.js with a list that overflows sideways (the phone gallery)
function gallery() {
  const on = (el) => { el.listeners = {}; el.addEventListener = (t, f) => { (el.listeners[t] ||= []).push(f); }; return el; };
  const names = ["Wave Pico", "Wave Nano", "Wave Micro", "Wave Giga", "Wave Tera", "Wave Peta", "Wave Exa"];
  const lis = names.map((n, k) => on({ cls: new Set(), offsetLeft: k * 300, offsetWidth: 280,
    querySelector: (s) => (s === "b" ? { textContent: n } : null), setAttribute() {} }));
  lis.forEach((li) => { li.classList = { toggle: (c, v) => (v ? li.cls.add(c) : li.cls.delete(c)) }; });
  let range = null;
  const scrolls = [];
  const ol = on({ getAttribute: () => "The Wave Spectrum, pico to exa", querySelectorAll: () => lis,
    scrollWidth: 2100, clientWidth: 360, scrollLeft: 0,
    scrollTo(o) { scrolls.push(o); this.scrollLeft = o.left; },
    parentNode: { insertBefore: (el) => { range = el; } } });
  const doc = { querySelectorAll: (s) => (s === "[data-scrub]" ? [ol] : []),
    createElement: (tag) => on({ tagName: tag, value: "", setAttribute() {}, style: { setProperty() {} } }) };
  const win = { document: doc, matchMedia: () => ({ matches: false }), setTimeout: (f) => f(), clearTimeout() {} };
  win.window = win;
  vm.createContext(win);
  vm.runInContext(src("js/scrub.js"), win);
  const fire = (el, t) => { for (const f of el.listeners[t] || []) f({ type: t }); };
  return { ol, lis, scrolls, range: () => range, fire, tuned: () => lis.findIndex((l) => l.cls.has("is-tuned")) };
}

test("(c) scrubbing an overflowing ladder brings the tuned tier into the middle of the gallery", () => {
  const g = gallery();
  assert.equal(g.scrolls.length, 0, "loading the page never scrolls the gallery");
  const r = g.range();
  r.value = "4"; g.fire(r, "input");
  assert.equal(g.scrolls.length, 1);
  assert.equal(g.scrolls[0].left, 4 * 300 - (360 - 280) / 2, "centred");
});

test("(c) swiping the gallery tunes the tier nearest its middle (the scrubber follows)", () => {
  const g = gallery();
  g.ol.scrollLeft = 2 * 300 - 40; g.fire(g.ol, "scroll");
  assert.equal(g.tuned(), 2);
  assert.equal(g.range().value, "2");
});

// ---- (d) motion is opt-in ----
test("(d) the pass's new animations run only under prefers-reduced-motion: no-preference", () => {
  const all = css("components.css") + css("home.css");
  const blocks = mediaList(all, /prefers-reduced-motion:\s*no-preference/);
  const calm = blocks.join("\n");
  const outside = blocks.reduce((s, b) => s.split(b).join(""), all);
  for (const name of ["cursor-blink"]) {
    assert.match(all, new RegExp(`@keyframes\\s+${name}\\b`), `${name} exists`);
    assert.doesNotMatch(outside.replace(/@keyframes[^{]*\{(?:[^{}]*\{[^}]*\})*[^}]*\}/g, ""), new RegExp(`animation[^;]*\\b${name}\\b`), `${name} used outside no-preference`);
    assert.match(calm, new RegExp(`animation[^;]*\\b${name}\\b`), `${name} used under no-preference`);
  }
});
