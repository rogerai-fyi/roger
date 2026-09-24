// Round 9: ?stage=pinned - a darkbloom-style pinned instrument stage, as a
// variant the founder can compare with the round-8 dial (the default).
//
// Mechanism (darkbloom, measured in round 8): a stage pinned for the whole
// §1-§9 run; each section is a full-width chapter panel with a gap before it;
// in each gap the stage shows, and scroll progress through the gap drives one
// step through progress windows - HOLD 0-25%, MOVE (eased) 25-75%, HOLD 75-100%.
// The needle stays centred; the scale pans under it so station k arrives.
// Dense sections (the market table, the spec plate, the spectrum, the company
// cards) are never on the stage: they ride their own full-width panels OVER it.
//
// No JS, reduced motion, narrow screens (<800px): the normal stacked page
// (round 8), the stage hidden. Runs the REAL stage.js against a mini-DOM.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import vm from "node:vm";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = readFileSync(path.join(WEB, "src/js/stage.js"), "utf8");
const dist = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

// sections as [id, top, height, name] in page px; a 600px gap precedes each
const IDS = ["demo", "market", "company"];
const SECTIONS = IDS.map((id, k) => [id, 1600 + k * 1600, 1000, ["§1 / OPERATING PROCEDURE", "§2 / THE BAND", "§3 / COMPANY"][k]]);

function run({ search = "?stage=pinned", w = 1440, h = 900, reduced = false, sections = SECTIONS } = {}) {
  const attrs = {}, listeners = {};
  let rafs = [], scrollY = 0;
  const style = {};
  const numerals = sections.map(() => ({ cls: new Set(), classList: null }));
  numerals.forEach((n) => { n.classList = { toggle: (c, on) => (on ? n.cls.add(c) : n.cls.delete(c)) }; });
  const title = { textContent: "", cls: new Set() };
  title.classList = { add: (c) => title.cls.add(c), remove: (c) => title.cls.delete(c) };
  const stage = {
    style: { setProperty: (k, v) => { style[k] = v; } },
    attrs: {}, setAttribute(k, v) { this.attrs[k] = String(v); },
    querySelector: (s) => (s === ".stage__title" ? title : null),
    querySelectorAll: (s) => (s === ".stage__scale b" ? numerals : []),
  };
  const targets = Object.fromEntries(sections.map(([id, top, height, name]) => [id, {
    getBoundingClientRect: () => ({ top: top - scrollY, bottom: top + height - scrollY, height }),
    querySelector: (s) => (s === ".sectionno" ? { textContent: name } : null),
  }]));
  const links = sections.map(([id]) => ({ getAttribute: (k) => (k === "href" ? "#" + id : null) }));
  const doc = {
    documentElement: { setAttribute: (k, v) => { attrs[k] = v; }, removeAttribute: (k) => { delete attrs[k]; } },
    querySelector: (s) => (s === ".stage" ? stage : null),
    querySelectorAll: (s) => (s === ".dial a[href^=\"#\"]" ? links : []),
    getElementById: (id) => targets[id] || null,
    hidden: false,
    addEventListener: (t, f) => { (listeners["d:" + t] ||= []).push(f); },
  };
  const win = {
    location: { search }, innerWidth: w, innerHeight: h,
    get scrollY() { return scrollY; },
    matchMedia: (q) => ({ matches: /reduce/.test(q) ? reduced : /min-width: 800px/.test(q) ? w >= 800 : false }),
    addEventListener: (t, f) => { (listeners[t] ||= []).push(f); },
    requestAnimationFrame: (f) => { rafs.push(f); return rafs.length; },
    cancelAnimationFrame: () => { rafs = []; },
    ResizeObserver: function (cb) { this.observe = () => {}; },
    URLSearchParams,
  };
  win.window = win; win.document = doc;
  vm.createContext(win);
  vm.runInContext(SRC, win);
  const frame = () => { const due = rafs; rafs = []; for (const f of due) f(); };
  const to = (y) => { scrollY = y; for (const f of listeners.scroll || []) f(); frame(); };
  return { attrs, style, title, numerals, to, pending: () => rafs.length, station: () => Number(style["--station"]) };
}
// the reading line is the middle of the viewport: scrollY = pageY - 450
const at = (pageY) => pageY - 450;

test("?stage=pinned turns the stage on for wide screens with motion allowed; otherwise the stacked page stays", () => {
  assert.equal(run().attrs["data-stage"], "pinned");
  assert.equal(run({ search: "" }).attrs["data-stage"], undefined, "default: the round-8 dial page");
  assert.equal(run({ search: "?stage=nope" }).attrs["data-stage"], undefined);
  assert.equal(run({ reduced: true }).attrs["data-stage"], undefined, "reduced motion: stacked, static");
  assert.equal(run({ w: 390 }).attrs["data-stage"], undefined, "phones: stacked, never a pinned trap");
});

test("in a gap the step runs through progress windows: hold, eased move, hold", () => {
  const r = run();
  // the gap before "market" is 2600..3200 (page); station 0 -> 1
  r.to(at(2600 + 600 * 0.15));
  assert.equal(r.station(), 0, "first 25%: still holding on §1");
  r.to(at(2600 + 600 * 0.5));
  assert.ok(r.station() > 0.2 && r.station() < 0.8, `mid-window: moving (${r.station()})`);
  r.to(at(2600 + 600 * 0.9));
  assert.equal(r.station(), 1, "last 25%: settled on §2");
});

test("while a chapter panel covers the stage, the station holds", () => {
  const r = run();
  r.to(at(3200 + 200));
  assert.equal(r.station(), 1);
  r.to(at(3200 + 900));
  assert.equal(r.station(), 1);
});

test("the stage engraves the station on air: its numeral lights and its title shows", () => {
  const r = run();
  r.to(at(3200 + 300));
  assert.ok(r.numerals[1].cls.has("is-on") && !r.numerals[0].cls.has("is-on"));
  assert.equal(r.title.textContent, "§2 / THE BAND", "the title is the section's own label (no new words)");
});

test("frames only while scrolling: one rAF per scroll burst, nothing queued at rest", () => {
  const r = run();
  r.to(at(3000));
  assert.equal(r.pending(), 0);
});

test("the markup: one decorative stage, hidden unless pinned; the run's content stays in DOM order", () => {
  const html = dist("index.html");
  assert.match(html, /<div class="stage" aria-hidden="true">/);
  const order = ["demo", "market", "company", "what", "spec", "books", "monetize", "operator", "go"].map((id) => html.indexOf(`id="${id}"`));
  assert.ok(order.every((i, k) => i > 0 && (k === 0 || i > order[k - 1])), "every section, in order");
  const css = dist("styles/home.css");
  assert.match(css, /\.stage\s*\{\s*display:\s*none;?\s*\}/, "no stage unless asked for");
  assert.match(css, /:root\[data-stage="pinned"\] \.stage\s*\{[^}]*position:\s*sticky/);
  assert.match(css, /\.stage__title\s*\{[^}]*transition:[^;]*1\.15s cubic-bezier\(\.16,\s*\.84,\s*\.28,\s*1\)/, "darkbloom's settle");
  assert.match(css, /\.stage\s*\{[^}]*overflow:\s*clip/, "the panning scale never widens the page");
  assert.match(dist("index.html"), /<script src="js\/stage\.js(\?v=[0-9a-f]+)?" defer><\/script>/);
});
// (the lab switcher's stage row is tested with the switcher, in scroll-stage.test.mjs)
