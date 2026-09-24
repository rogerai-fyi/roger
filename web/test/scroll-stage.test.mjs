// The homepage scroll-stage (scroll-stage.js): the three motion variants the
// founder compares in one build, and variant C's snap between sections.
// The snap is the risky part (snapping easily feels hijacked), so its rules
// are pinned here: it only ever finishes a scroll that already came to rest
// near a section, in the direction you were going, on a wide screen with a
// fine pointer, never under reduced motion, never while you type or select.
// Runs the REAL script against a mini-DOM, like the other browser-IIFE tests.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import vm from "node:vm";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = readFileSync(path.join(WEB, "src/js/scroll-stage.js"), "utf8");
before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

function run({ search = "", hash = "", w = 1440, h = 900, fine = true, reduced = false, sections = [0, 900, 1900], navBottom = 65, scrollY = 0, docH = 5000, zones = [] }) {
  const attrs = {};
  const appended = [];
  const listeners = {};
  const scrolls = [];
  const mk = (tag) => ({
    tagName: tag.toUpperCase(), children: [], attrs: {}, textContent: "", className: "", href: "",
    setAttribute(k, v) { this.attrs[k] = String(v); }, appendChild(c) { this.children.push(c); return c; },
  });
  const win = {
    location: { search, hash },
    innerHeight: h, innerWidth: w, scrollY,
    matchMedia: (q) => ({ matches: /reduce/.test(q) ? reduced : /pointer: fine/.test(q) ? fine && w >= 1024 : false }),
    addEventListener: (t, f) => { (listeners[t] ||= []).push(f); },
    setTimeout: (f) => { f(); return 1; }, clearTimeout() {},
    scrollBy: (o) => scrolls.push(o),
    getSelection: () => "",
    onscrollend: null,
  };
  const doc = {
    documentElement: { setAttribute: (k, v) => { attrs[k] = v; }, removeAttribute: (k) => { delete attrs[k]; }, scrollHeight: docH },
    body: { appendChild: (c) => appended.push(c) },
    activeElement: { tagName: "BODY" },
    createElement: mk,
    createTextNode: (t) => ({ text: t }),
    querySelector: (s) => (s === ".nav" ? { getBoundingClientRect: () => ({ bottom: navBottom }) } : null),
    querySelectorAll: (s) => (s === "main > section, .tone-zone > section"
      ? sections.map((top) => ({ getBoundingClientRect: () => ({ top: top - win.scrollY }) }))
      : s === ".tone-zone"
        ? zones.map(([top, height]) => ({ getBoundingClientRect: () => ({ top: top - win.scrollY, bottom: top + height - win.scrollY }) }))
        : []),
  };
  const later = [];
  // the hash target (round 7, 8a) and a ResizeObserver the test can fire by hand
  const aligned = [];
  const target = { scrollIntoView: (o) => aligned.push(o) };
  doc.getElementById = (id) => (hash === "#" + id ? target : null);
  const observers = [];
  win.ResizeObserver = function (cb) { this.cb = cb; this.observe = () => {}; this.disconnect = () => { this.cb = null; }; observers.push(this); };
  const grow = () => { for (const o of observers) if (o.cb) o.cb([]); };
  win.setTimeout = (f) => { later.push(f); return later.length; };   // run by flush()
  win.window = win; win.document = doc; win.URLSearchParams = URLSearchParams;
  vm.createContext(win);
  vm.runInContext(SRC, win);
  const fire = (t) => { for (const f of listeners[t] || []) f({ type: t }); };
  // move the page to `y` (a scroll event), then let it come to rest
  const land = (y) => { win.scrollY = y; fire("scroll"); fire("scrollend"); };
  const scrollTo = (y) => { fire("wheel"); land(y); };          // a wheel / trackpad scroll
  const keyTo = (y) => { fire("keydown"); land(y); };           // PageDown, Space, arrows
  const flush = () => { while (later.length) later.shift()(); };
  const scrollToFlushed = (y) => { scrollTo(y); flush(); };
  return { attrs, appended, scrolls, scrollTo: scrollToFlushed, land: (y) => { land(y); flush(); }, keyTo: (y) => { keyTo(y); flush(); }, fire, grow, aligned, doc, win, flush };
}

test("?motion=a|b|c sets the variant; anything else (or nothing) leaves the page untouched", () => {
  for (const v of ["a", "b", "c"]) assert.equal(run({ search: `?motion=${v}` }).attrs["data-motion"], v);
  assert.equal(run({ search: "?motion=C" }).attrs["data-motion"], "c", "case-insensitive");
  for (const s of ["", "?motion=z", "?motion=<b>", "?utm=x"]) {
    assert.equal(run({ search: s }).attrs["data-motion"], undefined, `${s || "no query"}: no attribute`);
  }
});

test("the lab switcher exists only when the URL asks for it", () => {
  assert.equal(run({}).appended.length, 0, "production: nothing added to the page");
  const lab = run({ search: "?lab=1" }).appended;
  assert.equal(lab.length, 1);
  const links = lab[0].children.filter((c) => c.tagName === "A");
  assert.deepEqual(links.map((a) => a.href), ["?motion=a&lab=1", "?motion=b&lab=1", "?motion=c&lab=1"]);
  const cur = run({ search: "?motion=c" }).appended[0].children.find((c) => c.attrs && c.attrs["aria-current"]);
  assert.match(cur.textContent, /^C/, "the current variant is marked");
});

test("C snaps a section home when a scroll comes to rest just short of it", () => {
  const r = run({ search: "?motion=c" });
  r.scrollTo(780); // heading down; the section at 900 now sits 120-65 = 55px under the nav line
  assert.equal(r.scrolls.length, 1);
  assert.equal(r.scrolls[0].behavior, "smooth");
  assert.equal(r.scrolls[0].top, 900 - 780 - 65);
});

test("C leaves you alone when no section is near, or it would pull against your direction", () => {
  const far = run({ search: "?motion=c" });
  far.scrollTo(500); // next section is 335px below the line: 37% of the viewport away
  assert.equal(far.scrolls.length, 0, "not near a section: no snap");
  const back = run({ search: "?motion=c", scrollY: 300 });
  back.scrollTo(916); // going DOWN, the section at 900 is 81px behind the line: 9% back, over the 8% allowance
  assert.equal(back.scrolls.length, 0, "never drags you back against your scroll");
});

test("C does not snap its own landing, and never snaps past the end of the page", () => {
  const r = run({ search: "?motion=c" });
  r.scrollTo(780);
  r.land(835); // the smooth scroll lands (no wheel): that rest is ours, not the reader's
  assert.equal(r.scrolls.length, 1);
  const end = run({ search: "?motion=c", sections: [0, 4200], docH: 5000 });
  end.scrollTo(4100); // 35px short, but lining it up would need 4135 and the page ends at 4100
  assert.equal(end.scrolls.length, 0);
});

test("the snap is off for A, B and the default, for touch/narrow, reduced motion, and while typing", () => {
  for (const search of ["", "?motion=a", "?motion=b"]) {
    const r = run({ search }); r.scrollTo(780);
    assert.equal(r.scrolls.length, 0, `${search || "default"}: no snap`);
  }
  for (const opts of [{ fine: false }, { w: 900 }, { reduced: true }]) {
    const r = run({ search: "?motion=c", ...opts }); r.scrollTo(780);
    assert.equal(r.scrolls.length, 0, `${JSON.stringify(opts)}: no snap`);
  }
  const typing = run({ search: "?motion=c" });
  typing.doc.activeElement = { tagName: "INPUT" };
  typing.scrollTo(780);
  assert.equal(typing.scrolls.length, 0, "focus in a field: no snap");
});

test("the homepage loads the stage; the choreography CSS is opt-in motion and off for variant A", () => {
  const html = readFileSync(path.join(WEB, "dist/index.html"), "utf8");
  assert.match(html, /<script src="js\/scroll-stage\.js(\?v=[0-9a-f]+)?" defer><\/script>/);
  assert.doesNotMatch(html, /class="motion-lab"/, "the switcher is never in the HTML");
  const css = readFileSync(path.join(WEB, "dist/styles/home.css"), "utf8");
  const chor = css.match(/:root:not\(\[data-motion="a"\]\)[^{]*\{/g) || [];
  assert.ok(chor.length >= 3, "variant B/C rules are scoped away from A");
  // every choreography rule sits after the no-preference guard opens (a cheap structural check;
  // homepage-lift.test.mjs parses the blocks properly for every lift- animation)
  const guard = css.indexOf("prefers-reduced-motion: no-preference");
  assert.ok(guard > 0 && css.indexOf(':root:not([data-motion="a"])') > guard);
});

// ---- adaptive chrome (round 6): the nav, promo and rail take the tone under them ----
// The sticky nav was a white slab over the ink cover. scroll-stage.js now sets
// data-chrome="ink" on <html> whenever an ink zone sits under the nav's bottom edge;
// tokens.css re-themes .nav/.promo/.rail from that. The first set is instant (no fade
// on load), later swaps transition; no JS = the default paper chrome.
test("the chrome goes ink over an ink zone and back to paper over paper", () => {
  const r = run({ zones: [[65, 1000], [2000, 1500]] });   // the cover starts right under the nav
  r.flush();
  assert.equal(r.attrs["data-chrome"], "ink", "on load, over the ink cover");
  r.scrollTo(1100);                                      // paper between the zones
  assert.equal(r.attrs["data-chrome"], undefined);
  r.scrollTo(2100);
  assert.equal(r.attrs["data-chrome"], "ink");
  r.scrollTo(4000);
  assert.equal(r.attrs["data-chrome"], undefined);
});

test("the first chrome swap is instant (no fade-in on load); later ones transition", () => {
  const r = run({ zones: [[65, 1000]] });
  assert.equal(r.attrs["data-chrome"], "ink");
  assert.equal(r.attrs["data-chrome-instant"], "", "transitions held off for the first paint");
  r.flush();
  assert.equal(r.attrs["data-chrome-instant"], undefined, "and released right after");
});

test("tokens and home.css: the chrome re-themes from the attribute, AA by the same ink tokens", () => {
  const tokens = readFileSync(path.join(WEB, "dist/styles/tokens.css"), "utf8");
  assert.match(tokens, /:root\[data-theme="dark"\],\s*\.tone-zone,\s*:root\[data-chrome="ink"\] :is\(\.nav, \.promo, \.rail\)\s*\{/);
  assert.match(tokens, /:root\[data-theme="dark"\] \.tone-zone,\s*:root\[data-theme="dark"\]\[data-chrome="ink"\] :is\(\.nav, \.promo, \.rail\)\s*\{/);
  const home = readFileSync(path.join(WEB, "dist/styles/home.css"), "utf8");
  assert.match(home, /:root\[data-chrome="ink"\] \.nav\s*\{[^}]*background:\s*var\(--paper\)/);
  assert.match(home, /:root\[data-chrome-instant\][^{]*\{[^}]*transition:\s*none/);
});

// ---- round 7 ----
test("C snaps only after a wheel/trackpad scroll: never after PageDown, Space or arrows", () => {
  const key = run({ search: "?motion=c" });
  key.keyTo(780);                         // same resting place as the snapping case above
  assert.equal(key.scrolls.length, 0, "a keyboard scroll lands exactly where the reader sent it");
  const mixed = run({ search: "?motion=c" });
  mixed.fire("wheel"); mixed.fire("keydown"); mixed.land(780);
  assert.equal(mixed.scrolls.length, 0, "a key after the wheel hands control back to the reader");
});

test("every chrome swap is instant, not only the first: no half-faded, unreadable nav", () => {
  const r = run({ zones: [[65, 1000], [2000, 1500]] });
  r.flush();
  r.scrollTo(1100);                        // flushes: check the attribute was set on the swap
  const seen = [];
  const orig = r.doc.documentElement.setAttribute;
  r.doc.documentElement.setAttribute = (k, v) => { seen.push(k); orig(k, v); };
  r.scrollTo(2100);
  assert.ok(seen.includes("data-chrome-instant"), "the paper -> ink swap holds transitions off");
  assert.equal(r.attrs["data-chrome-instant"], undefined, "and releases them right after");
});

test("a page opened on #anchor stays on its target while late content above it loads", () => {
  const r = run({ hash: "#monetize" });
  r.grow();                                // the market rows / the reel grow the page above
  assert.equal(r.aligned.length, 1, "re-aligned after the page grew");
  assert.equal(r.aligned[0].block, "start");
  r.fire("wheel");                         // the reader takes over
  r.grow();
  assert.equal(r.aligned.length, 1, "never after the reader scrolls");
  const none = run({});
  none.grow();
  assert.equal(none.aligned.length, 0, "no hash, nothing to hold");
});
