// The TOC tuner, drag and tap. The scale strip (ticks, needle, numerals) is the drag zone
// (touch-action: none): a touch that starts there always tunes, whatever its angle.
// On TOUCH it is two steps (founder: "it shouldn't automatically scroll like that"): the
// first tap or drag only SELECTS - the needle lands on the station, the readout names it,
// a hint "Tap again to tune in" fades in, and the page does not move. A second tap on the
// selected station, on the readout or on the hint goes there; a tap or drag to another
// station moves the selection; scrolling, a tap outside the tuner or 6s of nothing clears
// it. A mouse and the keyboard are unchanged (a click goes straight there; a mouse drag
// jumps on release), and assistive tech activates the links directly: only a touch
// pointer gets two steps. The long-document list form (13+ stations under 760px) is
// plain tappable rows. This drives the real tuner.js in a small fake DOM.
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
function rig({ reduced = false, listForm = false, coarse = true } = {}) {
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
  const band = on({ setPointerCapture: (id) => captured.push(id), releasePointerCapture: () => {}, hasPointerCapture: () => captured.length > 0,
    kids: [], appendChild(c) { this.kids.push(c); return c; } });
  const readout = on({ cls: "toc-tuner__readout" });
  readout.contains = (t) => t === readout;
  const scale = on({ getBoundingClientRect: () => ({ left: 100, width: 500, right: 600 }) });
  band.contains = (t) => t === band || t === scale || t === readout || band.kids.includes(t) || links.includes(t);
  const inNav = (t) => band.contains(t);
  scale.setPointerCapture = band.setPointerCapture;
  const needle = { listForm };
  nav.querySelectorAll = (q) => (q === "a" ? links : []);
  nav.querySelector = (q) => ({ ".toc-tuner__band": band, ".toc-tuner__scale": scale, ".toc-tuner__needle": needle, ".toc-tuner__readout": readout }[q] || null);
  nav.contains = inNav;
  const vibes = [];
  const win = {
    innerHeight: 1000,
    matchMedia: (q) => ({ matches: /reduce/.test(q) ? reduced : /coarse/.test(q) ? coarse : false }),
    getComputedStyle: (el) => ({ display: el === needle && listForm ? "none" : "block" }),
    navigator: { vibrate: (ms) => { vibes.push(ms); return true; } },
    // timers fire only when a test says time has passed (the trailing click comes first, as in a browser)
    setTimeout: (f, ms) => { timers.push({ f, ms, id: timers.length + 1 }); return timers.length; },
    clearTimeout: (id) => { const x = timers.find((q) => q.id === id); if (x) x.f = null; },
  };
  const timers = [];
  on(win);
  win.window = win;
  const doc = on({ querySelectorAll: (q) => (q === "[data-tuner]" ? [nav] : []), getElementById: () => null,
    createElement: (tag) => { const el = on({ tag, cls: "", attrs: {}, textContent: "", setAttribute(a, v) { this.attrs[a] = String(v); } });
      Object.defineProperty(el, "className", { get() { return this.cls; }, set(v) { this.cls = v; } }); return el; } });
  win.document = doc;
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
  // a click (from a tap) on the readout or the hint: through the nav's capture listeners
  const clickOn = (el) => nativeClick(el);
  const hint = () => band.kids.find((k) => /toc-tuner__hint/.test(k.cls));
  const wait = (ms) => { for (const q of timers.splice(0)) { if (q.f && q.ms <= ms) q.f(); else if (q.f) timers.push(q); } };
  const scroll = (dy = 200) => { win.pageYOffset = (win.pageYOffset || 0) + dy; fire(win, "scroll", {}); };
  const tapElsewhere = () => fire(doc, "pointerdown", { target: { id: "page" } });
  const releaseOutside = (o) => fire(win, "pointerup", ev(50, 400, { target: { id: "page" }, ...o }));
  const resize = () => fire(win, "resize", {});
  const key = (k) => fire(nav, "keydown", { key: k, target: links[0] });
  return { nav, links, style, clicks, captured, vibes, down, move, up, cancel, outside, nativeClick, readout, hint, clickOn, wait, scroll, tapElsewhere, releaseOutside, resize, key };
}

const at = (t) => Number(t.style["--at"]);

const mouse = { pointerType: "mouse" };
const selected = (t) => t.nav.cls.has("is-selected");

test("touch drag: the needle follows the finger and the readout names the station under it", () => {
  const t = rig();
  t.down(150);                       // on station 1: the needle jumps to the finger at once
  assert.ok(t.nav.cls.has("is-dragging"));
  assert.equal(at(t), 0);
  assert.ok(t.links[0].cls.has("is-tuning"));
  t.move(170);                       // past the slop: a drag
  assert.deepEqual(t.captured, [7], "the scale captures the pointer");
  assert.ok(Math.abs(at(t) - 0.2) < 1e-9, `needle at ${at(t)}`);   // (170-100)/100 - 0.5
  t.move(365);                       // over station 3
  assert.ok(Math.abs(at(t) - 2.15) < 1e-9);
  assert.ok(t.links[2].cls.has("is-tuning") && !t.links[0].cls.has("is-tuning"), "one station tunes at a time");
  t.move(9999);                      // past the end: clamped to the last station
  assert.equal(at(t), 4);
});

test("touch drag: letting go SELECTS the station (needle on it, name in the readout, hint in); nothing navigates", () => {
  const t = rig();
  t.down(150); t.move(200); t.move(450); t.up(450);
  assert.deepEqual(t.clicks, []);
  assert.ok(!t.nav.cls.has("is-dragging") && selected(t));
  assert.equal(t.style["--at"], "3", "the needle rests on the selection");
  assert.ok(t.links[3].cls.has("is-tuning"), "the readout names it");
  assert.equal(t.hint().textContent, "Tap again to tune in");
  assert.equal(t.hint().attrs["aria-hidden"], "true", "the hint is decoration for sighted touch readers");
  assert.equal(t.nativeClick(3), false, "the browser's own click at the end is swallowed");
});

test("touch tap: the first tap selects without navigating; a second tap on it tunes in", () => {
  const t = rig();
  t.down(250); t.up(250);
  assert.deepEqual(t.clicks, []);
  assert.equal(t.nativeClick(1), false, "the link's own jump is held back");
  assert.ok(selected(t) && t.links[1].cls.has("is-tuning"));
  t.down(252); t.up(252);
  assert.deepEqual(t.clicks, ["s2"], "the second tap goes there");
  assert.equal(t.style["--cur"], "1");
  assert.equal(t.links[1].attrs["aria-current"], "location");
  assert.ok(!selected(t) && !t.links[1].cls.has("is-tuning"), "the selection clears");
  assert.equal(t.style["--at"], undefined, "the needle hands back to the section in view");
});

test("touch: a tap on the readout or on the hint tunes in to the selected station", () => {
  const t = rig();
  t.down(350); t.up(350);
  assert.equal(t.clickOn(t.readout), false, "the readout's click is the tuner's");
  assert.deepEqual(t.clicks, ["s3"]);
  const u = rig();
  u.down(150); u.up(150);
  u.clickOn(u.hint());
  assert.deepEqual(u.clicks, ["s1"]);
  const v = rig();
  v.clickOn(v.readout);
  assert.deepEqual(v.clicks, [], "with nothing selected the readout does nothing");
});

test("touch: a tap or drag to another station moves the selection; the hint stays", () => {
  const t = rig();
  t.down(150); t.up(150);
  t.down(450); t.up(450);
  assert.deepEqual(t.clicks, []);
  assert.ok(selected(t) && t.links[3].cls.has("is-tuning") && !t.links[0].cls.has("is-tuning"));
  t.down(460); t.move(480); t.move(250); t.up(250);   // a drag onto station 2: moves it, never navigates
  assert.deepEqual(t.clicks, []);
  assert.ok(selected(t) && t.links[1].cls.has("is-tuning"));
  t.down(250); t.move(270); t.move(240); t.up(240);   // a drag that ends on the selected station: still no jump
  assert.deepEqual(t.clicks, []);
});

test("touch: ignoring the selection clears it (6s, a page scroll, a tap outside the tuner)", () => {
  for (const clear of [(t) => t.wait(6000), (t) => t.scroll(), (t) => t.tapElsewhere()]) {
    const t = rig();
    t.down(350); t.up(350);
    assert.ok(selected(t));
    clear(t);
    assert.ok(!selected(t), "cleared");
    assert.equal(t.style["--at"], undefined, "the needle returns to the section in view");
    assert.ok(t.links.every((a) => !a.cls.has("is-tuning")));
    assert.deepEqual(t.clicks, []);
  }
  const t = rig();
  t.down(350); t.up(350); t.wait(3000);
  assert.ok(selected(t), "not before its time");
  t.scroll(6);
  assert.ok(selected(t), "a few pixels of layout settling above the tuner is not the reader scrolling");
});

test("cancel (the browser took the gesture) restores the needle", () => {
  const t = rig();
  t.down(150); t.move(300); t.cancel();
  assert.deepEqual(t.clicks, []);
  assert.ok(!t.nav.cls.has("is-dragging") && !selected(t));
  assert.equal(t.style["--at"], undefined);
  assert.ok(t.links.every((a) => !a.cls.has("is-tuning")));
});

test("inside the scale there is no direction rule: a steep thumb drag still tunes", () => {
  const t = rig();
  t.down(150, 50); t.move(158, 70); t.move(210, 110); t.move(265, 160);   // ~45 degrees and more
  assert.ok(t.nav.cls.has("is-dragging"));
  assert.deepEqual(t.captured, [7]);
  t.up(265, 160);
  assert.ok(selected(t) && t.links[1].cls.has("is-tuning"));
});

test("a gesture that starts outside the scale (the readout) is the page's: it never tunes", () => {
  const t = rig();
  t.outside(150, 10);
  assert.ok(!t.nav.cls.has("is-dragging"));
  assert.equal(t.style["--at"], undefined);
  assert.deepEqual(t.captured, []);
});

test("mouse: a click goes straight there (the link's own), a drag jumps on release; no selection, no hint", () => {
  const t = rig({ coarse: false });
  t.down(250, 50, mouse); t.up(250, 50, mouse);
  assert.ok(!selected(t));
  assert.equal(t.nativeClick(1), true, "the click's own navigation goes through");
  assert.ok(t.hint() && !selected(t), "the hint exists on any device (a touch laptop), shown only by a touch selection");
  const d = rig({ coarse: false });
  d.down(150, 50, mouse); d.move(200, 50, mouse); d.move(450, 50, mouse); d.up(450, 50, mouse);
  assert.deepEqual(d.clicks, ["s4"]);
  assert.equal(d.nativeClick(3), false, "its trailing click is not a second jump");
  const b = rig({ coarse: false });
  b.down(150, 50, mouse); b.move(260, 50, mouse); b.move(140, 50, mouse); b.up(140, 50, mouse);
  assert.deepEqual(b.clicks, [], "a mouse drag back to its start does nothing");
});

test("keyboard Enter and assistive tech activate a link in one step", () => {
  const t = rig();
  assert.equal(t.nativeClick(2), true, "a click with no touch before it (Enter, a screen reader) navigates");
  t.down(350); t.up(350); t.wait(500);            // even after a touch selection has settled
  t.nativeClick(3);
  assert.equal(t.nativeClick(4), true, "a later keyboard click still goes through");
});

test("touch ticks once per station passed; never with reduced motion, never for a mouse", () => {
  const t = rig();
  t.down(150); t.move(170); t.move(260); t.move(290); t.move(360);
  assert.deepEqual(t.vibes, [8, 8], "a tick entering station 2 and station 3 (not on the press itself)");
  const m = rig();
  m.down(150, 50, mouse); m.move(170, 50, mouse); m.move(360, 50, mouse);
  assert.deepEqual(m.vibes, []);
  assert.ok(m.nav.cls.has("is-dragging"), "a mouse drags too");
  const r = rig({ reduced: true });
  r.down(150); r.move(170); r.move(360);
  assert.deepEqual(r.vibes, []);
  r.move(365);
  assert.equal(at(r), 2, "reduced motion: the needle jumps station to station");
});

test("the long-document list form (no needle shown) is not dragged or selected: its rows are tapped", () => {
  const t = rig({ listForm: true });
  t.down(150); t.move(300); t.up(300);
  assert.ok(!t.nav.cls.has("is-dragging") && !selected(t));
  assert.deepEqual(t.clicks, []);
  assert.equal(t.nativeClick(2), true);
});

test("a secondary mouse button or a second finger does not tune", () => {
  const t = rig();
  t.down(150, 50, { pointerType: "mouse", button: 2 }); t.move(300, 50, { pointerType: "mouse", button: 2 });
  assert.ok(!t.nav.cls.has("is-dragging"));
  const u = rig();
  u.down(150, 50, { isPrimary: false }); u.move(300, 50, { isPrimary: false });
  assert.ok(!u.nav.cls.has("is-dragging"));
});

test("the hint: a mono label that fades in under the readout (instant under reduced motion), never red", () => {
  assert.match(css, /\.toc-tuner__hint \{[^}]*font-family: var\(--font-mono\)[^}]*color: var\(--ink-500\)[^}]*opacity: 0/);
  assert.match(css, /\.toc-tuner\.is-selected \.toc-tuner__hint \{[^}]*opacity: 1/);
  assert.match(css, /\.toc-tuner__hint \{[^}]*transition: opacity 250ms var\(--ease-out-quint\), transform 250ms var\(--ease-out-quint\)/);
  assert.match(css, /@media \(prefers-reduced-motion: reduce\) \{[^@]*\.toc-tuner__hint \{ transition: none; \}/);
  assert.doesNotMatch(css, /\.toc-tuner__hint[^{]*\{[^}]*(--live|box-shadow)/, "no red wash, no glow");
  assert.match(css, /\.toc-tuner:is\(\.is-dragging, \.is-selected\) a:not\(\.is-tuning\) \.toc-tuner__name \{[^}]*opacity: 0/, "the readout names only the tuned station");
});

test("the scale is the drag zone: touch-action none there, finger-sized on touch, with a grip; the readout still scrolls", () => {
  assert.match(css, /\.toc-tuner__band:has\(\.toc-tuner__hint\) \.toc-tuner__scale \{ touch-action: none; \}/, "only once the script runs (the hint is its mark)");
  assert.doesNotMatch(css, /(^|\n)\.toc-tuner__scale \{[^}]*touch-action: none/, "never on the bare scale: without JS the strip must scroll and zoom");
  assert.match(css, /\.toc-tuner__band \{[^}]*touch-action: pan-y/, "outside the scale the page scrolls");
  assert.match(css, /\.toc-tuner__band \{[^}]*user-select: none/);
  const coarse = [...css.matchAll(/@media \(any-pointer: coarse\) \{([\s\S]*?)\n\}/g)].map((m) => m[1]).join("\n");
  assert.match(coarse, /\.toc-tuner__scale \{[^}]*min-height: 56px/, "a finger-sized strip, the full tuner width");
  assert.match(coarse, /\.toc-tuner__needle::before \{[^}]*width: 12px;[^}]*height: 12px/, "a heavier needle head says it can be grabbed");
  assert.doesNotMatch(coarse, /\.toc-tuner__needle[^{]*\{[^}]*box-shadow:[^}]*(blur|\d+px \d+px \d+px)/, "no glow");
  assert.match(css, /\.toc-tuner\.is-dragging \.toc-tuner__needle \{[^}]*transition: none/, "the needle follows the finger, no spring lag");
  assert.match(css, /\.toc-tuner a\.is-tuning \.toc-tuner__name[^{]*\{[^}]*opacity: 1/);
  assert.doesNotMatch(TUNER, /aria-(live|label|describedby)|role=|setAttribute\("role/, "no new semantics");
});

test("after a touch gesture, only a click inside the tuner is held back; a click elsewhere goes through", () => {
  const t = rig();
  t.down(150); t.move(200); t.move(450); t.up(450);
  assert.equal(t.nativeClick({ id: "elsewhere" }), true, "an unrelated click (outside the band) is not eaten");
  assert.equal(t.nativeClick(3), false, "the gesture's own trailing click on the band is");
});

test("the long-document list form is not a drag surface: its names select and long-press gives the link menu", () => {
  const list = css.match(/@media \(max-width: 760px\) \{[\s\S]*?\n\}/)?.[0] || "";
  assert.match(list, /\.toc-tuner:has\(li:nth-child\(13\)\) \.toc-tuner__band \{[^}]*touch-action: auto;[^}]*user-select: text;[^}]*-webkit-user-select: text;[^}]*-webkit-touch-callout: default/);
  assert.match(list, /\.toc-tuner:has\(li:nth-child\(13\)\) \.toc-tuner__scale \{[^}]*touch-action: auto/, "and its rows scroll the page like any list");
});

test("touch: the needle head (the grip) sits inside the scale's hit area, so pressing it drags", () => {
  const coarse = [...css.matchAll(/@media \(any-pointer: coarse\) \{([\s\S]*?)\n\}/g)].map((m) => m[1]).join("\n");
  const band = Number(css.match(/\.toc-tuner__band \{[^}]*padding-top: (\d+)px/)[1]);             // the scale starts here
  const needleTop = Number(css.match(/\.toc-tuner__needle \{[^}]*top: (\d+)px/)[1]);
  const head = coarse.match(/\.toc-tuner__needle::before \{[^}]*height: (\d+)px;[^}]*top: (-?\d+)px/);
  const headTop = needleTop + Number(head[2]);
  const scale = coarse.match(/\.toc-tuner__scale \{[^}]*margin-top: -(\d+)px;[^}]*padding-top: calc\(26px \+ (\d+)px\);[^}]*background-position: 0 (\d+)px/);
  assert.ok(scale, "on touch the scale reaches up (margin-top) and keeps its content in place (padding-top, ticks)");
  const [, up, pad, ticks] = scale.map(Number);
  assert.equal(pad, up, "the numerals do not move");
  assert.equal(ticks, 6 + up, "the ticks do not move");
  assert.ok(band - up <= headTop - 3, `the drag zone starts at ${band - up}px, the needle head at ${headTop}px`);
  assert.match(coarse, /\.toc-tuner__scale \{[^}]*min-height: 56px/);
});

test("touch: a keyboard or other click on a link after a selection clears it and marks the station current", () => {
  const t = rig();
  t.down(350); t.up(350); t.wait(500);              // selected; the gesture's own click window has passed
  assert.ok(selected(t));
  assert.equal(t.nativeClick(3), true, "the click navigates");
  assert.ok(!selected(t), "the selection and hint do not linger");
  assert.equal(t.style["--at"], undefined);
  assert.equal(t.style["--cur"], "3");
  assert.equal(t.links[3].attrs["aria-current"], "location");
});

test("the header comment describes the touch flow (select, then tap again)", () => {
  const head = TUNER.slice(0, TUNER.indexOf("(function"));
  assert.doesNotMatch(head, /letting\s+go on another station follows/);
  assert.match(head, /first tap or drag only selects/);
  assert.match(head, /second tap/);
});

test("a mouse or pen press that leaves the scale before it drags, and is released off it, still ends", () => {
  const t = rig({ coarse: false });
  t.down(150, 50, mouse);                 // is-dragging and the needle start at once
  assert.ok(t.nav.cls.has("is-dragging"));
  t.releaseOutside(mouse);                // no pointerup on the scale: the window hears it
  assert.ok(!t.nav.cls.has("is-dragging"), "the drag state ends");
  assert.equal(t.style["--at"], undefined, "the needle hands back to the stylesheet");
  assert.ok(t.links.every((a) => !a.cls.has("is-tuning")));
  assert.deepEqual(t.clicks, []);
});

test("hybrid devices: the hint exists without a coarse primary pointer; the tall strip keys on any coarse pointer", () => {
  const t = rig({ coarse: false });
  t.down(250); t.up(250);                 // a finger on a touch laptop whose primary pointer is fine
  assert.ok(selected(t));
  assert.equal(t.hint().textContent, "Tap again to tune in", "the selection has its hint");
  assert.match(css, /@media \(any-pointer: coarse\) \{[^@]*\.toc-tuner__scale \{[^}]*min-height: 56px/);
  assert.match(css, /@media \(any-pointer: coarse\) \{[^@]*\.toc-tuner__band:has\(\.toc-tuner__hint\) \{ padding-bottom: 36px; \}/, "room for the hint only where touch can select");
  assert.doesNotMatch(css.replace(/@media[^{]*\{(?:[^{}]*\{[^}]*\})*[^{}]*\}/g, ""), /\.toc-tuner__band:has\(\.toc-tuner__hint\) \{ padding-bottom/, "not reserved on a mouse-only desktop");
});

test("a touch screen's sticky :hover never steers the needle or the readout once JS rests it", () => {
  const block = (q) => css.match(new RegExp(`@media \\(hover: none\\)${q} \\{([\\s\\S]*?)\\n\\}`))?.[1] || "";
  assert.match(block(""), /\.toc-tuner\.toc-tuner\.toc-tuner:has\(a\.is-current\):not\(:has\(a:focus-visible\)\) \{ --at: var\(--cur, 0\); \}/, "the needle rests on the section in view");
  for (const q of [" and \\(min-width: 761px\\)", " and \\(max-width: 760px\\)"]) {
    const b = block(q);
    assert.match(b, /a:not\(\.is-current\):not\(\.is-tuning\) \.toc-tuner__name \{[^}]*opacity: 0/, "a stuck-hovered name hides");
    assert.match(b, /a\.is-current \.toc-tuner__name \{[^}]*opacity: 1/, "the current name shows");
  }
});

test("the sticky-hover fix never reaches the long-document list form (its rows keep their own name style)", () => {
  // every hover:none rule that styles a station name, where the list form exists (under
  // 760px), excludes it; above 760px there is no list form
  const narrow = css.match(/@media \(hover: none\) and \(max-width: 760px\) \{([\s\S]*?)\n\}/)?.[1] || "";
  const rules = [...narrow.matchAll(/([^{}]+)\{[^}]*\}/g)].map((m) => m[1].trim()).filter((sel) => /toc-tuner__name/.test(sel));
  assert.ok(rules.length >= 2);
  for (const sel of rules) assert.match(sel, /:not\(:has\(li:nth-child\(13\)\)\)/, sel);
  const plain = css.match(/@media \(hover: none\) \{([\s\S]*?)\n\}/)?.[1] || "";
  assert.doesNotMatch(plain, /toc-tuner__name/, "no unscoped hover:none rule styles a name");
});

test("the hint is described as it is: added on any device, shown only by a touch selection", () => {
  const ds = readFileSync(path.join(WEB, "DESIGN-SYSTEM.md"), "utf8");
  const raw = readFileSync(path.join(WEB, "src/styles/components.css"), "utf8");
  for (const [name, text] of [["DESIGN-SYSTEM.md", ds], ["components.css", raw]]) {
    assert.doesNotMatch(text.replace(/\s+/g, " "), /coarse pointer only/, name);
    assert.match(text.replace(/\s+/g, " "), /added by the script on any device; shown only by a touch selection/, name);
  }
});

test("the long-document list form: every row's name is readable (no blur, full opacity, its own tracking), hovered or not", () => {
  const list = css.match(/@media \(max-width: 760px\) \{[\s\S]*?\n\}/)?.[0] || "";
  const rule = list.match(/\.toc-tuner\.toc-tuner:has\(li:nth-child\(13\)\) a \.toc-tuner__name \{([^}]*)\}/)?.[1] || "";
  assert.ok(rule, "the list rule outranks the tuner-state rules (.toc-tuner:is(.is-dragging, .is-selected) a:not(.is-tuning) .toc-tuner__name, 0,4,1)");
  assert.match(rule, /opacity: 1 !important/);
  assert.match(rule, /filter: none !important/, "the scale form's blur (a name coming into tune) is not a list's");
  assert.match(rule, /letter-spacing: 0\.04em !important/, "nor its wide tracking while out of tune");
});

test("a resize or rotation (the list form may now show) clears a pending selection or drag", () => {
  const t = rig();
  t.down(350); t.up(350);
  assert.ok(selected(t));
  t.resize();
  assert.ok(!selected(t) && !t.nav.cls.has("is-dragging"));
  assert.equal(t.style["--at"], undefined);
  const d = rig();
  d.down(150); d.move(300);
  d.resize();
  assert.ok(!d.nav.cls.has("is-dragging"));
  d.up(300);
  assert.ok(!selected(d), "and the finger's release after it selects nothing");
});

test("a pen is one step, like a mouse: a tap is the link's own click, a drag jumps on release", () => {
  const pen = { pointerType: "pen" };
  const t = rig();
  t.down(250, 50, pen); t.up(250, 50, pen);
  assert.ok(!selected(t));
  assert.equal(t.nativeClick(1), true);
  const d = rig();
  d.down(150, 50, pen); d.move(200, 50, pen); d.move(450, 50, pen); d.up(450, 50, pen);
  assert.deepEqual(d.clicks, ["s4"]);
  assert.ok(!selected(d));
});

test("a modified click (ctrl, cmd, shift: a new tab or window) is the browser's", () => {
  for (const mod of ["ctrlKey", "metaKey", "shiftKey"]) {
    const t = rig();
    t.down(250, 50, { pointerType: "mouse", [mod]: true });
    assert.ok(!t.nav.cls.has("is-dragging") && t.style["--at"] === undefined, `${mod}: the press does not take the needle`);
    t.move(400, 50, { pointerType: "mouse", [mod]: true });
    assert.deepEqual(t.captured, [], `${mod}: nor drag it`);
    t.up(400, 50, { pointerType: "mouse", [mod]: true });
    assert.ok(!t.nav.cls.has("is-dragging") && !selected(t), mod);
    assert.equal(t.style["--at"], undefined, mod);
    assert.equal(t.nativeClick(1), true, `${mod}: the click goes through untouched`);
    assert.deepEqual(t.clicks, []);
  }
});

test("a cancelled gesture during a pending selection puts the selection back (it is not dropped)", () => {
  const t = rig();
  t.down(150); t.up(150);                 // station 1 selected
  t.down(450); t.move(470);               // a new gesture over station 4...
  assert.ok(t.links[3].cls.has("is-tuning"));
  t.cancel();                             // ...that the browser takes
  assert.ok(selected(t), "still selected");
  assert.ok(t.links[0].cls.has("is-tuning") && !t.links[3].cls.has("is-tuning"));
  assert.equal(t.style["--at"], "0");
});

test("hybrid touch and keyboard: a key inside the tuner clears a pending touch selection", () => {
  const t = rig();
  t.down(350); t.up(350);
  t.key("Tab");
  assert.ok(!selected(t));
  assert.equal(t.style["--at"], undefined, "the needle follows the keyboard, not the stale selection");
});
