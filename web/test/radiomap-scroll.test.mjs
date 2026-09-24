// The background blip map (radiomap.js, on index / models / voices) is driven
// by the reader's SCROLL, not a timer.
//
// History: the original field ran 8-24 stations that fired rings on timers and
// sent comets to a receiver behind the headline, ~40 redraws a second forever
// (founder: "too distracting"). Round 2 calmed it to a few margin stations and
// rare timed pings (founder: "dialed in a bit too much ... maybe it works as you
// scroll so it's more intentional"). Round 3: a network of stations and links
// across the page that drifts with the scroll (parallax), draws its links in as
// they arrive, and lights the stations crossing a carrier line while you scroll.
// Still at rest. Faint behind the text column. Asleep when nothing moves.
//
// These run the REAL shipped script against a dependency-free mini-DOM (same
// approach as wave-mark-mobile-spacing / session / fmt tests) with a virtual
// clock, and assert what it DRAWS and when it WAKES.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import vm from "node:vm";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = readFileSync(path.join(WEB, "src/js/radiomap.js"), "utf8");
const css = (p) => readFileSync(path.join(WEB, "src/styles", p), "utf8");
const page = (p) => readFileSync(path.join(WEB, "src", p), "utf8");

const TOKENS = { "--ink-900": "#15140F", "--ink-400": "#9A968B", "--live": "#E0231C" };
const LIVE = /^rgba\(224,35,28,/;

function makeCtx(log) {
  const ctx = { _fill: "", _stroke: "", globalAlpha: 1, lineWidth: 1 };
  Object.defineProperty(ctx, "fillStyle", { get() { return this._fill; }, set(v) { this._fill = v; log.fills.push(v); } });
  Object.defineProperty(ctx, "strokeStyle", { get() { return this._stroke; }, set(v) { this._stroke = v; log.strokes.push(v); } });
  for (const fn of ["clearRect", "beginPath", "fill", "stroke", "save", "restore", "rect", "clip", "setTransform", "translate", "drawImage", "fillRect", "moveTo", "closePath"]) {
    ctx[fn] = () => { log.calls++; };
  }
  ctx.lineTo = () => { log.calls++; log.lines++; };
  ctx.arc = (x, y, r) => { log.calls++; log.arcs.push({ x, y, r, fill: ctx._fill }); };
  ctx.createRadialGradient = () => { log.calls++; return { addColorStop() {} }; };
  ctx.createPattern = () => { log.calls++; return "pattern"; };
  return ctx;
}
function makeCanvas(log) {
  const ctx = makeCtx(log);
  return { width: 0, height: 0, style: {}, getContext: () => ctx };
}
const newLog = () => ({ calls: 0, lines: 0, arcs: [], fills: [], strokes: [] });
const reset = (log) => { log.calls = 0; log.lines = 0; log.arcs = []; log.fills = []; log.strokes = []; };

function run({ w, h, col, spine = 0, docH = 8000, reduced = false }) {
  let now = 0;
  const timers = [];
  let rafs = [], rafCalls = 0, nextId = 1;
  const listeners = {};
  const main = newLog();
  const canvas = makeCanvas(main);
  const colEl = { getBoundingClientRect: () => ({ left: col[0] - 20, right: col[1] + 20, top: 0, bottom: 400 }), _pad: "20px" };
  const doc = {
    hidden: false,
    getElementById: (id) => (id === "blipmap" ? canvas : null),
    querySelectorAll: () => [colEl],
    createElement: () => makeCanvas(newLog()),
    addEventListener: (t, f) => { (listeners[t] ||= []).push(f); },
    documentElement: { scrollHeight: docH },
    body: { _pad: `${spine}px` },
  };
  const win = {
    innerWidth: w, innerHeight: h, devicePixelRatio: 1, scrollY: 0,
    matchMedia: (q) => ({ matches: reduced && /reduce/.test(q), addEventListener() {}, addListener() {} }),
    addEventListener: (t, f) => { (listeners["w:" + t] ||= []).push(f); },
    requestAnimationFrame: (f) => { rafCalls++; const id = nextId++; rafs.push({ id, f }); return id; },
    cancelAnimationFrame: (id) => { rafs = rafs.filter((r) => r.id !== id); },
    setTimeout: (f, ms) => { const id = nextId++; timers.push({ id, f, at: now + (ms || 0) }); return id; },
    clearTimeout: (id) => { const i = timers.findIndex((t) => t.id === id); if (i >= 0) timers.splice(i, 1); },
    getComputedStyle: (el) => ({
      getPropertyValue: (k) => TOKENS[k] || "",
      paddingLeft: (el && el._pad) || "0px", paddingRight: (el && el._pad) || "0px",
    }),
  };
  win.window = win; win.document = doc;
  win.performance = { now: () => now };
  win.Math = Math; win.parseFloat = parseFloat;
  vm.createContext(win);
  vm.runInContext(SRC, win);
  const tick = () => {
    now += 1000 / 60;
    for (const t of timers.filter((t) => t.at <= now)) { timers.splice(timers.indexOf(t), 1); t.f(); }
    const due = rafs; rafs = [];
    for (const r of due) r.f(now);
  };
  const advance = (ms) => { const end = now + ms; while (now < end) tick(); };
  // a steady scroll: `px` per frame for `frames` frames, a scroll event each frame
  const scroll = (px, frames) => {
    for (let i = 0; i < frames; i++) {
      win.scrollY = Math.max(0, Math.min(docH - h, win.scrollY + px));
      for (const f of listeners["w:scroll"] || []) f();
      tick();
    }
  };
  const fire = (t, hidden) => { doc.hidden = hidden; for (const f of listeners[t] || []) f(); };
  return { main, advance, scroll, fire, win, stats: () => ({ rafCalls, pending: rafs.length + timers.length }) };
}

const alphaOf = (s) => { const m = /rgba\([^)]*,\s*([\d.]+)\)$/.exec(String(s)); return m ? Number(m[1]) : null; };
const DESKTOP = { w: 1440, h: 900, col: [290, 1258], spine: 108 };
const PHONE = { w: 390, h: 844, col: [20, 370], spine: 0 };

test("at rest it is still: after load settles, nothing is scheduled and nothing is red", () => {
  const r = run(DESKTOP);
  r.advance(3000);
  assert.equal(r.stats().pending, 0, "no frame or timer queued while nobody scrolls");
  const before = r.stats().rafCalls;
  reset(r.main);
  r.advance(30000);
  assert.equal(r.stats().rafCalls, before, "30s of reading: zero frames");
  assert.equal(r.main.calls, 0, "and zero canvas calls");
});

test("scrolling wakes it, the carrier lights stations red, and it falls asleep soon after", () => {
  const r = run(DESKTOP);
  r.advance(1000);
  reset(r.main);
  r.scroll(12, 60); // one second of brisk scrolling
  assert.ok(r.main.calls > 0, "it draws while you scroll");
  assert.ok(r.main.fills.some((f) => LIVE.test(f) && alphaOf(f) > 0.05), "stations on the carrier light up red");
  r.advance(1500);
  assert.equal(r.stats().pending, 0, "asleep within 1.5s of the last scroll");
  reset(r.main);
  r.advance(100);
  assert.equal(r.main.calls, 0);
});

test("the network is drawn from scroll position: links arrive as you go down", () => {
  const r = run(DESKTOP);
  r.advance(1000);
  // the settled frame at the top vs a settled frame part-way down
  reset(r.main); r.scroll(0, 1); r.advance(1500);
  const topLinks = r.main.lines;
  reset(r.main); r.scroll(20, 150); r.advance(1500);
  const midLinks = r.main.lines;
  assert.ok(topLinks > 0, "some of the network is on air at the top");
  assert.ok(midLinks > topLinks, `more of it is drawn once you have scrolled (${topLinks} -> ${midLinks})`);
});

test("faint behind the text column, present in the margins", () => {
  const r = run(DESKTOP);
  r.advance(500);
  r.scroll(10, 90);
  const inCol = (a) => a.x > DESKTOP.col[0] && a.x < DESKTOP.col[1];
  const inside = r.main.arcs.filter(inCol).map((a) => alphaOf(a.fill)).filter((a) => a !== null);
  const outside = r.main.arcs.filter((a) => !inCol(a)).map((a) => alphaOf(a.fill)).filter((a) => a !== null);
  assert.ok(inside.length && outside.length, "stations on both sides of the column edge");
  assert.ok(Math.max(...inside) <= 0.2, `behind text peaks at ${Math.max(...inside)}`);
  assert.ok(Math.max(...outside) <= 0.7, `margins peak at ${Math.max(...outside)}`);
  assert.ok(Math.max(...outside) > Math.max(...inside), "the margins carry the colour, not the text column");
});

test("cheap: a scroll frame is a few hundred canvas calls, not thousands", () => {
  const r = run({ ...DESKTOP, w: 1920, h: 1080, col: [530, 1498] });
  r.advance(500);
  reset(r.main);
  r.scroll(15, 60);
  assert.ok(r.main.calls / 60 < 700, `${Math.round(r.main.calls / 60)} calls per scroll frame`);
});

test("phones get the network too, and the same rest/scroll behaviour", () => {
  const r = run(PHONE);
  r.advance(3000);
  assert.equal(r.stats().pending, 0);
  reset(r.main);
  r.scroll(10, 40);
  assert.ok(r.main.arcs.length > 0, "stations are drawn on a phone");
});

test("a hidden tab cancels any frame in flight", () => {
  const r = run(DESKTOP);
  r.advance(500);
  r.scroll(12, 10);
  r.fire("visibilitychange", true);
  assert.equal(r.stats().pending, 0, "nothing queued once hidden");
  reset(r.main);
  r.advance(5000);
  assert.equal(r.main.calls, 0);
});

test("reduced motion: one still frame, deaf to scroll, never red", () => {
  const r = run({ ...DESKTOP, reduced: true });
  r.advance(500);
  assert.ok(r.main.arcs.length > 0 && r.main.lines > 0, "the still network is painted");
  assert.ok(!r.main.fills.some((f) => LIVE.test(f)), "no red");
  reset(r.main);
  r.scroll(15, 60);
  r.advance(1000);
  assert.equal(r.main.calls, 0, "scrolling does not animate it");
  assert.equal(r.stats().rafCalls, 0, "no frame loop");
});

test("the still frame IS the reduced-motion state: no hidden canvas, no gradient stand-in", () => {
  const base = css("base.css");
  assert.doesNotMatch(base, /\.blipmap\s*\{\s*display:\s*none/, "the canvas is not hidden under reduced motion");
  assert.doesNotMatch(base, /blipmap-fallback/, "the red-wash fallback is gone");
  for (const p of ["index.html", "models.html", "voices.html"]) {
    assert.match(page(p), /<canvas id="blipmap"/, `${p} keeps the map`);
    assert.doesNotMatch(page(p), /blipmap-fallback/, `${p} drops the fallback div`);
  }
});
