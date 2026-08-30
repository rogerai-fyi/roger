// The wave mark's station plate: the on-air dot beside ROGERAI.FM and the tier nameplate
// (WAVE PICO, WAVE NANO, ...) must not crowd their type on a phone.
//
// FOUNDER 2026-08-30: "on mobile it's a bit bunched up, the text goes over the box, and
// the red dot goes over the word roger ... can we fix it for mobile only".
//
// The gap was the fixed user-unit constant PAD, tuned at the ~518 CSS px this mark renders
// at on a desktop layout (1.44 px per viewBox unit), where it reads as a comfortable
// ~13 CSS px. A phone renders the same 360-unit viewBox at ~350 px, so the identical PAD
// units collapse to ~8.7 CSS px: the art and its breathing room shrink together, leaving
// no margin on a device whose mono metrics run a shade wider than ours.
//
// wave-mark-spectrum.js is a browser IIFE with no test export hook, so - like
// session.test.mjs / fmt.test.mjs / easter-egg.test.mjs - this runs the REAL shipped
// source against a dependency-free mini-DOM and asserts the NUMBERS it computes, not the
// characters it is written in. No jsdom: the web tree is dependency-free on purpose
// (build.mjs takes no npm install), so a devDep would make the gate RED.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const SRC = readFileSync(
  path.join(path.dirname(fileURLToPath(import.meta.url)), "../src/js/wave-mark-spectrum.js"),
  "utf8",
);

const VIEWBOX_UNITS = 360; // the authored width the script sets: "-6 -16 360 188"

// ---- a tiny dependency-free SVG DOM -----------------------------------------
// Just enough of the surface the mark touches: attributes, a child list, inline style,
// and the two measurements it makes - getComputedTextLength() on its type and
// getBoundingClientRect() on the <svg> itself, which is what decides the render scale.
function makeEl(tag, svgWidthCSS) {
  const el = {
    tagName: String(tag),
    children: [],
    _attrs: {},
    style: { setProperty() {}, opacity: "", transform: "" },
    setAttribute(k, v) { this._attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(this._attrs, k) ? this._attrs[k] : null; },
    hasAttribute(k) { return Object.prototype.hasOwnProperty.call(this._attrs, k); },
    appendChild(c) { this.children.push(c); return c; },
    insertBefore(c) { this.children.push(c); return c; },
    querySelector() { return null; },
    addEventListener() {},
    // Mono type at the authored sizes: 10 units per character is close enough to the
    // real face for geometry, and the assertions below are about the PADDING either
    // side of the text, which is what the fix changes.
    getComputedTextLength() { return (this.textContent || "").length * 10; },
    getBoundingClientRect() { return { width: svgWidthCSS, bottom: 100 }; },
    textContent: "",
  };
  return el;
}

// Run the real IIFE against one <svg> at a given viewport + rendered width, and hand back
// the elements it built so their geometry can be read.
function runMark({ innerWidth, svgWidthCSS, slim = false }) {
  const svg = makeEl("svg", svgWidthCSS);
  svg.setAttribute("data-animate", "");
  if (slim) svg.setAttribute("data-slim", "");
  const built = [];
  const doc = {
    querySelectorAll: (sel) => (sel.includes("wave-mark__svg") ? [svg] : []),
    createElementNS: (_ns, tag) => { const e = makeEl(tag, svgWidthCSS); built.push(e); return e; },
    addEventListener() {},
    fonts: null,
  };
  const win = {
    location: { search: "" },
    innerWidth,
    matchMedia: () => ({ matches: false }),
    // Never actually animate: the frame loop is not what is under test here, and letting
    // it run would spin. IntersectionObserver is absent, so build() calls go() once.
    requestAnimationFrame: () => 0,
    cancelAnimationFrame() {},
    addEventListener() {},
  };
  // eslint-disable-next-line no-new-func
  new Function("window", "document", "performance", SRC)(win, doc, { now: () => 0 });

  const byClass = (c) => built.find((e) => e.getAttribute("class") === c);
  return {
    ident: byClass("wave-mark__ident"),
    onair: byClass("wave-mark__onair"),
    plate: byClass("wave-mark__plate"),
    scale: svgWidthCSS / VIEWBOX_UNITS,
  };
}

// The gap the mark was drawn with, in user units, and where the dot ends up: the script
// centres the callsign on CX and puts the dot padUnits() to its left.
const CX = 174;
function dotGapUnits({ ident, onair }) {
  return CX - ident.getComputedTextLength() / 2 - Number(onair.getAttribute("cx"));
}

test("a desktop-width mark keeps the authored 9-unit gap", () => {
  const m = runMark({ innerWidth: 1280, svgWidthCSS: 518 });
  assert.equal(Math.round(dotGapUnits(m) * 100) / 100, 9,
    "above NARROW the gap must be exactly the authored constant - that is what makes " +
    "this change mobile-only and leaves the desktop drawing untouched");
});

test("a phone widens the gap instead of letting it shrink with the art", () => {
  const desktop = runMark({ innerWidth: 1280, svgWidthCSS: 518 });
  const phone = runMark({ innerWidth: 390, svgWidthCSS: 350 });

  const phoneUnits = dotGapUnits(phone);
  assert.ok(phoneUnits > dotGapUnits(desktop),
    `a phone must get MORE drawing units of gap, not the same ${phoneUnits}`);

  // The point of the fix: the same VISUAL gap at both sizes. Held to half a CSS pixel.
  const desktopPx = dotGapUnits(desktop) * desktop.scale;
  const phonePx = phoneUnits * phone.scale;
  assert.ok(Math.abs(desktopPx - phonePx) < 0.5,
    `the phone should render the desktop spacing: desktop ${desktopPx.toFixed(2)}px vs ` +
    `phone ${phonePx.toFixed(2)}px`);
});

test("the gap is floored, never tightened, on a narrow-but-large mark", () => {
  // Below NARROW but rendered WIDER than the desktop reference: the ratio would scale the
  // gap DOWN, and the floor is what stops it going below the authored constant.
  const m = runMark({ innerWidth: 500, svgWidthCSS: 900 });
  assert.ok(dotGapUnits(m) >= 9,
    `the gap must never fall below the authored 9 units, got ${dotGapUnits(m)}`);
});

test("the dot stays inside the drawing on a phone", () => {
  // A gap that grows without bound would push the lamp off the left of the viewBox, which
  // is a worse bug than the one being fixed. The box starts at -6.
  const m = runMark({ innerWidth: 390, svgWidthCSS: 350 });
  assert.ok(Number(m.onair.getAttribute("cx")) > -6,
    "the on-air dot must stay within the mark's own box");
});

test("a slim mark carries no type to crowd, and a full one does", () => {
  // The plate and callsign are the only things padUnits() positions, and a slim mark
  // (data-slim) is defined as the motion WITHOUT them - so it must build neither, and a
  // full mark must build both. If a slim mark ever grew a plate it would be measured by
  // a helper that never runs for it.
  const full = runMark({ innerWidth: 390, svgWidthCSS: 350 });
  assert.ok(full.plate, "a full mark builds a nameplate");
  assert.ok(full.ident && full.onair, "a full mark builds the callsign and its on-air dot");
  assert.equal(full.plate.getAttribute("height"), "20", "the plate keeps its authored height");

  const slim = runMark({ innerWidth: 390, svgWidthCSS: 350, slim: true });
  assert.equal(slim.plate, undefined, "a slim mark builds no nameplate");
  assert.equal(slim.ident, undefined, "a slim mark builds no callsign");
});
