// radiomap.js, round 4: the LED station field inside the homepage's ink zones.
//
// History: a page-wide canvas of timed pings (round 0, "too distracting"), a
// calmed version (round 2, "dialed in too much"), a scroll-driven network of
// stations and LINKS (round 3, "i don't like the lines"). Now: no lines. Each
// ink zone gets a sticky canvas behind its content: a dot-matrix of station
// LEDs (the pattern of the Ping band's grille), and the stations come on air,
// red, as you scroll through the zone - a few at its door, most by its end.
// Still at rest, asleep between scrolls, faint behind the text column.
//
// Runs the REAL script against a mini-DOM with a virtual clock.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import vm from "node:vm";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = readFileSync(path.join(WEB, "src/js/radiomap.js"), "utf8");
const TOKENS = { "--ink-900": "#F3F1EA", "--live": "#FF4438" };
const LIVE = /^rgba\(255,68,56,/;

function makeCtx(log) {
  const ctx = { _fill: "", globalAlpha: 1 };
  Object.defineProperty(ctx, "fillStyle", { get() { return this._fill; }, set(v) { this._fill = v; } });
  for (const fn of ["clearRect", "beginPath", "fill", "stroke", "save", "restore", "rect", "setTransform", "translate", "fillRect", "moveTo", "closePath"]) {
    ctx[fn] = () => { log.calls++; };
  }
  ctx.lineTo = () => { log.calls++; log.lines++; };
  ctx.arc = (x, y, r) => { log.calls++; log.arcs.push({ x, y, r, fill: ctx._fill }); };
  ctx.createPattern = () => { log.calls++; return "pattern"; };
  return ctx;
}
const newLog = () => ({ calls: 0, lines: 0, arcs: [] });

function run({ w = 1440, h = 900, col = [290, 1258], zones = [[1100, 1700]], reduced = false }) {
  let now = 0, rafs = [], rafCalls = 0, nextId = 1;
  const timers = [], listeners = {}, canvases = [];
  const win = {
    innerWidth: w, innerHeight: h, devicePixelRatio: 1, scrollY: 0,
    matchMedia: (q) => ({ matches: reduced && /reduce/.test(q) }),
    addEventListener: (t, f) => { (listeners["w:" + t] ||= []).push(f); },
    requestAnimationFrame: (f) => { rafCalls++; const id = nextId++; rafs.push({ id, f }); return id; },
    cancelAnimationFrame: (id) => { rafs = rafs.filter((r) => r.id !== id); },
    setTimeout: (f, ms) => { const id = nextId++; timers.push({ id, f, at: now + (ms || 0) }); return id; },
    clearTimeout: (id) => { const i = timers.findIndex((t) => t.id === id); if (i >= 0) timers.splice(i, 1); },
    getComputedStyle: (el) => ({ getPropertyValue: (k) => TOKENS[k] || "", paddingLeft: (el && el._pad) || "0px", paddingRight: (el && el._pad) || "0px" }),
  };
  const zoneEls = zones.map(([top, height, onload]) => ({
    _top: top, _height: height, children: [],
    hasAttribute: (k) => k === "data-onload" && !!onload,
    getBoundingClientRect() { return { top: this._top - win.scrollY, height: this._height, bottom: this._top + this._height - win.scrollY }; },
    insertBefore(c) { this.children.unshift(c); return c; },
    get firstChild() { return this.children[0] || null; },
  }));
  const colEl = { getBoundingClientRect: () => ({ left: col[0] - 20, right: col[1] + 20 }), _pad: "20px" };
  const doc = {
    hidden: false,
    querySelectorAll: (s) => (s === ".tone-zone" ? zoneEls : [colEl]),
    createElement: () => {
      const log = newLog();
      const c = { width: 0, height: 0, style: {}, className: "", attrs: {}, log, setAttribute(k, v) { this.attrs[k] = v; }, getContext: () => makeCtx(log) };
      canvases.push(c);
      return c;
    },
    addEventListener: (t, f) => { (listeners[t] ||= []).push(f); },
    documentElement: {},
  };
  win.window = win; win.document = doc; win.performance = { now: () => now }; win.Math = Math; win.parseFloat = parseFloat;
  vm.createContext(win);
  vm.runInContext(SRC, win);
  const tick = () => {
    now += 1000 / 60;
    for (const t of timers.filter((t) => t.at <= now)) { timers.splice(timers.indexOf(t), 1); t.f(); }
    const due = rafs; rafs = [];
    for (const r of due) r.f(now);
  };
  const advance = (ms) => { const end = now + ms; while (now < end) tick(); };
  const scrollTo = (y, frames = 20) => {
    const from = win.scrollY;
    for (let i = 1; i <= frames; i++) {
      win.scrollY = from + ((y - from) * i) / frames;
      for (const f of listeners["w:scroll"] || []) f();
      tick();
    }
  };
  const fire = (t, hidden) => { doc.hidden = hidden; for (const f of listeners[t] || []) f(); };
  const fields = () => canvases.filter((c) => c.className === "tone-field");
  const reset = () => { for (const c of canvases) Object.assign(c.log, newLog()); };
  const calls = () => canvases.reduce((n, c) => n + c.log.calls, 0);
  return { advance, scrollTo, fire, fields, reset, calls, win, stats: () => ({ rafCalls, pending: rafs.length + timers.length }) };
}
const alphaOf = (s) => { const m = /rgba\([^)]*,\s*([\d.]+)\)$/.exec(String(s)); return m ? Number(m[1]) : null; };
const lit = (log) => log.arcs.filter((a) => LIVE.test(a.fill) && alphaOf(a.fill) > 0.05);

test("one decorative LED canvas per ink zone, and not a single line drawn", () => {
  const r = run({ zones: [[1100, 1700], [5500, 2200]] });
  assert.equal(r.fields().length, 2);
  for (const c of r.fields()) assert.equal(c.attrs["aria-hidden"], "true");
  r.scrollTo(1400); r.scrollTo(6000); r.advance(2000);
  assert.equal(r.fields().reduce((n, c) => n + c.log.lines, 0), 0);
});

test("stations come on air as you scroll through a zone", () => {
  const r = run({});
  r.scrollTo(700); r.advance(1500);          // the zone's door is just in view
  const early = lit(r.fields()[0].log).length;
  r.reset(); r.scrollTo(2200); r.advance(1500); // deep into the zone
  const deep = lit(r.fields()[0].log).length;
  assert.ok(deep > early * 2 && deep > 10, `on air: ${early} at the door -> ${deep} deep in`);
});

test("at rest it is still, it only works while you scroll, and it sleeps after", () => {
  const r = run({});
  r.advance(3000);
  assert.equal(r.stats().pending, 0, "nothing queued at load");
  r.scrollTo(1300);
  r.advance(1500);
  assert.equal(r.stats().pending, 0, "asleep within 1.5s of the last scroll");
  r.reset(); r.advance(30000);
  assert.equal(r.calls(), 0, "30s of reading: zero canvas calls");
});

test("a zone that is off screen costs nothing", () => {
  const r = run({ zones: [[6000, 1500]] });
  r.reset(); r.scrollTo(900); r.advance(1500);
  assert.equal(r.calls(), 0);
});

test("faint behind the text column, bright in the margins", () => {
  const r = run({});
  r.scrollTo(2200); r.advance(1500);
  const arcs = lit(r.fields()[0].log);
  const inCol = (a) => a.x > 290 && a.x < 1258;
  const inside = arcs.filter(inCol).map((a) => alphaOf(a.fill));
  const outside = arcs.filter((a) => !inCol(a)).map((a) => alphaOf(a.fill));
  assert.ok(inside.length && outside.length);
  assert.ok(Math.max(...inside) <= 0.35, `behind text peaks at ${Math.max(...inside)}`);
  assert.ok(Math.max(...outside) > Math.max(...inside));
});

test("cheap: a scroll frame stays around a thousand plain canvas calls, even at 1920", () => {
  const r = run({ w: 1920, h: 1080, col: [530, 1498] });
  r.scrollTo(1200); r.reset();
  r.scrollTo(2000, 60);
  // two batched fills; each lit station is a moveTo + arc into one path
  assert.ok(r.calls() / 60 < 1200, `${Math.round(r.calls() / 60)} calls per frame`);
});

test("a hidden tab cancels any frame in flight", () => {
  const r = run({});
  r.scrollTo(1300, 5);
  r.fire("visibilitychange", true);
  assert.equal(r.stats().pending, 0);
});

test("reduced motion: each field is painted once, still, and never reacts", () => {
  const r = run({ reduced: true });
  const painted = r.fields()[0].log.calls;
  assert.ok(painted > 0 && lit(r.fields()[0].log).length > 0, "a still, lit field");
  r.reset(); r.scrollTo(2000); r.advance(1000);
  assert.equal(r.calls(), 0);
  assert.equal(r.stats().rafCalls, 0);
});

test("the hero zone tunes in on load: stations come on over a second or so, then it sleeps", () => {
  const r = run({ zones: [[100, 900, true], [2000, 1600]] });
  const hero = r.fields()[0];
  const oneFrame = () => { r.reset(); r.advance(17); return lit(hero.log).length; };
  r.advance(100);
  const early = oneFrame();                           // ~0.1s in
  r.advance(1250);
  const settled = oneFrame();                         // ~1.4s in: tuned in
  assert.ok(settled > early * 2 && settled > 10, `on load: ${early} -> ${settled}`);
  r.advance(2000);
  assert.equal(r.stats().pending, 0, "asleep once tuned in");
});

test("reduced motion: the hero zone is painted tuned in, with no load animation", () => {
  const r = run({ zones: [[100, 900, true]], reduced: true });
  assert.ok(lit(r.fields()[0].log).length > 10);
  assert.equal(r.stats().rafCalls, 0);
});
