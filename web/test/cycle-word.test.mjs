// The cycle word (components.css .cycle-word + js/cycle.js): ONE implementation of
// "a word that tunes to the next station", shared by the nav's App / TUI / WebUI item and
// the homepage headline ("Tune in to the world's [decentralized | specialized |
// industrialized] Models."). Founder ask 2026-10-16, FOR REVIEW: the headline word changes
// like the nav's does; compare "cycles in page" (default) with "one word per load"
// (?hero=refresh, a dev-only preview switch).
//
// Locks:
//   - the shipped HTML (no JS, crawlers, print) still reads the canonical word, so the
//     hero text fixture and the SEO copy are unchanged;
//   - the word list lives in ONE place (the hero's data-cycle attribute), in the
//     founder's order;
//   - nav and headline run the same module and the same CSS component (no fork);
//   - reduced motion never cycles; the headline's accessible name is stable;
//   - the slot is width-reserved and the tail moves by transform only (CLS-safe).
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import vm from "node:vm";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const src = (p) => readFileSync(path.join(WEB, "src", p), "utf8");
const dist = (p) => readFileSync(path.join(WEB, "dist", p), "utf8");
const WORDS = ["decentralized", "specialized", "industrialized"];

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

const h1 = () => dist("index.html").match(/<h1 class="hero__title"[\s\S]*?<\/h1>/)?.[0] || "";
const heroWord = () => h1().match(/<span class="([^"]*\bcycle-word\b[^"]*)"([^>]*)>([^<]*)<\/span>/);
const attr = (attrs, name) => attrs.match(new RegExp(`\\s${name}="([^"]*)"`))?.[1];

/* ---- the shipped markup ---------------------------------------------------------- */

test("the headline ships the canonical word in HTML, with the cycle list in founder order", () => {
  const m = heroWord();
  assert.ok(m, "the headline word is a .cycle-word span");
  const [, cls, attrs, text] = m;
  assert.equal(text, "decentralized", "no JS / crawlers / print read the canonical word");
  assert.match(cls, /\bcarrier\b/, "the word keeps the carrier sweep (the beat it cycles on)");
  assert.equal(attr(attrs, "data-cycle"), WORDS.join("|"), "the founder's three, in order");
  assert.equal(attr(attrs, "data-cycle-reserve"), "decentralized",
    "the slot reserves the widest word (measured: decentralized is widest in the hero face)");
  assert.equal(attr(attrs, "data-cycle-store"), "hero-word", "load rotation is opted in");
  assert.equal(attr(attrs, "data-cycle-fit"), ".hero__line", "the tail closes up by transform");
  const text1 = h1().replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");
  assert.match(text1, /world&rsquo;s decentralized Models \. In under 30 seconds\./);
});

test("the word list lives in one place: no script and no other page carries it", () => {
  const js = readdirSync(path.join(WEB, "src/js")).filter((f) => f.endsWith(".js"));
  for (const f of js) {
    for (const w of ["specialized", "industrialized"]) {
      assert.ok(!src(`js/${f}`).includes(w), `js/${f} hard-codes "${w}"; the list is the markup's`);
    }
  }
  const lists = [...src("index.html").matchAll(/data-cycle="([^"]*)"/g)];
  assert.equal(lists.length, 1, "one cycle list on the homepage");
  const pages = readdirSync(path.join(WEB, "src")).filter((f) => f.endsWith(".html") && f !== "index.html");
  for (const p of pages) assert.ok(!src(p).includes(WORDS.join("|")), `${p} repeats the headline list`);
});

test("the nav and the headline share one cycler: same module, same CSS component", () => {
  const nav = src("_partials/nav.html");
  const a = nav.match(/<a class="nav__link"[^>]*aria-label="[^"]*"[^>]*>\s*<span class="carrier cycle-word nav__app-word" data-cycle="App\|TUI\|WebUI" data-cycle-hrefs="\/app\.html\|\/app\.html#cli\|\/app\.html#webui">App<\/span><\/a>/);
  assert.ok(a, "the App item is a .cycle-word with its words and hrefs in markup");
  const site = src("js/site.js");
  assert.doesNotMatch(site, /animationiteration|data-app-cycle|data-app-word|STOPS/, "site.js no longer runs its own cycler");
  const cycle = src("js/cycle.js");
  assert.match(cycle, /querySelectorAll\("\[data-cycle\]"\)/, "cycle.js wakes on the data-cycle hook");
  for (const f of readdirSync(path.join(WEB, "src/js")).filter((x) => x.endsWith(".js") && x !== "cycle.js")) {
    assert.ok(!src(`js/${f}`).includes("[data-cycle"), `js/${f} handles [data-cycle], which belongs to cycle.js`);
  }
  assert.match(src("_partials/site-js.html"), /<script src="js\/cycle\.js" defer><\/script>/, "it ships with the page runtime");
  assert.match(dist("index.html"), /<script src="js\/cycle\.js\?v=[0-9a-f]+" defer><\/script>/);
  const comp = src("styles/components.css");
  assert.match(comp, /\.cycle-word \{[^}]*display: inline-block/, "the component lives in components.css");
  assert.match(comp, /\.cycle-word\.is-out \{/);
  assert.match(comp, /\.cycle-word\.is-in \{/);
  assert.doesNotMatch(src("styles/base.css"), /\.nav__app-word\.is-(out|in)|\.nav__app-word \{[^}]*transition/,
    "the nav keeps only its own slot width; the motion is the shared component's");
});

test("reduced motion never cycles (CSS and script both)", () => {
  const comp = src("styles/components.css");
  const rm = comp.match(/@media \(prefers-reduced-motion: reduce\) \{\s*(?:\/\*[\s\S]*?\*\/\s*)?\.cycle-word[^{]*\{[^}]*\}/);
  assert.ok(rm, "components.css has a reduced-motion rule for .cycle-word");
  assert.match(rm[0], /transition: none/);
  assert.match(src("js/cycle.js"), /prefers-reduced-motion: reduce/);
});

test("CLS-safe: the slot reserves the widest word and the tail moves by transform only", () => {
  const comp = src("styles/components.css");
  assert.match(comp, /\.cycle-word\[data-cycle-reserve\]::before \{[^}]*content: attr\(data-cycle-reserve\)[^}]*height: 0[^}]*visibility: hidden/,
    "a zero-height ghost of the widest word holds the slot's width");
  const home = src("styles/home.css");
  assert.match(home, /\.hero__tail \{[^}]*display: inline-block[^}]*transform: translateX\(calc\(-1 \* var\(--cycle-slack, 0em\)\)\)/,
    "Models and the period close up by transform, never by width or margin");
  assert.match(home, /\.hero__op::after \{[^}]*right: var\(--cycle-slack, 0em\)/, "the underline ends with the word");
  const tails = h1().match(/<span class="hero__tail">[^<]*<\/span>/g) || [];
  assert.deepEqual(tails, ['<span class="hero__tail">Models</span>', '<span class="hero__tail">.</span>']);
});

test("print reads the canonical word and drops the close-up", () => {
  const comp = src("styles/components.css");
  const print = comp.slice(comp.lastIndexOf("@media print"));
  assert.match(print, /\.cycle-word__sr \{[^}]*position: static !important/, "the canonical twin prints");
  assert.match(print, /\.cycle-word\[aria-hidden="true"\] \{ display: none !important; \}/, "the moving word does not");
  assert.match(print, /\.cycle-fit \{ --cycle-slack: 0em !important; \}/);
});

/* ---- behaviour, in a small fake DOM ----------------------------------------------- */

const CYCLE = () => readFileSync(path.join(WEB, "src/js/cycle.js"), "utf8");

function el(tag, { attrs = {}, cls = [], text = "", parent = null } = {}) {
  const e = {
    tagName: tag.toUpperCase(), attrs: { ...attrs }, textContent: text, children: [], listeners: {},
    parentNode: parent, offsetWidth: 0,
    style: { props: {}, setProperty(k, v) { this.props[k] = v; } },
    classList: { set: new Set(cls), add(c) { this.set.add(c); }, remove(c) { this.set.delete(c); }, contains(c) { return this.set.has(c); } },
    getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
    setAttribute(k, v) { this.attrs[k] = String(v); },
    hasAttribute(k) { return k in this.attrs; },
    addEventListener(t, f) { (this.listeners[t] ||= []).push(f); },
    fire(t, ev = {}) { for (const f of this.listeners[t] || []) f({ type: t, target: e, ...ev }); },
    insertBefore(n, ref) { n.parentNode = this; const i = this.children.indexOf(ref); this.children.splice(i < 0 ? this.children.length : i, 0, n); },
    matches(sel) {
      if (sel.startsWith(".")) return this.classList.contains(sel.slice(1));
      if (sel.startsWith("[")) return this.hasAttribute(sel.slice(1, -1));
      return this.tagName === sel.toUpperCase();
    },
    closest(sel) { for (let n = this; n; n = n.parentNode) if (n.matches && n.matches(sel)) return n; return null; },
    getBoundingClientRect() { return { width: e.box || 0 }; },
  };
  if (parent) parent.children.push(e);
  return e;
}

// widths at a 100px font: the slot is the widest word (decentralized, 500px)
const WIDTH = { decentralized: 500, specialized: 400, industrialized: 490 };

function page({ store = {}, throwStore = false, reduce = false, search = "" } = {}) {
  const line = el("span", { cls: ["hero__line"] });
  const op = el("span", { cls: ["hero__op"], parent: line });
  const hero = el("span", {
    cls: ["carrier", "cycle-word", "cycle-word--tune"], text: "decentralized", parent: op,
    attrs: { "data-cycle": WORDS.join("|"), "data-cycle-reserve": "decentralized", "data-cycle-store": "hero-word", "data-cycle-fit": ".hero__line" },
  });
  hero.box = 500;
  const link = el("a", { attrs: { href: "/app.html", "aria-label": "The RogerAI app, on every surface" }, cls: ["nav__link"] });
  const nav = el("span", {
    cls: ["carrier", "cycle-word", "nav__app-word"], text: "App", parent: link,
    attrs: { "data-cycle": "App|TUI|WebUI", "data-cycle-hrefs": "/app.html|/app.html#cli|/app.html#webui" },
  });
  const timers = [];
  const storage = {
    getItem(k) { if (throwStore) throw new Error("blocked"); return k in store ? store[k] : null; },
    setItem(k, v) { if (throwStore) throw new Error("blocked"); store[k] = String(v); },
  };
  const win = {
    matchMedia: (q) => ({ matches: reduce && /reduce/.test(q) }),
    location: { search },
    setTimeout: (f) => { timers.push(f); return timers.length; },
    getComputedStyle: () => ({ fontSize: "100px" }),
  };
  Object.defineProperty(win, "localStorage", { get() { if (throwStore) throw new Error("denied"); return storage; } });
  win.window = win;
  win.document = {
    querySelectorAll: (q) => (q === "[data-cycle]" ? [nav, hero] : []),
    createElement: (t) => el(t),
    createRange: () => ({ n: null, selectNodeContents(n) { this.n = n; }, getBoundingClientRect() { return { width: WIDTH[this.n.textContent] || 0 }; } }),
    fonts: { ready: Promise.resolve() },
  };
  vm.createContext(win);
  vm.runInContext(CYCLE(), win);
  const beat = (e, n = 2) => { for (let i = 0; i < n; i++) e.fire("animationiteration", { animationName: "carrier-sweep" }); };
  const flush = () => { while (timers.length) timers.shift()(); };
  return { hero, nav, link, line, op, store, beat, flush };
}

test("each load starts on the next word; first visit is the canonical word", () => {
  const store = {};
  const seen = [];
  for (let i = 0; i < 4; i++) seen.push(page({ store }).hero.textContent);
  assert.deepEqual(seen, ["decentralized", "specialized", "industrialized", "decentralized"]);
  assert.equal(page({ store: { "rogerai.cycle.hero-word": "junk" } }).hero.textContent, "decentralized", "a bad value starts over");
  assert.equal(page({ store: { "rogerai.cycle.hero-word": "9" } }).hero.textContent, "decentralized", "an out-of-range value starts over");
});

test("blocked storage never breaks the page: the canonical word stands", () => {
  const p = page({ throwStore: true });
  assert.equal(p.hero.textContent, "decentralized");
  p.beat(p.hero); p.flush();
  assert.equal(p.hero.textContent, "specialized", "and it still cycles");
});

test("the headline's accessible name is stable: a canonical twin, the moving word aria-hidden", () => {
  const p = page({ store: { "rogerai.cycle.hero-word": "2" } });
  const twin = p.op.children.find((c) => c.classList.contains("cycle-word__sr"));
  assert.ok(twin, "a visually-hidden twin carries the word for assistive tech");
  assert.equal(p.op.children.indexOf(twin), p.op.children.indexOf(p.hero) - 1, "right before the visual word");
  assert.equal(twin.textContent, "decentralized");
  assert.equal(p.hero.getAttribute("aria-hidden"), "true");
  p.beat(p.hero); p.flush();
  assert.equal(twin.textContent, "decentralized", "a swap never changes what is announced");
  assert.equal(p.nav.getAttribute("aria-hidden"), null, "the nav item is named by its link's aria-label instead");
  assert.equal(p.link.children.length, 1, "so it gets no twin");
});

test("default: the word tunes to the next one every second carrier loop", () => {
  const p = page();
  assert.equal(p.hero.textContent, "decentralized");
  p.beat(p.hero, 1);
  assert.ok(!p.hero.classList.contains("is-out"), "one loop is not enough");
  p.hero.fire("animationiteration", { animationName: "lift-line" });
  assert.ok(!p.hero.classList.contains("is-out"), "only the carrier's beat counts");
  p.beat(p.hero, 1);
  assert.ok(p.hero.classList.contains("is-out"), "the outgoing word fades");
  p.flush();
  assert.equal(p.hero.textContent, "specialized");
  assert.ok(!p.hero.classList.contains("is-out"));
  p.beat(p.hero); p.flush();
  assert.equal(p.hero.textContent, "industrialized");
  p.beat(p.hero); p.flush();
  assert.equal(p.hero.textContent, "decentralized", "and wraps");
});

test("the nav item cycles exactly as before: href follows the word, hover and focus pause it", () => {
  const p = page();
  assert.equal(p.nav.textContent, "App");
  assert.equal(p.link.getAttribute("href"), "/app.html");
  p.beat(p.nav); p.flush();
  assert.equal(p.nav.textContent, "TUI");
  assert.equal(p.link.getAttribute("href"), "/app.html#cli");
  p.link.fire("mouseenter");
  p.beat(p.nav, 4); p.flush();
  assert.equal(p.nav.textContent, "TUI", "paused under the pointer");
  p.link.fire("mouseleave");
  p.link.fire("focusin");
  p.beat(p.nav, 4); p.flush();
  assert.equal(p.nav.textContent, "TUI", "paused while focused");
  p.link.fire("focusout");
  p.beat(p.nav); p.flush();
  assert.equal(p.nav.textContent, "WebUI");
  assert.equal(p.link.getAttribute("href"), "/app.html#webui");
  assert.equal(page({ store: { "rogerai.cycle.hero-word": "1" } }).nav.textContent, "App", "the nav never rotates across loads");
});

test("reduced motion: the load's word, shown still", () => {
  const p = page({ reduce: true, store: { "rogerai.cycle.hero-word": "1" } });
  assert.equal(p.hero.textContent, "specialized", "the load rotation still applies");
  p.beat(p.hero, 6); p.beat(p.nav, 6); p.flush();
  assert.equal(p.hero.textContent, "specialized", "no cycling");
  assert.equal(p.nav.textContent, "App");
});

test("?hero=refresh (dev-only preview): one word per load, no in-page cycling", () => {
  const p = page({ search: "?hero=refresh", store: { "rogerai.cycle.hero-word": "2" } });
  assert.equal(p.hero.textContent, "industrialized");
  p.beat(p.hero, 6); p.flush();
  assert.equal(p.hero.textContent, "industrialized");
  p.beat(p.nav); p.flush();
  assert.equal(p.nav.textContent, "TUI", "the nav is unaffected");
  assert.match(CYCLE(), /DEV-ONLY[^\n]*remove before ship/i, "the switch is marked for removal");
});

test("fit: the line's tail closes up on the word by the slot's spare width, in em", async () => {
  const p = page({ store: { "rogerai.cycle.hero-word": "1" } });
  await Promise.resolve(); await Promise.resolve();
  assert.ok(p.line.classList.contains("cycle-fit"));
  assert.equal(p.line.style.props["--cycle-slack"], "1.000em", "(500 - 400) / 100px");
  p.beat(p.hero); p.flush();
  assert.equal(p.line.style.props["--cycle-slack"], "0.100em", "industrialized: (500 - 490) / 100px");
  p.beat(p.hero); p.flush();
  assert.equal(p.line.style.props["--cycle-slack"], "0.000em", "decentralized fills the slot");
  assert.equal(p.link.style.props["--cycle-slack"], undefined, "the nav does not fit");
});

test("copying the headline yields the canonical word once (the moving word is not selectable)", () => {
  // found in the browser: with the twin in place, a copy read "decentralizeddecentralized"
  assert.match(src("styles/components.css"), /\.cycle-word\[aria-hidden="true"\] \{ -webkit-user-select: none; user-select: none; \}/);
});
