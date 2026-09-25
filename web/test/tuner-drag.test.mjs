// The TOC tuner is drag-to-tune (founder: tapping small stations on a phone is hard).
// The scale strip (ticks, needle, numerals) is the drag zone: a touch that starts there
// always tunes, whatever its angle (touch-action: none; the founder's phone showed a
// pan-y band handing every slightly vertical thumb drag to the page as a scroll). The
// needle jumps to the finger on press and follows it; the readout names the station under
// it; letting go on another station goes there, as its link would. A tap is still the
// link's own click; a drag back to the start station, or a cancelled gesture, changes
// nothing. The rest of the band (the readout) scrolls the page as usual. Semantics do not change: the links and their
// aria-current stay the whole accessible story. The long-document list form (13+ stations
// under 760px, no needle) is not dragged: it is a list of full-width 44px rows.
// This drives the real tuner.js in a small fake DOM.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import vm from "node:vm";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const TUNER = readFileSync(path.join(WEB, "src/js/tuner.js"), "utf8");
const css = readFileSync(path.join(WEB, "src/styles/components.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

// 5 stations on a 500px scale starting at x=100: station k spans [100 + 100k, 200 + 100k)
function rig({ reduced = false, listForm = false } = {}) {
  const on = (el) => { el.listeners = {}; el.addEventListener = (t, f, o) => { (el.listeners[t] ||= []).push({ f, capture: !!(o && (o === true || o.capture)) }); }; el.removeEventListener = () => {}; return el; };
  const fire = (el, t, ev) => { for (const { f } of el.listeners[t] || []) f(ev); };
  const clicks = [];
  const ids = ["s1", "s2", "s3", "s4", "s5"];
  const style = {};
  const nav = on({
    cls: new Set(),
    style: { setProperty: (k, v) => { style[k] = String(v); }, removeProperty: (k) => { delete style[k]; } },
  });
  nav.classList = { add: (c) => nav.cls.add(c), remove: (c) => nav.cls.delete(c), contains: (c) => nav.cls.has(c), toggle: (c, v) => (v ? nav.cls.add(c) : nav.cls.delete(c)) };
  const links = ids.map((id, k) => on({ id, k, attrs: {}, cls: new Set(),
    getAttribute: (a) => (a === "href" ? "#" + id : null), setAttribute(a, v) { this.attrs[a] = String(v); },
    focus() {}, click() { clicks.push(id); fire(nav, "click", { target: this, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {} }); } }));
  links.forEach((a) => { a.classList = { toggle: (c, v) => (v ? a.cls.add(c) : a.cls.delete(c)), add: (c) => a.cls.add(c), remove: (c) => a.cls.delete(c) }; });
  const captured = [];
  const band = on({ setPointerCapture: (id) => captured.push(id), releasePointerCapture: () => {}, hasPointerCapture: () => captured.length > 0 });
  const scale = on({ getBoundingClientRect: () => ({ left: 100, width: 500, right: 600 }) });
  band.contains = (t) => t === band || t === scale || links.includes(t);
  scale.setPointerCapture = band.setPointerCapture;
  const needle = { listForm };
  nav.querySelectorAll = (q) => (q === "a" ? links : []);
  nav.querySelector = (q) => ({ ".toc-tuner__band": band, ".toc-tuner__scale": scale, ".toc-tuner__needle": needle }[q] || null);
  const vibes = [];
  const win = {
    innerHeight: 1000,
    matchMedia: (q) => ({ matches: /reduce/.test(q) ? reduced : false }),
    getComputedStyle: (el) => ({ display: el === needle && listForm ? "none" : "block" }),
    navigator: { vibrate: (ms) => { vibes.push(ms); return true; } },
    setTimeout: () => 0,   // timers never fire here: the trailing click comes first, as in a browser
  };
  win.window = win;
  win.document = { querySelectorAll: (q) => (q === "[data-tuner]" ? [nav] : []), getElementById: () => null };
  vm.createContext(win);
  vm.runInContext(TUNER, win);
  const ev = (x, y, extra = {}) => ({ clientX: x, clientY: y, pointerId: 7, pointerType: "touch", isPrimary: true, button: 0,
    target: extra.target || links[Math.max(0, Math.min(4, Math.floor((x - 100) / 100)))], preventDefault() { this.prevented = true; }, ...extra });
  const down = (x, y = 50, o) => fire(scale, "pointerdown", ev(x, y, o));
  const move = (x, y = 50, o) => fire(scale, "pointermove", ev(x, y, o));
  const up = (x, y = 50, o) => fire(scale, "pointerup", ev(x, y, o));
  const cancel = () => fire(scale, "pointercancel", ev(0, 0));
  // a gesture on the band outside the scale (the readout): the page's
  const outside = (x, y) => { const e = ev(x, y, { target: band }); fire(band, "pointerdown", e); fire(band, "pointermove", ev(x, y + 80, { target: band })); };
  // the browser's own click after a pointerup on a link (a tap): passes the nav's capture listeners
  const nativeClick = (k) => { const e = { target: typeof k === "number" ? links[k] : k, defaultPrevented: false, preventDefault() { this.defaultPrevented = true; }, stopPropagation() {} };
    fire(nav, "click", e); return !e.defaultPrevented; };
  return { nav, links, style, clicks, captured, vibes, down, move, up, cancel, outside, nativeClick };
}

const at = (t) => Number(t.style["--at"]);

test("drag: the needle follows the finger continuously and the readout names the station under it", () => {
  const t = rig();
  t.down(150);                       // on station 1: the needle jumps to the finger at once
  assert.ok(t.nav.cls.has("is-dragging"));
  assert.equal(at(t), 0);
  assert.ok(t.links[0].cls.has("is-tuning"));
  t.move(170);                       // past the slop: a drag
  assert.deepEqual(t.captured, [7], "the scale captures the pointer");
  assert.ok(Math.abs(at(t) - 0.2) < 1e-9, `needle at ${at(t)}`);   // (170-100)/100 - 0.5
  assert.ok(t.links[0].cls.has("is-tuning"));
  t.move(365);                       // over station 3
  assert.ok(Math.abs(at(t) - 2.15) < 1e-9);
  assert.ok(t.links[2].cls.has("is-tuning") && !t.links[0].cls.has("is-tuning"), "one station tunes at a time");
  t.move(9999);                      // past the end: clamped to the last station
  assert.equal(at(t), 4);
  assert.ok(t.links[4].cls.has("is-tuning"));
});

test("drag: letting go on a station goes there (its link's click) and marks it current", () => {
  const t = rig();
  t.down(150); t.move(200); t.move(450); t.up(450);
  assert.deepEqual(t.clicks, ["s4"]);
  assert.ok(!t.nav.cls.has("is-dragging"));
  assert.equal(t.style["--at"], undefined, "the needle hands back to the stylesheet");
  assert.equal(t.style["--cur"], "3");
  assert.equal(t.links[3].attrs["aria-current"], "location");
  assert.ok(!t.links[3].cls.has("is-tuning"));
  assert.equal(t.nativeClick(0), false, "the browser's own click at the end of the drag is swallowed");
});

test("tap: no drag, the link's own click navigates as today", () => {
  const t = rig();
  t.down(250); t.move(252); t.up(252);
  assert.deepEqual(t.clicks, [], "the script does not navigate on a tap");
  assert.ok(!t.nav.cls.has("is-dragging"));
  assert.equal(t.nativeClick(1), true, "the tap's own click goes through");
});

test("a drag that ends back on the station it started from does nothing", () => {
  const t = rig();
  t.down(150); t.move(260); t.move(140); t.up(140);
  assert.deepEqual(t.clicks, []);
  assert.equal(t.style["--at"], undefined);
  assert.equal(t.nativeClick(0), false, "and its trailing click does not jump either");
});

test("cancel (the browser took the gesture) restores the needle", () => {
  const t = rig();
  t.down(150); t.move(300); t.cancel();
  assert.deepEqual(t.clicks, []);
  assert.ok(!t.nav.cls.has("is-dragging"));
  assert.equal(t.style["--at"], undefined);
  assert.ok(t.links.every((a) => !a.cls.has("is-tuning")));
});

test("inside the scale there is no direction rule: a steep thumb drag still tunes and lands", () => {
  const t = rig();
  t.down(150, 50); t.move(158, 70); t.move(210, 110); t.move(265, 160);   // ~45 degrees and more
  assert.ok(t.nav.cls.has("is-dragging"));
  assert.deepEqual(t.captured, [7]);
  assert.ok(t.links[1].cls.has("is-tuning"));
  t.up(265, 160);
  assert.deepEqual(t.clicks, ["s2"]);
});

test("a gesture that starts outside the scale (the readout) is the page's: it never tunes", () => {
  const t = rig();
  t.outside(150, 10);
  assert.ok(!t.nav.cls.has("is-dragging"));
  assert.equal(t.style["--at"], undefined);
  assert.deepEqual(t.captured, []);
});

test("touch ticks once per station passed; never with reduced motion, never for a mouse", () => {
  const t = rig();
  t.down(150); t.move(170); t.move(260); t.move(290); t.move(360);
  assert.deepEqual(t.vibes, [8, 8], "a tick entering station 2 and station 3 (not on the press itself)");
  const m = rig();
  m.down(150, 50, { pointerType: "mouse" }); m.move(170, 50, { pointerType: "mouse" }); m.move(360, 50, { pointerType: "mouse" });
  assert.deepEqual(m.vibes, []);
  assert.ok(m.nav.cls.has("is-dragging"), "a mouse drags too");
  const r = rig({ reduced: true });
  r.down(150); r.move(170); r.move(360);
  assert.deepEqual(r.vibes, []);
  r.move(365);
  assert.equal(at(r), 2, "reduced motion: the needle jumps station to station");
});

test("the long-document list form (no needle shown) is not dragged: its rows are tapped", () => {
  const t = rig({ listForm: true });
  t.down(150); t.move(300); t.up(300);
  assert.ok(!t.nav.cls.has("is-dragging"));
  assert.deepEqual(t.clicks, []);
});

test("a secondary mouse button or a second finger does not tune", () => {
  const t = rig();
  t.down(150, 50, { pointerType: "mouse", button: 2 }); t.move(300, 50, { pointerType: "mouse", button: 2 });
  assert.ok(!t.nav.cls.has("is-dragging"));
  const u = rig();
  u.down(150, 50, { isPrimary: false }); u.move(300, 50, { isPrimary: false });
  assert.ok(!u.nav.cls.has("is-dragging"));
});

test("the scale is the drag zone: touch-action none there, finger-sized on touch, with a grip; the readout still scrolls", () => {
  assert.match(css, /\.toc-tuner__scale \{[^}]*touch-action: none/);
  assert.match(css, /\.toc-tuner__band \{[^}]*touch-action: pan-y/, "outside the scale the page scrolls");
  assert.match(css, /\.toc-tuner__band \{[^}]*user-select: none/);
  const coarse = [...css.matchAll(/@media \(pointer: coarse\) \{([\s\S]*?)\n\}/g)].map((m) => m[1]).join("\n");
  assert.match(coarse, /\.toc-tuner__scale \{[^}]*min-height: 56px/, "a finger-sized strip, the full tuner width");
  assert.match(coarse, /\.toc-tuner__needle::before \{[^}]*width: 12px;[^}]*height: 12px/, "a heavier needle head says it can be grabbed");
  assert.doesNotMatch(coarse, /\.toc-tuner__needle[^{]*\{[^}]*box-shadow:[^}]*(blur|\d+px \d+px \d+px)/, "no glow");
  assert.match(css, /\.toc-tuner\.is-dragging \.toc-tuner__needle \{[^}]*transition: none/, "the needle follows the finger, no spring lag");
  assert.match(css, /\.toc-tuner a\.is-tuning \.toc-tuner__name[^{]*\{[^}]*opacity: 1/);
  assert.match(css, /\.toc-tuner\.is-dragging a:not\(\.is-tuning\) \.toc-tuner__name \{[^}]*opacity: 0/);
  assert.doesNotMatch(TUNER, /aria-(live|label|describedby)|role=|setAttribute\("role/, "no new semantics");
});

test("after a drag, only a click inside the band is swallowed; a click elsewhere goes through", () => {
  const t = rig();
  t.down(150); t.move(200); t.move(450); t.up(450);   // a captured drag, no trailing click yet
  assert.equal(t.nativeClick({ id: "elsewhere" }), true, "an unrelated click (outside the band) is not eaten");
  assert.equal(t.nativeClick(3), false, "the drag's own trailing click on the band still is");
  const u = rig();
  u.down(150); u.move(200); u.move(450); u.up(450);
  u.down(250); u.up(250);                              // the next press clears it
  assert.equal(u.nativeClick(1), true);
});

test("the long-document list form is not a drag surface: its names select and long-press gives the link menu", () => {
  const list = css.match(/@media \(max-width: 760px\) \{[\s\S]*?\n\}/)?.[0] || "";
  assert.match(list, /\.toc-tuner:has\(li:nth-child\(13\)\) \.toc-tuner__band \{[^}]*touch-action: auto;[^}]*user-select: text;[^}]*-webkit-user-select: text;[^}]*-webkit-touch-callout: default/);
  assert.match(list, /\.toc-tuner:has\(li:nth-child\(13\)\) \.toc-tuner__scale \{[^}]*touch-action: auto/, "and its rows scroll the page like any list");
});
