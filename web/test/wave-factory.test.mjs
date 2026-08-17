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

test("cookie line: service invoices, loans if broke - and can never soft-lock", () => {
  /* AMENDED (v24, founder direction): service costs COINS again - "it will
     always fix it but it will require some money/coins, if you don't have
     coins then it goes on loan so you get negative money." The guarantee this
     lock has always protected is NO SOFT-LOCK: in v22 that was bought by
     making the work free; now it is bought by credit - service is ALWAYS
     available, a broke wallet goes negative, and earnings pay the debt down
     before they accumulate (negative + income -> zero is exactly that order).
     The downtime cost stays on top of the invoice. */
  const h = loadHook();
  const s = h.freshState();
  const mixer = s.machines[0];
  mixer.cond = "stuck"; mixer.stuckAt = 2.4;
  s.coins = 10;                            // cannot cover the invoice

  assert.ok(h.serviceWith(s, "mixer"), "a broke player can still order the work");
  assert.equal(s.coins, 10 - h.serviceCost, "the invoice lands anyway - that is the loan");
  assert.ok(s.coins < 0, "the balance is genuinely negative, not clamped");

  // the work still completes from negative money, and the line recovers
  for (let i = 0; i < 60; i++) h.stepWith(s, 1 / 12);
  assert.equal(mixer.cond, "none", "the line recovers even in debt");

  // deeper into debt is still allowed - service is NEVER unavailable
  mixer.cond = "drifting"; mixer.driftLie = 1;
  assert.ok(h.serviceWith(s, "mixer"), "service works at any balance");
  assert.ok(s.coins <= 10 - 2 * h.serviceCost + 60, "and invoices again");

  // earnings pay the debt down before they pile up: income is plain addition
  // on a negative balance, so the wallet must cross zero before it grows
  const debtState = h.freshState();
  debtState.coins = -20;
  for (let i = 0; i < 240; i++) h.stepWith(debtState, 1 / 12);   // twenty seconds of shipping
  assert.ok(debtState.coins > -20, "earnings move the balance up from debt");
  assert.match(js, /on loan/i, "the loan is worded on the surface when it happens");
  assert.match(js, /stays impossible/,
    "and the no-soft-lock reasoning is written down where the next reader will see it");
});

/* ---- v24: THE ACTION LADDER -------------------------------------------- */

test("v24: restart follows the doctrine - never fixes drift or railing, and lockout is the cost of guessing", () => {
  const h = loadHook();
  // the doctrine table itself: what Nano sells is exactly this knowledge
  assert.equal(h.restartOdds.drifting, 0, "drift is calibration - restart never fixes it");
  assert.equal(h.restartOdds.railed, 0, "railing is hardware - restart never fixes it");
  assert.ok(h.restartOdds.stuck >= 0.7 && h.restartOdds.dropout >= 0.7,
    "stuck and dropout usually clear on a restart");
  assert.ok(h.restartOdds.noisy <= 0.3, "noise rarely does");

  // EXECUTED on a deterministic case: restarting a drifting sensor cannot
  // succeed, so it must end in the 60s lockout with the fault still there
  const s = h.freshState();
  const mixer = s.machines[0];
  mixer.cond = "drifting"; mixer.driftLie = 2;
  assert.ok(h.restartWith(s, "mixer"), "the restart is accepted");
  for (let i = 0; i < 24; i++) h.stepWith(s, 1 / 12);   // two seconds - restart resolves
  assert.equal(mixer.cond, "drifting", "the fault survived, as the doctrine says it must");
  assert.ok(mixer.lockout > 50, "and the machine's maintenance is locked out for ~a minute");

  // locked out means locked out - but only for MAINTENANCE, the dial stays live
  assert.equal(h.restartWith(s, "mixer"), false, "no second restart during lockout");
  assert.equal(h.serviceWith(s, "mixer"), false, "no service during lockout either");
  // the lockout expires on the clock
  mixer.lockout = 0.05;
  h.stepWith(s, 1 / 12);
  assert.equal(mixer.lockout, 0, "and it expires");
});

test("v24: Nano's counsel prescribes the ACTION per fault kind, per the doctrine", () => {
  const h = loadHook();
  assert.match(h.conditionFix.stuck, /RESTART/i, "stuck: restart is the first move");
  assert.match(h.conditionFix.dropout, /RESTART/i, "dropout: restart re-seats it");
  assert.match(h.conditionFix.noisy, /SERVICE/, "noisy: service is the sure fix");
  assert.match(h.conditionFix.noisy, /rarely/i, "and the odds are stated, not implied");
  assert.match(h.conditionFix.drifting, /will not help.*SERVICE/i, "drifting: restart ruled out");
  assert.match(h.conditionFix.railed, /will not help.*SERVICE/i, "railed: restart ruled out");
  // the not-a-sensor-fault case: the gateway points at the dial, not the crew
  assert.match(js, /not a sensor fault - the process is out of its band/,
    "a clean out-of-band gets dial advice, saving a wasted service");
  assert.match(js, /site gateway/, "and the counsel is signed by the one gateway serving every Pico");
});

test("v24: a healthy Pico reads fresh truth-none windows on a cadence", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  const mixer = s.machines[0];
  mixer.pico = true; mixer.windowLeft = 0; mixer.nextFault = 999; // stay healthy
  h.stepWith(s, 1 / 12);
  assert.ok(mixer.healthySample, "a healthy window is drawn at all");
  assert.equal(mixer.healthySample.record.truth, "none",
    "and it is a real truth-none record - health draws health, faults draw faults");
  assert.ok(measured.records.includes(mixer.healthySample.record), "from the committed export");
  assert.ok(mixer.picoRead, "so Pico has something to say while healthy");

  // the redraw cadence: more windows arrive as time passes
  const draws = mixer.healthyDraws;
  for (let i = 0; i < 12 * h.healthyWindow * 2.2; i++) h.stepWith(s, 1 / 12);
  assert.ok(mixer.healthyDraws > draws, "the display lives - windows redraw on the cadence");
  // and the display surfaces the confidence, so "steady" reads as measured
  assert.match(js, /toFixed\(1\) \+ " sure"/, "the margin rides the word");
});

test("v24: the meter shows the band as a zone and pre-warns on the METER only", () => {
  const h = loadHook();
  const s = h.freshState();
  const mixer = s.machines[0];               // Mk I band 0-5
  const mid = h.meterWith(s, "mixer", 2.5);
  assert.equal(mid.state, "ok");
  assert.ok(mid.zoneLeft > 0.15 && mid.zoneLeft < 0.25, "the band is an inset zone, not the whole meter");
  assert.equal(h.meterWith(s, "mixer", 4.8).state, "edge", "near the top edge warns");
  assert.equal(h.meterWith(s, "mixer", 5.6).state, "out", "past it is OUT - a place on the meter");
  assert.equal(h.meterWith(s, "mixer", null).state, "gone", "a dropout reads as gone");
  // the lamp never borrows the meter's early warning: its states are unchanged
  assert.match(js, /state = m\.stopped \? "stopped" : \(!claimsOk \? "warn" : "ok"\)/,
    "lamp state still derives only from stopped + what the sensor claims");
});

test("v24: process creep leans on healthy machines, and is game simulation with flavour", () => {
  const h = loadHook();
  const s = h.freshState();
  const mixer = s.machines[0];
  mixer.nextFault = 9999;                    // no faults; creep only
  mixer.event = { t: 0, ramp: 1, hold: 60, decay: 1, mag: 1.4 };
  for (let i = 0; i < 36; i++) h.stepWith(s, 1 / 12);   // three seconds into the hold
  assert.ok(mixer.ambient > 1.2, "the event leans on the machine");
  const drifted = mixer.real;
  assert.ok(drifted > 2.2, "and the real value creeps up while every sensor stays honest");
  assert.match(js, /creeping up/, "the line radio narrates the creep in plant language");
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

/* ---- THE SCENE (v23: the game is a picture, not a dashboard) ----------- */

test("scene: the machines are the engraved sprites, standing on a floor", () => {
  for (const plate of ["game-mixer-ink.png", "game-oven-ink.png", "game-packer-ink.png", "game-belt-ink.png"]) {
    assert.ok(css.includes(plate), `${plate} is masked into the scene`);
  }
  assert.match(css, /\.clf-floor \{/, "there is a floor");
  assert.match(js, /clf-machine clf-machine--/, "machines stand in it");
  assert.doesNotMatch(css, /\.cl-line \{/,
    "the card grid is gone as the primary surface - the floor replaced it");
  // FIG.3 wears its own plate now, not the mesh deck's masthead
  assert.match(css, /wm-masthead--factory[\s\S]{0,400}factory-game-ink\.png/,
    "the factory masthead points at the factory plate");
});

test("scene: the lamp on the machine reads the SENSOR, and the tells read the sim", () => {
  // same guarantee as before, now painted onto the machine body: the lamp
  // state comes from what the instrument CLAIMS, never from m.real
  assert.match(js, /claimsOk = shown == null \? true : inBand\(m, shown\)/,
    "lamp state derives from the shown value");
  assert.match(js, /is-dead", m\.stopped/, "the art dims from the sim's stopped flag");
  assert.match(js, /baking = !m\.stopped && m\.real >= tierOf\(m\)\.lo/,
    "the oven glow follows the real baking state - decoration for a worded state");
});

test("scene: traveling cookies are SPENT from the sim's own transfers", () => {
  const h = loadHook();
  const s = h.freshState();
  // a running line really transfers product, so flow accumulates
  for (let i = 0; i < 24; i++) h.flowWith(s, 1 / 12);
  assert.ok(s.flow.dough > 0, "the mixer's real output feeds the dough segment");
  // a dead oven transfers nothing to the baked segment
  const s2 = h.freshState();
  s2.machines[1].set = 240; s2.machines[1].real = 240;   // far out of band
  for (let i = 0; i < 40; i++) h.flowWith(s2, 1 / 12);
  const bakedAfterStop = s2.flow.baked;
  for (let i = 0; i < 12; i++) h.flowWith(s2, 1 / 12);
  assert.ok(s2.flow.baked - bakedAfterStop < 0.36,
    "a stopped oven stops feeding the baked segment - its belt stretch empties");
  // and the only writer of flow is step(); the only spender is the sprite layer
  const writers = js.match(/G\.flow\.\w+ \+=/g) || [];
  assert.equal(writers.length, 3, "exactly the three segment writes, inside step()");
  assert.match(js, /G\.flow\[seg\.flow\] -= SPRITE_PER/,
    "sprites exist only by spending what the sim transferred");
});

test("scene: the shop is an overlay that pauses the line, and [hidden] wins", () => {
  assert.match(js, /G\.shopWasRunning = G\.running;\s*G\.running = false/,
    "opening the shop stops the clock");
  assert.match(css, /\.clf-shopover\[hidden\] \{ display: none; \}/,
    "display:flex must not beat the hidden attribute - the mesh deck's own bug");
});
