// anchor-hold.js (was the homepage's scroll-stage.js; round 10 left only the
// #fragment hold, and the design-system pass made it a shared, opt-in module).
// Rounds 3-9 also ran motion variants (?motion=), a wheel-only snap, a sticky
// tuning dial and a pinned stage from here; the founder retired the pinned
// and sticky pieces ("i don't like the pinned thing on the top"), so nothing
// on the page is pinned but the site nav. These tests pin both halves.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import vm from "node:vm";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = readFileSync(path.join(WEB, "src/js/anchor-hold.js"), "utf8");
const dist = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

function run({ hash = "", optIn = true } = {}) {
  const listeners = {}, aligned = [], observers = [];
  const target = { scrollIntoView: (o) => aligned.push(o) };
  const win = {
    location: { hash },
    addEventListener: (t, f) => { (listeners[t] ||= []).push(f); },
    setTimeout: () => 1,
    ResizeObserver: function (cb) { this.cb = cb; this.observe = () => {}; this.disconnect = () => { this.cb = null; }; observers.push(this); },
  };
  win.window = win;
  win.document = { body: {}, getElementById: (id) => (hash === "#" + id ? target : null),
    querySelector: (q) => (q === "[data-anchor-hold]" && optIn ? {} : null) };
  vm.createContext(win);
  vm.runInContext(SRC, win);
  return {
    aligned,
    grow: () => { for (const o of observers) if (o.cb) o.cb([]); },
    fire: (t) => { for (const f of listeners[t] || []) f({ type: t }); },
  };
}

test("a page opened on #anchor stays on its target while late content above it loads", () => {
  const r = run({ hash: "#monetize" });
  r.grow();
  assert.equal(r.aligned.length, 1, "re-aligned after the page grew");
  assert.equal(r.aligned[0].block, "start");
  r.fire("wheel");
  r.grow();
  assert.equal(r.aligned.length, 1, "never after the reader scrolls");
  for (const t of ["keydown", "touchstart", "pointerdown"]) {
    const k = run({ hash: "#monetize" });
    k.fire(t); k.grow();
    assert.equal(k.aligned.length, 0, `${t} hands control to the reader`);
  }
  const none = run({});
  none.grow();
  assert.equal(none.aligned.length, 0, "no hash, nothing to hold");
  const off = run({ hash: "#monetize", optIn: false });
  off.grow();
  assert.equal(off.aligned.length, 0, "a page that does not opt in is never held");
});

test("the homepage opts in to the hold by markup", () => {
  const html = dist("index.html");
  assert.match(html, /<main id="top" data-anchor-hold>/);
  assert.match(html, /<script src="js\/anchor-hold\.js(\?v=[0-9a-f]+)?" defer><\/script>/);
});

test("sections land under the sticky nav, not behind it", () => {
  assert.match(dist("styles/home.css"), /main section\[id\]\s*\{[^}]*scroll-margin-top:\s*72px/);
});

test("nothing pinned or sticky on the homepage but the site nav: no dial, no stage, no variants", () => {
  const html = dist("index.html");
  assert.doesNotMatch(html, /class="dial"|class="stage"|js\/stage\.js|motion-lab/);
  assert.ok(!existsSync(path.join(WEB, "src/js/stage.js")), "stage.js deleted");
  const css = dist("styles/home.css");
  assert.doesNotMatch(css, /\.dial\b|\.stage\b|data-stage|data-motion|motion-lab|--dial-h/);
  assert.doesNotMatch(css, /position:\s*sticky/, "home.css pins nothing (the nav's sticky lives in base.css)");
  assert.doesNotMatch(SRC, /data-motion|scrollBy|\.dial|stage=/);
});
