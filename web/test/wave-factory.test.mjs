// Regression locks for THE COOKIE LINE, the third Playbox deck.
//
// The game is EXECUTED as rules, not only grepped as source. The locks that
// matter most are the honesty ones: what Pico and Nano say has to come out of
// the committed bench export - including their misses - while the plant itself
// (cookies, coins, bands, wear) is game simulation and says so on the surface.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const SRC = path.join(path.dirname(fileURLToPath(import.meta.url)), "../src");
const read = (p) => readFileSync(path.join(SRC, p), "utf8");
const html = read("playbox.html").replace(/\s+/g, " ");
const js = read("js/wave-factory.js");
const css = read("styles/wave-factory.css");
const measured = JSON.parse(read("data/wave-measured.json"));

function loadHook() {
  const window = {
    setTimeout() {}, requestAnimationFrame() { return 0; },
    matchMedia() { return { matches: false }; },
    fetch() { return new Promise(() => {}); },
  };
  const document = {
    readyState: "loading", addEventListener() {}, getElementById() { return null; },
  };
  const fn = new Function("window", "document", js + "; return window.__waveFactoryTest;");
  return fn(window, document);
}

test("factory: it is a third deck of its own", () => {
  assert.match(html, /id="pgModeFactory"[^>]+aria-controls="pgFactoryView"/);
  assert.match(html, /id="pgFactoryView"[^>]+aria-labelledby="pgModeFactory"/);
  assert.match(html, /js\/wave-factory\.js/);
  assert.match(css, /--tier-pico/, "the deck carries the Spectrum hues, scoped to itself");
});

test("cookie line: the line is mixer, oven, packer - each with one sensor and one dial", () => {
  const h = loadHook();
  const s = h.freshState();
  assert.deepEqual(s.machines.map((m) => m.id), ["mixer", "oven", "packer"]);
  const kinds = s.machines.map((m) => m.spec.sensor.kind);
  assert.deepEqual(kinds, ["vib", "temp", "amp"],
    "vibration, temperature and motor current - the sensor kinds the bench actually recorded");
  for (const m of s.machines) {
    assert.ok(m.spec.control && m.spec.control.min < m.spec.control.max, `${m.id} has a real control range`);
    assert.ok(h.tiers[m.id].length >= 3, `${m.id} has upgrade tiers`);
    assert.ok(h.tiers[m.id][0].price === 0, `${m.id} starts owned - the line runs from the first second`);
  }
});

test("cookie line: it runs live, and never behind the visitor's back", () => {
  assert.ok(!js.includes("setInterval"), "no interval loop");
  assert.match(js, /requestAnimationFrame/, "the line runs on a frame loop");
  assert.match(js, /visibilitychange/, "and pauses when the tab is hidden");
  assert.match(js, /function toggleRun/, "the player can pause it outright");
  assert.match(css, /prefers-reduced-motion: reduce/, "belt motion has a reduced-motion path");
});

/* ---- THE HONESTY CORE ------------------------------------------------- */

test("honesty: what Pico says is a real record's own prediction, misses included", () => {
  const h = loadHook();
  const recs = measured.records;

  // every condition the game can hand a sensor must be drawable from the bench
  for (const cond of h.conditions) {
    const drawn = h.sampleWith(recs, "vib", cond, 3);
    assert.ok(drawn, `a record exists for ${cond}`);
    assert.equal(drawn.record.truth, cond, "the drawn record's recorded truth IS the condition");
    assert.ok(recs.includes(drawn.record), "and it is a record from the committed export, not a construction");
  }

  // Pico's three outcomes are read straight off the record
  const sure = { record: { truth: "stuck", child: { prediction: "stuck", margin: 3.1 }, parent: {} } };
  assert.equal(h.picoRead(sure).kind, "caught");
  const unsure = { record: { truth: "stuck", child: { prediction: "stuck", margin: 0.4 }, parent: {} } };
  assert.equal(h.picoRead(unsure).kind, "unsure", "below its floor it says it is not sure");
  const wrong = { record: { truth: "stuck", child: { prediction: "noisy", margin: 3.1 }, parent: {} } };
  assert.equal(h.picoRead(wrong).kind, "wrong",
    "a recorded miss stays a miss - the game never upgrades it into a catch");
});

test("honesty: Nano only resolves what the recorded senior actually got right", () => {
  const h = loadHook();
  const good = { record: { truth: "drifting", child: {}, parent: { prediction: "drifting", margin: 2.0 } } };
  assert.equal(h.nanoRead(good).kind, "resolved");
  const bad = { record: { truth: "drifting", child: {}, parent: { prediction: "none", margin: 2.0 } } };
  assert.equal(h.nanoRead(bad).kind, "missed",
    "when the recorded parent was wrong, Nano is wrong here too");
});

test("honesty: a cross-instrument draw is disclosed, never blurred", () => {
  const h = loadHook();
  // the bench has no NOISY vibration channel, so this must fall back AND say so
  const drawn = h.sampleWith(measured.records, "vib", "noisy", 1);
  assert.ok(drawn, "it still finds a real noisy record");
  assert.equal(drawn.sameKind, false, "and reports that it came from another instrument");
  assert.match(js, /recorded on another instrument/,
    "the surface prints that disclosure rather than implying a vibration record");
  // where the pairing does exist it is used and reported as exact
  const exact = h.sampleWith(measured.records, "vib", "stuck", 0);
  assert.equal(exact.sameKind, true);
});

test("honesty: the plant is labelled simulation, and the desk tiers claim no inference", () => {
  assert.match(js, /MODEL BEHAVIOUR: RECORDED REPLAY/);
  assert.match(js, /THE PLANT ITSELF IS GAME SIMULATION/);
  assert.match(js, /no recorded run exists for those tiers/,
    "Micro and Giga are named as computing over game state, not replaying a model");
  for (const view of ["cl-view--micro", "cl-view--giga"]) {
    assert.ok(js.includes(view), `${view} exists`);
  }
  assert.match(js, /arithmetic over the game's own numbers/);
});

/* ---- THE PLAYABLE LOOP ------------------------------------------------ */

test("cookie line: a lying sensor shows a comfortable number while the truth moves", () => {
  const h = loadHook();
  const s = h.freshState();
  const mixer = s.machines[0];
  mixer.cond = "stuck";
  mixer.stuckAt = 2.4;
  mixer.real = 7.9;                      // the process has walked well out of band
  const save = global.G;
  const shown = h.shownValue(mixer);
  assert.equal(shown, 2.4,
    "a stuck sensor reports its frozen value - this is the whole phase-0 problem");
  assert.notEqual(shown, mixer.real, "and it is not the truth");
});

test("cookie line: an out-of-band machine stops feeding the next one", () => {
  const h = loadHook();
  const s = h.freshState();
  const oven = s.machines[1];
  oven.set = 240; oven.real = 240;        // far above the Mk I band
  for (let i = 0; i < 40; i++) h.stepWith(s, 1 / 12);
  assert.ok(oven.stopped, "the oven stops when it sits outside its band");
  assert.ok(s.machines[1].buffer < 1, "so nothing reaches the packer");
});

test("cookie line: the ladder is a handover - reach grows with the desk tiers", () => {
  const h = loadHook();
  const s = h.freshState();
  assert.equal(h.reachWith(s), 0, "with no desk model, every knob is the player's");
  s.micro = true;
  assert.equal(h.reachWith(s), 1, "Micro can hold one knob");
  s.giga = true;
  assert.equal(h.reachWith(s), 3, "Giga can hold the whole plant");
});

test("cookie line: automation acts on what it BELIEVES, so a lie still fools it", () => {
  const h = loadHook();
  const s = h.freshState();
  s.micro = true;
  const mixer = s.machines[0];
  mixer.auto = true;
  mixer.cond = "stuck";
  mixer.stuckAt = 0.2;                    // the sensor claims the machine is idle
  mixer.real = 4.9;                       // it is actually near the top of the band
  const before = mixer.set;
  h.autoWith(s, "mixer", 1);
  assert.ok(mixer.set >= before,
    "believing the low reading, the automation pushes the knob UP - exactly as a person would be fooled");
});

test("cookie line: an incident that stopped the line counts as missed, automated or not", () => {
  const h = loadHook();
  const s = h.freshState();
  const mixer = s.machines[0];
  mixer.cond = "drifting"; mixer.hadStop = true; s.incidents.open = 1;
  const inc = h.clearWith(s, "mixer");
  assert.equal(inc.missed, 1, "the line died before anyone acted");
  assert.equal(inc.caught, 0);

  const s2 = h.freshState();
  s2.machines[0].cond = "drifting"; s2.machines[0].hadStop = false; s2.incidents.open = 1;
  const inc2 = h.clearWith(s2, "mixer");
  assert.equal(inc2.caught, 1, "serviced before it bit - that is a catch");
});

test("cookie line: results accumulate from play, and say they are the game's own numbers", () => {
  const h = loadHook();
  const s = h.freshState();
  for (let i = 0; i < 30; i++) h.stepWith(s, 1 / 12);
  assert.ok(s.history.length >= 1, "a rolling series accumulates while the line runs");
  const sample = s.history[0];
  for (const key of ["t", "rate", "up", "caught", "missed"]) {
    assert.ok(key in sample, `the series records ${key}`);
  }
  assert.match(js, /not a measured claim about any model/,
    "the results panel says what kind of numbers these are");
  assert.match(js, /Automation does not make the recorded models infallible/,
    "and an automated plant still shows the misses");
});

test("cookie line: the site and plant views read the game's live state", () => {
  const h = loadHook();
  const s = h.freshState();
  s.micro = true; s.giga = true;
  const site = h.siteWith(s);
  assert.equal(site.rows.length, 3, "one row per machine");
  assert.ok(site.worst && site.worst.name, "and it names the one with least headroom");
  const plant = h.plantWith(s);
  assert.ok(plant.bottleneck && plant.bottleneck.name, "the plant view names the bottleneck");
  assert.ok(plant.rates.length === 3);
});

test("cookie line: tier colour is identity, state colour is state", () => {
  // the same discipline the mesh deck is locked to
  const identity = css.match(/\.cl-say--(pico|nano) \.cl-say__who \{[^}]*\}/g) || [];
  assert.ok(identity.length >= 2, "model names wear their tier hue");
  assert.match(css, /\.cl-lamp\[data-state="stopped"\]/, "machine state has its own colour scale");
  assert.match(js, /state === "stopped" \? "STOPPED"/,
    "and every state carries its word, so colour is never the only signal");
});

test("cookie line: servicing costs downtime, so the player can never be soft-locked", () => {
  const h = loadHook();
  const s = h.freshState();
  const mixer = s.machines[0];
  mixer.cond = "stuck"; mixer.stuckAt = 2.4;
  s.coins = 0;                             // flat broke on purpose

  // the fix is reachable with no money at all: it is paid in production, and
  // the source must not contain a coin deduction for it anywhere
  assert.match(js, /SERVICE_SECS/, "the cost is measured in seconds");
  assert.doesNotMatch(js, /SERVICE_COST/, "the money cost is gone for good");
  assert.doesNotMatch(js, /coins -= [^;]*SERVICE/, "service never debits the wallet");
  assert.match(js, /no way back/,
    "and the reason is written down where the next reader will see it");

  // a broke player can still start the work, and it still completes
  mixer.servicing = 4;
  for (let i = 0; i < 60; i++) h.stepWith(s, 1 / 12);
  assert.equal(mixer.cond, "none", "the line recovers even from zero coins");
});

test("cookie line: a machine is down while it is serviced, then comes back honest", () => {
  const h = loadHook();
  const s = h.freshState();
  const mixer = s.machines[0];
  mixer.cond = "drifting"; mixer.driftLie = 3; mixer.servicing = 4;

  h.stepWith(s, 1 / 12);
  assert.ok(mixer.stopped, "a machine under service is stopped");
  assert.equal(h.rateOf(s, "mixer"), 0, "and it produces nothing while it is down");

  for (let i = 0; i < 60; i++) h.stepWith(s, 1 / 12);   // five seconds
  assert.equal(mixer.servicing, 0, "the work finishes");
  assert.equal(mixer.cond, "none", "and the sensor tells the truth again");
});
