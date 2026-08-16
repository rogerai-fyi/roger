// Regression locks for WAVEWORKS, the third Playbox deck.
// The game is executed as rules, not only searched as source text.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const SRC = path.join(path.dirname(fileURLToPath(import.meta.url)), "../src");
const read = (p) => readFileSync(path.join(SRC, p), "utf8");
const html = read("playbox.html").replace(/\s+/g, " ");
const js = read("js/wave-factory.js");
const css = read("styles/wave-factory.css");
const measured = JSON.parse(read("data/wave-measured.json"));

function loadHook() {
  const window = { setTimeout() {}, fetch() { return new Promise(() => {}); } };
  const document = { readyState: "loading", addEventListener() {}, getElementById() { return null; } };
  const fn = new Function("window", "document", js + "; return window.__waveFactoryTest;");
  return fn(window, document);
}

test("factory: it is a third deck, not a mode inside Wave Mesh", () => {
  assert.match(html, /id="pgModeFactory"[^>]+aria-controls="pgFactoryView"/);
  assert.match(html, /id="pgFactoryView"[^>]+aria-labelledby="pgModeFactory"/);
  assert.match(html, /js\/wave-factory\.js/);
  assert.doesNotMatch(html, /id="wsWelcome"|id="wsStartGame"|id="wsFactory"/,
    "the old mystery quiz no longer occupies the engineering workbench");
  assert.match(css, /grid-template-columns:\s*repeat\(3/,
    "all three deck choices are equally visible");
});

test("factory: the first level is build, move, earn, expand - never answer model trivia", () => {
  for (const copy of ["BUILD THE PART SHAPER", "BUILD THE CRATE PACKER", "RUN A BATCH",
                      "REWORK THE HELD BATCH", "OPEN LINE TWO", "BOLTS", "CRATES", "BUILD SHELF"]) {
    assert.ok(js.includes(copy), `the playable loop includes ${copy}`);
  }
  assert.doesNotMatch(js, /missionChooseMove|incidentMove|correct answer|wrong answer/i,
    "there is no hidden multiple-choice model test");
  assert.ok(!js.includes("setInterval"), "the factory never plays itself behind the visitor's back");
  assert.match(css, /@keyframes wf2-part-run/, "a clicked batch visibly moves across the floor");
  assert.match(css, /prefers-reduced-motion: reduce/, "the movement has a reduced-motion path");
});

test("factory v2: the screen reads as a world, an objective, and one selected machine", () => {
  for (const token of ["FACTORY FLOOR", "MODEL NETWORK", "CURRENT OBJECTIVE", "wf2-grid",
                       "wf2-node", "wf2-track", "wf2-inspector", "wf2-buildbar",
                       "SHIP CRATES · EARN BOLTS · BUILD SMARTER", "WHY DID THAT HAPPEN?"]) {
    assert.ok(js.includes(token), `the game surface includes ${token}`);
  }
  assert.match(css, /\.wf2-grid\s*\{[^}]*grid-template-columns:\s*repeat\(14/s,
    "machines and belts occupy a real top-down floor grid");
  assert.match(css, /\.wf2-node\.is-pad|\.wf2-node\.is-pad/,
    "empty construction pads are visually different from installed machines");
  assert.match(css, /\.wf2-network__body[\s\S]*\.wf2-tree__node/,
    "the model ladder has a separate tech-tree surface");
});

test("factory v3: FIG. 3 uses the same theme-adaptive engraved plate system", () => {
  assert.match(html, /wm-masthead--factory/);
  assert.match(html, /factory-game-ink\.png|Engraved illustration: a miniature factory line/);
  assert.match(css, /assets\/wave\/factory-game-ink\.png/);
  assert.match(css, /assets\/wave\/factory-game-spot\.png/);
  assert.ok(statSync(path.join(SRC, "assets/wave/factory-game-ink.png")).size < 180_000,
    "the factory ink plate stays lean enough for the masthead");
  assert.ok(statSync(path.join(SRC, "assets/wave/factory-game-spot.png")).size < 16_000,
    "the single-red spot plate stays sparse");
});

test("factory v3: the order, money loop, and model payoff are visible before the first run", () => {
  for (const token of ["ACTIVE ORDER", "A CLEAN RUN PAYS", "BOLTS ARE BUILD CURRENCY",
                       "NEXT MODEL PAYOFF", "LINE DEFENSE", "FAULT CATCH · +4",
                       "DOUBT SAVE · +8", "SITE FLOW · +8/LINE", "PLANT FLOW · +15/LINE"]) {
    assert.ok(js.includes(token), `the game explains ${token}`);
  }
  const h = loadHook();
  const s = h.freshState();
  assert.equal(h.cleanPayFor(s), 26);
  s.lines = 2; s.built.micro = true; s.built.giga = true;
  assert.equal(h.cleanPayFor(s), 98, "clean pay is explicit base plus site and plant bonuses, per line");
  assert.equal(h.nextAutomation(h.freshState()).name, "PICO");
});

test("factory: the opening difficulty curve selects real replay records", () => {
  const h = loadHook();
  const r0 = h.pickRecord(measured.records, 0);
  const r1 = h.pickRecord(measured.records, 1);
  const r2 = h.pickRecord(measured.records, 2);
  const r4 = h.pickRecord(measured.records, 4);
  assert.equal(r0.truth, "none", "first batch is readable and healthy");
  assert.ok(r1.truth !== "none" && r1.child.prediction === r1.truth && r1.child.margin >= 1.5,
    "next comes a fault Pico can confidently catch");
  assert.ok(r2.truth !== "none" && r2.child.margin < 1.5 && r2.child.prediction !== r2.truth && r2.parent.prediction === r2.truth,
    "then a real doubtful read where the recorded Nano result helps");
  assert.ok(r4.truth !== "none" && r4.child.prediction !== r4.truth && r4.parent.prediction !== r4.truth,
    "the hard card is a real miss, not a fabricated victory");
  for (const r of [r0, r1, r2, r4]) assert.ok(measured.records.includes(r));
});

test("factory: model equipment changes flow without claiming answers that were not recorded", () => {
  const h = loadHook();
  const base = h.freshState();
  base.lines = 1;
  const picoCard = h.pickRecord(measured.records, 1);
  const nanoCard = h.pickRecord(measured.records, 2);
  const hardCard = h.pickRecord(measured.records, 4);

  assert.equal(h.resolveBatch(base, picoCard).reason, "no-scanner",
    "without automation the dock holds a bad part for the player");
  base.built.pico = true;
  assert.equal(h.resolveBatch(base, picoCard).kind, "pico",
    "Pico only gets credit for its recorded confident success");
  assert.equal(h.resolveBatch(base, nanoCard).reason, "needs-gateway",
    "a doubtful read stays held before Nano is purchased");
  base.built.nano = true;
  assert.equal(h.resolveBatch(base, nanoCard).kind, "nano",
    "Nano automates the handoff only when its recorded answer matches truth");
  base.built.micro = true;
  base.built.giga = true;
  assert.equal(h.resolveBatch(base, hardCard).reason, "model-miss",
    "larger unrecorded tiers never magically rewrite a Pico+Nano miss");
});

test("factory: progression is an economy, not a pass/fail gate", () => {
  const h = loadHook();
  const s = h.freshState();
  assert.equal(h.availability("former", s).ok, true);
  assert.match(h.availability("packer", s).reason, /SHAPER FIRST/);
  s.built.former = true;
  assert.equal(h.availability("packer", s).ok, true);
  assert.equal(h.availability("pico", s).ok, true);
  s.built.pico = true; s.credits = 30;
  assert.equal(h.availability("nano", s).ok, false, "wallet is the remaining constraint");
  s.credits = 200;
  assert.equal(h.availability("nano", s).ok, true);
  s.built.nano = true; s.shipped = 3;
  assert.equal(h.availability("micro", s).ok, true);
});

test("factory: game simulation and measured replay are labelled separately", () => {
  assert.match(js, /The signal and Pico\/Nano fields above come from the committed replay/);
  assert.match(js, /factory, crates, bolts, routing and bonuses are game simulation/);
  assert.match(js, /no replayed Micro answer is invented/i);
  assert.match(html, /economy, crates, buildings and production outcomes are an explicit game simulation/i);
});
