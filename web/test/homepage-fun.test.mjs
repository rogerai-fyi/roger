// Round 10 (founder: "study those website i gave you and see how we can clean
// up ours and make it fun to navigate on the homepage part"). Nothing pinned;
// the reader drives everything. Three interactions, each an enhancement of a
// page that is complete without JS:
//   1. the tuner - an in-page table of contents drawn as a tuning scale. The
//      red needle follows the station you point at or focus (CSS only).
//   2. the Wave Spectrum scrubber - a range control over the tier ladder;
//      dragging, arrow keys, pointing or focusing a tier tunes it in.
//   3. hardware-button feedback - buttons press down; the install command's
//      copy icon turns into a tick while "copied" is showing.
// And the clean-up: fewer boxes inside boxes (whitespace separates).
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import vm from "node:vm";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const dist = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const IDS = ["demo", "market", "company", "what", "spec", "books", "monetize", "operator", "go"];

// ---- 1. the tuner ----
test("the tuner is a plain in-page nav: nine links to the sections, in order, named by their own labels", () => {
  const html = dist("index.html");
  const tuner = html.match(/<nav class="tuner"[^>]*>[\s\S]*?<\/nav>/)?.[0] || "";
  assert.match(tuner, /aria-label="Sections"/);
  const hrefs = [...tuner.matchAll(/href="#([\w-]+)"/g)].map((m) => m[1]);
  assert.deepEqual(hrefs, IDS);
  // each station's name is the section's own label, word for word (no new copy)
  const labels = [...html.matchAll(/<span class="sectionno">([^<]*)<\/span>/g)].map((m) => m[1].replace(/^§\d+ \/ /, ""));
  const names = [...tuner.matchAll(/<span class="tuner__name">([^<]*)<\/span>/g)].map((m) => m[1]);
  assert.deepEqual(names, labels);
  assert.ok(html.indexOf('class="tuner"') > html.indexOf('class="institution-strip"'), "after the institution strip");
  assert.ok(html.indexOf('class="tuner"') < html.indexOf('id="demo"'), "before §1");
  assert.match(tuner, /<span class="tuner__needle" aria-hidden="true"><\/span>/);
});

test("the tuner's needle follows the pointer and keyboard focus with CSS only, and never pins", () => {
  const css = dist("styles/home.css");
  for (let k = 1; k <= 9; k++) {
    assert.match(css, new RegExp(`\\.tuner:has\\(li:nth-child\\(${k}\\) a:is\\(:hover, :focus-visible\\)\\)\\s*\\{[^}]*--at:\\s*${k - 1}`), `station ${k}`);
  }
  assert.match(css, /\.tuner__needle\s*\{[^}]*transition:[^;]*left/);
  assert.match(css, /\.tuner__needle\s*\{[^}]*background:\s*var\(--live\)/, "red only on the needle");
  const tunerRules = [...css.matchAll(/\.tuner[^{]*\{([^}]*)\}/g)].map((m) => m[1]).join("\n");
  assert.doesNotMatch(tunerRules, /position:\s*(sticky|fixed)/);
  assert.match(css, /@media \(prefers-reduced-motion: reduce\)\s*\{[^}]*\.tuner__needle\s*\{[^}]*transition:\s*none/);
});

// ---- 2. the Wave Spectrum scrubber ----
const SPEC = readFileSync(path.join(WEB, "src/js/spectrum.js"), "utf8");
function spectrum() {
  const tiers = ["Wave Pico", "Wave Nano", "Wave Micro", "Wave Giga", "Wave Tera", "Wave Peta", "Wave Exa"];
  const on = (el) => { el.listeners = {}; el.addEventListener = (t, f) => { (el.listeners[t] ||= []).push(f); }; return el; };
  const lis = tiers.map((name) => on({ cls: new Set(), attrs: {}, querySelector: (s) => (s === "b" ? { textContent: name } : null),
    classList: null, setAttribute(k, v) { this.attrs[k] = String(v); }, removeAttribute(k) { delete this.attrs[k]; } }));
  lis.forEach((li) => { li.classList = { toggle: (c, v) => (v ? li.cls.add(c) : li.cls.delete(c)) }; });
  let range = null;
  const ol = { getAttribute: (k) => (k === "aria-label" ? "The Wave Spectrum, pico to exa" : null), querySelectorAll: () => lis,
    parentNode: { insertBefore: (el) => { range = el; } } };
  const doc = {
    querySelector: (s) => (s === ".home-spectrum" ? ol : null),
    createElement: (tag) => on({ tagName: tag, attrs: {}, value: "", setAttribute(k, v) { this.attrs[k] = String(v); } }),
  };
  const win = { document: doc };
  win.window = win;
  vm.createContext(win);
  vm.runInContext(SPEC, win);
  const fire = (el, t) => { for (const f of el.listeners[t] || []) f({ type: t }); };
  return { range: () => range, lis, fire, tuned: () => lis.map((l) => l.cls.has("is-tuned")) };
}

test("the scrubber is a real range control over the ladder, labelled, starting on Pico", () => {
  const s = spectrum();
  const r = s.range();
  assert.equal(r.tagName, "input");
  assert.equal(r.type, "range");
  assert.equal(r.min, "0"); assert.equal(r.max, "6"); assert.equal(r.value, "0");
  assert.equal(r.attrs["aria-label"], "The Wave Spectrum, pico to exa", "labelled by the ladder's own name");
  assert.equal(r.attrs["aria-valuetext"], "Wave Pico");
  assert.deepEqual(s.tuned(), [true, false, false, false, false, false, false]);
});

test("dragging or arrowing the scrubber tunes a tier; pointing at or focusing a tier moves the scrubber", () => {
  const s = spectrum();
  const r = s.range();
  r.value = "4"; s.fire(r, "input");
  assert.deepEqual(s.tuned(), [false, false, false, false, true, false, false]);
  assert.equal(r.attrs["aria-valuetext"], "Wave Tera");
  s.fire(s.lis[2], "pointerenter");
  assert.equal(r.value, "2");
  assert.equal(s.tuned()[2], true);
  s.fire(s.lis[6], "focusin");
  assert.equal(r.value, "6");
  assert.equal(r.attrs["aria-valuetext"], "Wave Exa");
});

test("without JS the ladder is the complete static list; with JS every tier stays readable", () => {
  const html = dist("index.html");
  const ladder = html.match(/<ol class="home-spectrum"[\s\S]*?<\/ol>/)?.[0] || "";
  assert.equal((ladder.match(/<li /g) || []).length, 7);
  assert.doesNotMatch(html, /type="range"/, "the control is added by spectrum.js, never in the HTML");
  assert.match(html, /<script src="js\/spectrum\.js(\?v=[0-9a-f]+)?" defer><\/script>/);
  const css = dist("styles/home.css");
  assert.doesNotMatch(css, /\.home-spectrum li(:not\(\.is-tuned\))?\s*\{[^}]*opacity/, "untuned tiers are never faded");
  assert.match(css, /\.home-spectrum li\.is-tuned i\s*\{[^}]*color:\s*var\(--live\)/, "the tuned tier's wave is the red indicator");
});

// ---- 3. hardware-button feedback ----
test("buttons press like hardware and the copy icon becomes a tick while copied", () => {
  const css = dist("styles/home.css");
  assert.match(css, /:is\(\.install__box, \.company__primary, \.tuner a\):active\s*\{[^}]*transform:\s*translateY\(1px\)/);
  assert.match(css, /\.install__box\.is-copied \.install__copy svg\s*\{[^}]*opacity:\s*0/);
  assert.match(css, /\.install__box\.is-copied \.install__copy::after\s*\{[^}]*content:/);
  assert.match(css, /@media \(prefers-reduced-motion: reduce\)\s*\{[^}]*:active\s*\{[^}]*transform:\s*none/);
});

// ---- the clean-up ----
test("clean: no box around the company cards or the spectrum; the two sides are tinted panels, not bordered boxes", () => {
  const css = dist("styles/home.css");
  const rule = (sel) => css.match(new RegExp(`(?:^|\\n)${sel.replace(/[.]/g, "\\.")}\\s*\\{([^}]*)\\}`))?.[1] || "";
  assert.doesNotMatch(rule(".company__grid"), /border/);
  assert.doesNotMatch(rule(".home-spectrum"), /border:/);
  assert.doesNotMatch(rule(".twoway__side"), /border:/);
  assert.match(rule(".twoway__side"), /border-radius/);
});
