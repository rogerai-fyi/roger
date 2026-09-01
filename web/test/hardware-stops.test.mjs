// hardware.js, actually run.
//
// The rest of the hardware suite reads the SOURCE, which is fine for "does this file say
// role=button" and useless for "does the right card get scrolled to". A stop on the
// spectrum is a control now: eight stops, ten cards, and a reader should be able to click
// "Wave Giga" and land on the machine that carries it. That behaviour is worth exercising,
// including the parts that are easy to ship broken - keyboard activation, a stop with no
// card behind it, and yielding to prefers-reduced-motion.
//
// The mini-DOM is the house idiom from session.test.mjs: enough element for this script and
// nothing more, so a failure here is this script's fault rather than a framework's.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC = readFileSync(path.join(WEB, "src", "js", "hardware.js"), "utf8");

function makeEl(tag) {
  const el = {
    tagName: String(tag).toUpperCase(),
    children: [], parentNode: null, _attrs: {}, className: "", textContent: "",
    _on: {},
    setAttribute(k, v) { this._attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this._attrs, k) ? this._attrs[k] : null; },
    appendChild(c) { c.parentNode = this; this.children.push(c); return c; },
    addEventListener(ev, fn) { (this._on[ev] = this._on[ev] || []).push(fn); },
    removeEventListener() {},
    // what the script is actually here to cause
    scrollIntoView(opts) { this.scrolled = opts; },
    fire(ev, e) { (this._on[ev] || []).forEach((fn) => fn(e || {})); },
    querySelector(sel) {
      const cls = sel.replace(/^\./, "");
      return this.children.find((c) => c.classList.contains(cls)) || null;
    },
  };
  el.classList = {
    contains: (c) => el.className.split(/\s+/).includes(c),
    add: (c) => { if (!el.classList.contains(c)) el.className = (el.className + " " + c).trim(); },
    remove: (c) => { el.className = el.className.split(/\s+/).filter((x) => x !== c).join(" "); },
  };
  return el;
}

// One spectrum (three stops, one of which has no board behind it) and two cards.
function build({ reduced = false } = {}) {
  const ladder = makeEl("svg"); ladder.className = "hw-ladder";

  const mk = (stop, tier) => {
    const g = makeEl("g"); g.className = "hw-stop"; g.setAttribute("data-stop", stop);
    const t = makeEl("text"); t.className = "hw-stop__tier"; t.textContent = tier;
    g.appendChild(t); return g;
  };
  const stops = [mk("giga", "Wave Giga"), mk("edge", "Roger Edge"), mk("peta", "Wave Peta")];

  const card = (stop) => { const a = makeEl("article"); a.className = "hw-card"; a.setAttribute("data-stop", stop); return a; };
  // two boards share "edge" - the jump must land on the FIRST
  const cards = [card("giga"), card("edge"), card("edge")];

  const doc = {
    querySelector: (sel) => (sel.includes("hw-ladder") ? ladder : null),
    querySelectorAll: (sel) => (sel.includes("hw-card") ? cards : sel.includes("hw-stop") ? stops : []),
    addEventListener() {},
  };
  const win = {
    matchMedia: () => ({ matches: reduced }),
    setTimeout: (fn) => { win._timer = fn; return 1 },
    document: doc,
  };
  new Function("window", "document", SRC)(win, doc);
  return { ladder, stops, cards, win };
}

test("stops: a stop with a machine behind it becomes an operable control", () => {
  const { stops } = build();
  const giga = stops[0];
  assert.equal(giga.getAttribute("role"), "button", "announced as a control");
  assert.equal(giga.getAttribute("tabindex"), "0", "reachable by keyboard");
  assert.match(giga.getAttribute("aria-label") || "", /Wave Giga/, "and says where it goes");
  assert.ok(giga.classList.contains("is-linked"), "and is styled as one");
});

test("stops: a stop with no machine behind it is left as drawing", () => {
  // "peta" has no card in this fixture. Making it look clickable and then doing nothing is
  // worse than leaving it alone, and a keyboard user would land on a dead tab stop.
  const { stops } = build();
  const peta = stops[2];
  assert.equal(peta.getAttribute("role"), null, "not announced as a control");
  assert.equal(peta.getAttribute("tabindex"), null, "and not in the tab order");
});

test("stops: clicking one scrolls to its machine, and the FIRST of several", () => {
  const { stops, cards } = build();
  stops[1].fire("click");                    // "edge", which two cards share
  assert.ok(cards[1].scrolled, "the first edge board was scrolled to");
  assert.ok(!cards[2].scrolled, "not the second one as well");
  assert.ok(cards[1].classList.contains("is-found"), "and is marked so the eye finds it");
});

test("stops: Enter and Space activate it, other keys do not", () => {
  for (const key of ["Enter", " "]) {
    const { stops, cards } = build();
    let defaultPrevented = false;
    stops[0].fire("keydown", { key, preventDefault: () => { defaultPrevented = true; } });
    assert.ok(cards[0].scrolled, `${JSON.stringify(key)} activates the stop`);
    assert.ok(defaultPrevented, `${JSON.stringify(key)} does not also scroll the page`);
  }
  const { stops, cards } = build();
  stops[0].fire("keydown", { key: "a", preventDefault() {} });
  assert.ok(!cards[0].scrolled, "an unrelated key does nothing");
});

test("stops: the jump and the sweep both yield to prefers-reduced-motion", () => {
  const { stops, cards, ladder } = build({ reduced: true });
  stops[0].fire("click");
  assert.equal(cards[0].scrolled.behavior, "auto", "no smooth scroll for a reader who asked not to");
  assert.ok(!ladder.classList.contains("is-sweeping"), "and the needle does not sweep");
});

test("stops: focusing a stop lights it, blurring puts it out", () => {
  const { stops } = build();
  stops[0].fire("focus");
  assert.ok(stops[0].classList.contains("is-lit"), "focus lights the stop it is on");
  stops[0].fire("blur");
  assert.ok(!stops[0].classList.contains("is-lit"), "and leaving puts it out again");
});
