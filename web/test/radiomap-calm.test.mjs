// The background blip map (radiomap.js, on index / models / voices) must be
// AMBIENT, not traffic. Founder 2026-09-23: "the red blips going back and forth,
// sometimes I feel those are too distracting". The old field ran 8-24 stations
// that all breathed, fired rings every few seconds and sent comets to a pulsing
// receiver parked behind the hero headline, redrawn ~40 times a second for as
// long as the page was open, whatever was on screen.
//
// These run the REAL shipped script against a dependency-free mini-DOM (same
// approach as wave-mark-mobile-spacing / session / fmt tests) with a virtual
// clock, and assert what it DRAWS and how often it WAKES UP:
//   - stations live only in the side margins, never behind the text column
//   - few of them, faint, and no station at all where there is no margin
//   - rare, soft pings; idle between them (no frame loop running)
//   - a hidden tab stops everything
//   - reduced motion paints one still frame and never animates
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

const TOKENS = { "--ink-900": "#15140F", "--ink-300": "#C4C0B5", "--ink-400": "#9A968B", "--live": "#E0231C", "--volt": "#15140F", "--ember": "#15140F" };

function makeCtx(log) {
  const ctx = { _fill: "", _stroke: "", globalAlpha: 1, lineWidth: 1 };
  Object.defineProperty(ctx, "fillStyle", { get() { return this._fill; }, set(v) { this._fill = v; log.fills.push(v); } });
  Object.defineProperty(ctx, "strokeStyle", { get() { return this._stroke; }, set(v) { this._stroke = v; log.strokes.push(v); } });
  for (const fn of ["clearRect", "beginPath", "fill", "stroke", "save", "restore", "rect", "clip", "setTransform", "drawImage", "fillRect", "moveTo", "lineTo", "quadraticCurveTo"]) {
    ctx[fn] = (...a) => { log.calls++; if (fn === "drawImage") log.draws++; };
  }
  ctx.arc = (x, y, r) => { log.calls++; log.arcs.push({ x, y, r }); };
  ctx.createRadialGradient = () => { log.calls++; return { addColorStop() {} }; };
  return ctx;
}
function makeCanvas(log) {
  const ctx = makeCtx(log);
  return { width: 0, height: 0, style: {}, getContext: () => ctx, getBoundingClientRect: () => ({ top: 0, bottom: 1, left: 0, right: 1 }) };
}
const newLog = () => ({ calls: 0, draws: 0, arcs: [], fills: [], strokes: [] });

// One page at a viewport. `col` is the text column's content box (what the
// hero / .wrap occupy on the real page at that width), `spine` the fixed rail.
function run({ w, h, col, spine = 0, reduced = false }) {
  let now = 0;
  const timers = [];
  let rafs = [], rafCalls = 0, nextId = 1;
  const listeners = {};
  const main = newLog(), base = newLog();
  const canvas = makeCanvas(main);
  const colEl = {
    getBoundingClientRect: () => ({ left: col[0] - 20, right: col[1] + 20, top: 0, bottom: 400, width: col[1] - col[0] + 40 }),
    _pad: "20px",
  };
  const doc = {
    hidden: false,
    getElementById: (id) => (id === "blipmap" ? canvas : null),
    querySelectorAll: () => [colEl],
    createElement: () => makeCanvas(base),
    addEventListener: (t, f) => { (listeners[t] ||= []).push(f); },
    documentElement: {},
    body: { _pad: `${spine}px` },
  };
  const win = {
    innerWidth: w, innerHeight: h, devicePixelRatio: 1,
    matchMedia: (q) => ({ matches: reduced && /reduce/.test(q), addEventListener() {}, addListener() {} }),
    addEventListener: (t, f) => { (listeners["w:" + t] ||= []).push(f); },
    requestAnimationFrame: (f) => { rafCalls++; const id = nextId++; rafs.push({ id, f }); return id; },
    cancelAnimationFrame: (id) => { rafs = rafs.filter((r) => r.id !== id); },
    setTimeout: (f, ms) => { const id = nextId++; timers.push({ id, f, at: now + (ms || 0) }); return id; },
    clearTimeout: (id) => { const i = timers.findIndex((t) => t.id === id); if (i >= 0) timers.splice(i, 1); },
    getComputedStyle: (el) => ({
      getPropertyValue: (k) => TOKENS[k] || "",
      paddingLeft: el && el._pad || "0px", paddingRight: el && el._pad || "0px",
    }),
  };
  win.window = win; win.document = doc;
  win.performance = { now: () => now };
  win.Math = Math; win.parseFloat = parseFloat;
  vm.createContext(win);
  vm.runInContext(SRC, win);
  // advance the virtual clock in 1/60s steps, firing timers and frames
  const advance = (ms) => {
    const end = now + ms;
    while (now < end) {
      now += 1000 / 60;
      for (const t of timers.filter((t) => t.at <= now)) { timers.splice(timers.indexOf(t), 1); t.f(); }
      const due = rafs; rafs = [];
      for (const r of due) r.f(now);
    }
  };
  const fire = (t, hidden) => { doc.hidden = hidden; for (const f of listeners[t] || []) f(); };
  return { main, base, advance, fire, stats: () => ({ rafCalls, pending: rafs.length + timers.length }) };
}

const alphaOf = (s) => { const m = /rgba\([^)]*,\s*([\d.]+)\)$/.exec(String(s)); return m ? Number(m[1]) : null; };
const stationArcs = (log) => log.arcs.filter((a) => a.r > 1); // grid dots are r <= 1

const DESKTOP = { w: 1440, h: 900, col: [180, 1352], spine: 108 };
const WIDE = { w: 1920, h: 1080, col: [420, 1608], spine: 108 };
const PHONE = { w: 390, h: 844, col: [20, 370], spine: 0 };

test("stations and pings live in the margins, never behind the text column", () => {
  for (const vp of [DESKTOP, WIDE]) {
    const r = run(vp);
    r.advance(30000);
    const all = [...stationArcs(r.base), ...r.main.arcs];
    assert.ok(all.length > 0, `${vp.w}: something is drawn`);
    for (const a of all) {
      assert.ok(a.x < vp.col[0] - 8 || a.x > vp.col[1] + 8,
        `${vp.w}: a station or ring centred at x=${Math.round(a.x)} sits inside the text column ${vp.col}`);
      assert.ok(a.x > vp.spine, `${vp.w}: nothing is drawn on the rail`);
    }
  }
});

test("few stations: at most 10 on any screen, none where there is no margin", () => {
  const count = (vp) => { const r = run(vp); r.advance(100); return stationArcs(r.base).length; };
  assert.ok(count(DESKTOP) >= 2 && count(DESKTOP) <= 10, `1440: ${count(DESKTOP)} stations`);
  assert.ok(count(WIDE) <= 10, `1920: ${count(WIDE)} stations`);
  const phone = run(PHONE);
  phone.advance(30000);
  assert.equal(stationArcs(phone.base).length, 0, "a phone has no margin, so no stations");
  assert.equal(phone.main.arcs.length, 0, "and no pings");
  assert.equal(phone.stats().rafCalls, 0, "and nothing animates");
});

test("faint: station dots and rings stay under their opacity caps", () => {
  const r = run(DESKTOP);
  r.advance(30000);
  const fills = [...r.base.fills, ...r.main.fills].map(alphaOf).filter((a) => a !== null);
  const strokes = r.main.strokes.map(alphaOf).filter((a) => a !== null);
  assert.ok(fills.length && strokes.length, "fills and ring strokes were drawn");
  assert.ok(Math.max(...fills) <= 0.5, `fill alpha peaks at ${Math.max(...fills)}`);
  assert.ok(Math.max(...strokes) <= 0.14, `ring alpha peaks at ${Math.max(...strokes)}`);
});

test("rare pings, idle in between: the frame loop only runs while a ring is on air", () => {
  const r = run(DESKTOP);
  r.advance(30000);
  const frames = r.main.draws; // one base blit per painted frame
  assert.ok(frames > 0, "it does ping");
  // 30s at 60fps would be 1800 frames; the old field painted ~1200 (40fps, always).
  assert.ok(frames < 30000 / 1000 * 60 * 0.35, `${frames} frames in 30s - it should be idle most of the time`);
  assert.ok(r.main.calls < 6000, `${r.main.calls} canvas calls in 30s (the old field made ~1.1M)`);
});

test("a hidden tab stops everything, and it resumes when visible", () => {
  const r = run(DESKTOP);
  r.advance(2000);
  r.fire("visibilitychange", true);
  const before = { calls: r.main.calls, raf: r.stats().rafCalls };
  r.advance(30000);
  assert.equal(r.main.calls, before.calls, "no drawing while hidden");
  assert.equal(r.stats().rafCalls, before.raf, "no frames requested while hidden");
  assert.equal(r.stats().pending, 0, "no timer or frame left queued while hidden");
  r.fire("visibilitychange", false);
  r.advance(30000);
  assert.ok(r.main.calls > before.calls, "pings resume once the tab is visible");
});

test("reduced motion paints one still frame and never animates", () => {
  const r = run({ ...DESKTOP, reduced: true });
  r.advance(30000);
  assert.ok(r.main.draws >= 1, "the still map is painted (grid + stations)");
  assert.ok(stationArcs(r.base).length > 0, "stations are part of the still frame");
  assert.equal(r.stats().rafCalls, 0, "no frame loop");
  assert.equal(r.main.strokes.length, 0, "no rings");
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
