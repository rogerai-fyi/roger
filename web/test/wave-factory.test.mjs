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
  s.micro = 1;
  assert.equal(h.reachWith(s), 1, "one Micro can hold one knob");
  s.micro = 2;
  assert.equal(h.reachWith(s), 2, "Micros stack - a second one holds a second knob");
  s.giga = true;
  assert.equal(h.reachWith(s), 3, "Giga can hold the whole plant");
});

test("cookie line: automation acts on what it BELIEVES, so a lie still fools it", () => {
  const h = loadHook();
  const s = h.freshState();
  s.micro = 1;
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
  s.micro = 1; s.giga = true;
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

  // the work still completes from negative money, and the line recovers.
  // (v26 repriced service to 10s of downtime - the sure thing is now the
  // slow thing - so this drive steps past the longer window.)
  for (let i = 0; i < 140; i++) h.stepWith(s, 1 / 12);
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
  /* AMENDED (v25, founder direction): the vocabulary grew - "other ways we
     can try to fix the problem without service first". The guarantee is the
     same: Nano prescribes a SPECIFIC verb per fault kind, and the cheap
     verbs' limits are stated, not implied. Service is now only THE answer
     for railed (hardware); noise wants CLEAN, drift wants RECALIBRATE. */
  assert.match(h.conditionFix.stuck, /RESTART/i, "stuck: restart is the first move");
  assert.match(h.conditionFix.dropout, /RESTART/i, "dropout: restart re-seats it");
  assert.match(h.conditionFix.noisy, /CLEAN/, "noisy: clean the pickup");
  assert.match(h.conditionFix.noisy, /rarely/i, "and restart's poor odds are stated, not implied");
  assert.match(h.conditionFix.drifting, /RECALIBRATE/i, "drifting: recalibrate is the fix");
  assert.match(h.conditionFix.drifting, /will not help/i, "and restart is ruled out in words");
  assert.match(h.conditionFix.railed, /Only SERVICE/i, "railed: hardware - only the crew");
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
  // and the display surfaces the confidence, so "steady" reads as measured.
  // AMENDED v26: the raw number pair confused the playtest ("0.9 sure...out
  // of what?"), so the confidence is now a METER with the floor as a tick,
  // and the exact figures ride its tooltip. Same guarantee, better surface.
  assert.match(js, /toFixed\(1\) \+ " sure, needs " \+ FLOOR/,
    "the margin still rides the display, in the meter's tooltip");
  assert.match(js, /cl-margin__tick/, "and the floor is a visible tick to clear");
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

/* ---- v25: the grown vocabulary, the dial-first rule, the floor that sells - */

test("v25: the verb-fault matrix - each cheap verb fixes its own fault kind, and nothing else", () => {
  const h = loadHook();
  // the doctrine table, executed as data
  assert.equal(h.verbs.clean.odds.noisy, 0.85, "clean clears a noisy pickup, usually");
  for (const k of ["stuck", "dropout", "drifting", "railed"])
    assert.equal(h.verbs.clean.odds[k], 0, `clean does nothing for ${k}`);
  assert.equal(h.verbs.recal.odds.drifting, 0.95, "recalibrate is THE drift fix");
  for (const k of ["stuck", "dropout", "noisy", "railed"])
    assert.equal(h.verbs.recal.odds[k], 0, `recalibrate does nothing for ${k}`);
  assert.equal(h.verbs.restart.odds.drifting, 0, "restart still never fixes drift");
  assert.equal(h.verbs.restart.odds.railed, 0, "nor railing");
  // and the prescription map points each fault at its cheapest correct verb
  assert.equal(h.verbFor("stuck"), "restart");
  assert.equal(h.verbFor("dropout"), "restart");
  assert.equal(h.verbFor("noisy"), "clean");
  assert.equal(h.verbFor("drifting"), "recal");
  assert.equal(h.verbFor("railed"), "service", "railed is hardware - only the crew");

  // EXECUTED: clean on a STUCK sensor is the wrong verb - it fails and locks
  const s = h.freshState();
  const mixer = s.machines[0];
  mixer.cond = "stuck"; mixer.stuckAt = 2;
  assert.ok(h.maintainWith(s, "mixer", "clean"), "the wrong verb is accepted - guessing is allowed");
  for (let i = 0; i < 12 * 8; i++) h.stepWith(s, 1 / 12);
  assert.equal(mixer.cond, "stuck", "and it fixed nothing");
  assert.ok(mixer.lockout > 50, "wrong verb: the 60s lockout");

  // EXECUTED: recalibrate on a DRIFTING sensor succeeds (seed 2 draws 0.03 < 0.95)
  const s2 = h.freshState();
  const oven = s2.machines[1];
  oven.cond = "drifting"; oven.driftLie = 3; oven.seed = 2;
  const coins = s2.coins;
  assert.ok(h.maintainWith(s2, "oven", "recal"));
  assert.equal(s2.coins, coins - h.verbs.recal.cost, "recalibration invoices its fee");
  for (let i = 0; i < 12 * 10; i++) h.stepWith(s2, 1 / 12);
  assert.equal(oven.cond, "none", "the right verb, and the drift is gone");
  assert.equal(oven.lockout, 0, "no lockout for the right call");
});

test("v25: the RIGHT verb failing its dice is not a wrong call - no lockout, try again", () => {
  const h = loadHook();
  const s = h.freshState();
  const mixer = s.machines[0];
  mixer.cond = "stuck"; mixer.stuckAt = 2;
  /* seed 29's next draw is 0.902, above restart's 0.8 odds for stuck - a
     deterministic unlucky roll on the DOCTRINALLY CORRECT verb. Punishing it
     with the lockout would make Nano's own advice read as a lie. */
  assert.ok(h.restartWith(s, "mixer"));
  mixer.seed = 29;
  for (let i = 0; i < 12 * 3; i++) h.stepWith(s, 1 / 12);
  assert.equal(mixer.cond, "stuck", "the roll failed");
  assert.equal(mixer.lockout, 0, "but the right verb never locks");
  assert.ok(h.restartWith(s, "mixer"), "so you may immediately try again");
});

test("v25: INSPECT reveals the fault kind, fixes nothing, and never locks - even during a lockout", () => {
  const h = loadHook();
  const s = h.freshState();
  const packer = s.machines[2];
  packer.cond = "railed";
  assert.ok(h.inspectWith(s, "packer"), "inspection accepted");
  for (let i = 0; i < 12 * (h.inspectSecs + 1); i++) h.stepWith(s, 1 / 12);
  assert.equal(packer.inspected, true, "the kind is now known");
  assert.equal(packer.cond, "railed", "inspection fixed nothing - it is diagnosis");
  assert.equal(packer.lockout, 0, "and it never locks");
  assert.match(h.inspectWord.railed, /RAILED/, "the reveal names the kind");
  assert.match(h.inspectWord.railed, /service/i, "and points at the doctrine's verb");
  assert.match(h.inspectWord.none, /honest/i, "inspecting a healthy sensor says so");
  // diagnosis stays available through a maintenance lockout
  const s3 = h.freshState();
  const mixer3 = s3.machines[0];
  mixer3.cond = "drifting"; mixer3.lockout = 45;
  assert.equal(h.restartWith(s3, "mixer"), false, "maintenance is locked");
  assert.ok(h.inspectWith(s3, "mixer"), "but you may still LOOK");
});

test("v25: the dial hint leads when the needle shows outside the band - the free fix, said first", () => {
  const h = loadHook();
  const s = h.freshState();
  // pure and display-keyed: it reads the SHOWN value only, so it neither
  // peeks at the fault state nor leaks it (a railed display also hints,
  // and the player learns the next rung when adjusting visibly does nothing)
  assert.equal(h.dialHintWith(s, "mixer", 2.5), null, "inside the band: no hint");
  assert.equal(h.dialHintWith(s, "mixer", 6.2).dir, "down", "above it: bring SPEED down");
  assert.match(h.dialHintWith(s, "mixer", 6.2).label, /free first move/i, "and it says the fix is free");
  assert.equal(h.dialHintWith(s, "oven", 140).dir, "up", "below it: bring HEAT up");
  assert.equal(h.dialHintWith(s, "mixer", null), null, "a dropped-out reading hints nothing");
  // the hint LEADS the slot, before any maintenance verb is offered
  assert.match(js, /the dial hint LEADS the slot/i, "stated where it is built");
  assert.match(js, /steered by the UI toward RESTART/,
    "and the reason - the founder's cascade - is recorded at the function");
});

test("v25: concurrent draws avoid each other's records - the chorus fix", () => {
  const h = loadHook();
  // a pool of three same-truth records; excluding the two on display must
  // yield the third, whatever the seed
  const mk = (id) => ({ truth: "noisy", node_id: id, child: { prediction: "noisy", margin: 2 },
    parent: { prediction: "noisy", margin: 3 }, window: { tag: "X_VIBRATION" } });
  const pool = [mk("a"), mk("b"), mk("c")];
  for (let seed = 0; seed < 7; seed++) {
    const got = h.sampleWith(pool, "vib", "noisy", seed, ["a", "b"]);
    assert.equal(got.record.node_id, "c", "the un-displayed record is drawn");
  }
  // but a pool of one is a pool of one: a real record beats an empty slot
  const solo = h.sampleWith([mk("a")], "vib", "noisy", 3, ["a"]);
  assert.equal(solo.record.node_id, "a", "exclusion never empties the draw");
  // and the live paths pass the exclusion list
  assert.ok(js.includes('sampleFor(m.spec.sensor.kind, pick, Math.floor(rnd(m) * 997), activeRecordIds(m.id))'),
    "fault draws exclude what the other machines show");
  assert.ok(js.includes('sampleFor(m.spec.sensor.kind, "none", Math.floor(rnd(m) * 997), activeRecordIds(m.id))'),
    "healthy draws too");
});

test("v25: automation runs the cheapest correct verb on Nano's advice", () => {
  const h = loadHook();
  const s = h.freshState();
  s.nano = true; s.micro = 1;
  const oven = s.machines[1];
  oven.auto = true; oven.cond = "noisy"; oven.condAge = 3;
  oven.picoRead = { kind: "caught", said: "noisy", margin: 2.2, truth: "noisy" };
  h.autoWith(s, "oven", 1 / 12);
  assert.ok(oven.restarting > 0, "a verb is running");
  assert.equal(oven.fixVerb, "clean", "and it is CLEAN - the doctrine's verb for noise, not a blanket service");
  assert.match(oven.autoNote, /CLEAN on Nano's advice/i, "attributed");
});

test("v25: the floor sells its own upgrades - buy points on the machines and the desk", () => {
  // an empty Pico mount is a dashed buy tag on the machine body; the
  // nameplate sells the next Mk; the desk sells its next model. The shop
  // overlay remains the full view - these are the visible storefront.
  assert.match(js, /clf-buytag", function \(e\) \{\s*e\.stopPropagation\(\);\s*buyPico\(m\.id\);/,
    "the empty mount buys Pico in place");
  assert.match(js, /clf-buytag clf-buytag--tier/, "the nameplate sells the next tier");
  assert.match(js, /clf-buytag clf-buytag--desk/, "the desk sells its next model");
  // AMENDED v29: priced entries carry their purchase kind so the
  // affordability glow can tell models from Mk upgrades
  assert.match(js, /DOM\.priced = DOM\.priced \|\| \[\]\)\.push\(\{ b: tag, cost: MODEL_PRICE\.pico, kind: "pico" \}/,
    "floor prices dim with the wallet like every other price");
  assert.match(css, /\.clf-buytag:disabled \{ opacity: \.38/, "short-wallet state is visible");
});

test("v25: the not-sure copy explains itself, and the maintenance card prints the doctrine", () => {
  // AMENDED v26: the inline number pair became the margin METER; the mesh
  // pattern (the number, what it is, the bar it failed) now lives in the
  // meter's tooltip and geometry, asserted in the v24 cadence lock above.
  assert.ok(js.includes('"not sure" + (m.unsureRun > 1 ? " \\u00d7" + m.unsureRun : "")'),
    "a run of doubts collapses into one line with a count, not a chirp per redraw");
  assert.ok(!/not sure[^"]*only/.test(js), "the opaque 'only 1.4' phrasing is gone");
  assert.match(js, /MAINTENANCE CARD · what fixes what/, "the doctrine is printed for study");
  assert.match(js, /Nano quotes this card instantly; INSPECT learns it the slow way/,
    "and the card prices the models honestly: they sell time, not secrets");
});

test("v25/v27: through a child the gateway hears instantly; bare machines wait for the sweep", () => {
  // AMENDED v27. The v25 rule was "no Pico, no Nano report" - right for the
  // escalation story, wrong to the measured data: parent-direct is the
  // highest-accuracy config the bench recorded, so a lone Nano CAN read a
  // bare machine. What survives of the old lock is the immediacy contrast:
  // at fault-start a bare machine still has NO report (the sweep has not
  // arrived), where a child's report is instant.
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  s.nano = true;
  const mixer = s.machines[0];        // no pico mounted
  mixer.nextFault = 0.01;
  for (let i = 0; i < 6; i++) h.stepWith(s, 1 / 12);
  assert.notEqual(mixer.cond, "none", "a fault started");
  assert.equal(mixer.nanoRead, null, "no instant report without a child - the sweep takes time");
  assert.match(js, /gateway hears THROUGH the child instantly/i, "the rule is stated where it lives");
});

/* ---- v27: NANO-DIRECT + THE HUMAN HANDOFF ------------------------------- */

test("v27: nano-direct arrives on the sweep delay and rides the recorded PARENT fields", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  s.nano = true;
  s.machines.forEach((m) => { m.nextFault = 9999; });   // no surprise faults
  h.conditionWith(s, "mixer", "stuck");
  const mixer = s.machines[0];
  assert.notEqual(mixer.cond, "none");
  // before the sweep lands: nothing (the delay IS the cost of direct watch)
  for (let i = 0; i < Math.floor(4 * 12); i++) h.stepWith(s, 1 / 12);
  assert.equal(mixer.nanoRead, null, `no read at 4s - the patrol sweep fires every ${h.nanoSweepSecs}s`);
  // after: the read exists, flagged direct, and its word IS the recorded
  // parent prediction of the drawn record - recorded misses stay misses
  for (let i = 0; i < Math.floor(7 * 12); i++) h.stepWith(s, 1 / 12);
  assert.ok(mixer.nanoRead, "the sweep delivered");
  assert.equal(mixer.nanoDirect, true, "and it is signed as a direct read");
  assert.equal(mixer.nanoRead.said, mixer.sample.record.parent.prediction,
    "what Nano says direct is the recorded parent's word - nothing invented");
  assert.equal(mixer.nanoRead.kind,
    mixer.sample.record.parent.prediction === mixer.sample.record.truth ? "resolved" : "missed",
    "and a recorded parent miss arrives just as wrong");
});

test("v27: the gateway watches ONE machine - the budget is the product argument", () => {
  // The gateway PATROLS in auto (belief-driven - the first cut auto-followed
  // "the newest fault", which meant navigating by a secret no model had read
  // yet). Pinning makes the budget deterministic to test: a pinned gateway
  // reads its one machine and nothing else, however long the other burns.
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  s.nano = true;
  s.machines.forEach((m) => { m.nextFault = 9999; });
  s.nanoWatch = "mixer";                                // the player pins it
  h.conditionWith(s, "mixer", "stuck");
  h.conditionWith(s, "oven", "drifting");
  assert.equal(h.watchWith(s), "mixer", "the WATCHING pin overrides the patrol");
  for (let i = 0; i < Math.floor(20 * 12); i++) h.stepWith(s, 1 / 12);
  const mixer = s.machines[0], oven = s.machines[1];
  assert.ok(mixer.nanoRead, "the pinned machine got its direct read");
  assert.equal(oven.nanoRead, null,
    "the other faulted bare machine got NOTHING - the gateway cannot be everywhere");
  // busy = the gateway BELIEVES its target is in trouble - its own delivered
  // read said an alarm word. Never the game's secret.
  mixer.nanoRead = { kind: "resolved", said: "stuck", margin: 2, truth: "stuck" };
  assert.equal(h.busyWith(s), true, "an alarmed read at the target = spread thin");
  mixer.nanoRead = { kind: "missed", said: "none", margin: 2, truth: "stuck" };
  assert.equal(h.busyWith(s), false,
    "a recorded parent miss says none - the gateway is honestly fooled, not busy");
});

test("v27: a Pico frees the gateway", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  s.nano = true;
  s.coins = 500;
  s.machines.forEach((m) => { m.nextFault = 9999; });
  h.conditionWith(s, "mixer", "stuck");
  assert.equal(h.watchWith(s), "mixer", "the patrol starts at the first bare machine");
  assert.equal(h.buyPicoWith(s, "mixer"), true);
  const mixer = s.machines[0];
  assert.ok(mixer.picoRead, "the new child reports instantly on the open fault");
  assert.ok(mixer.nanoRead, "and the gateway hears it instantly too");
  assert.equal(mixer.nanoDirect, false, "as a child report, not a direct read");
  assert.notEqual(h.watchWith(s), "mixer",
    "the gateway stops patrolling a machine that has its own child");
  // with a Pico on every machine there is nothing left to patrol at all
  s.machines.forEach((m) => { m.pico = true; });
  assert.equal(h.watchWith(s), null, "all children mounted - the gateway is fully free");
  // AMENDED v28 (voice pass): "that is what the children are for" read as
  // doctrine-speak. Same guarantee - the one-unit budget is explained at the
  // gateway's station - in a human voice.
  assert.match(js, /can't be in three places at once/,
    "and the why is printed at the gateway's station");
  assert.match(js, /That's what the Picos are for/, "with the children named plainly");
});

test("v27: the double-miss hands off to the human, and the catch is acknowledged", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  const mixer = s.machines[0];
  // a genuine recorded chain miss: both watching models said the wrong thing
  mixer.cond = "stuck";
  mixer.picoRead = { kind: "wrong", said: "none", margin: 2.0, truth: "stuck" };
  mixer.nanoRead = { kind: "missed", said: "none", margin: 2.0, truth: "stuck" };
  assert.equal(h.chainMissed(mixer), true, "models watching, none named the truth");
  h.clearWith(s, "mixer");
  assert.equal(s.humanSaves, 1, "the fix could only have been a person - and it counts");
  assert.ok(s.ackUntil > 0, "and the acknowledgment beat is armed");
  // no models watching: honest silence, no medal for routine work
  const oven = s.machines[1];
  oven.cond = "noisy";
  oven.picoRead = null; oven.nanoRead = null;
  assert.equal(h.chainMissed(oven), false, "no model watching is not a chain miss");
  h.clearWith(s, "oven");
  assert.equal(s.humanSaves, 1, "no acknowledgment when nothing was missed by a model");
  // a caught fault is the models' save, never the human handoff
  const packer = s.machines[2];
  packer.cond = "drifting";
  packer.picoRead = { kind: "caught", said: "drifting", margin: 3.0, truth: "drifting" };
  assert.equal(h.chainMissed(packer), false);
  // AMENDED v28 (voice pass): the founder called "the ladder ends with a
  // person" robotic. The guarantee is unchanged - the double-miss must steer
  // the player to inspect it themselves - re-anchored to the steer itself.
  assert.match(js, /This one's yours: INSPECT it/,
    "the double-miss hands the player the next move, plainly");
  assert.match(js, /is-doctrine", chainMissed\(m\)/,
    "and INSPECT lights on the handoff the way known-kind lights a verb");
});

/* ---- v26: THE PLAYTEST ROUND ------------------------------------------- */

test("v26: a live run opens calm, then teaches the core lie once, on a real record", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  h.begin(s);                                     // what boot()/reset() arm
  assert.equal(s.graceLeft, h.graceSecs, "the grace period is armed");
  // step to just short of the grace boundary: nothing may fault
  for (let i = 0; i < 12 * (h.graceSecs - 1); i++) h.stepWith(s, 1 / 12);
  assert.ok(s.machines.every((m) => m.cond === "none"), "the opening is calm");
  // cross the boundary: the ONE taught fault lands, on the mixer, STUCK
  for (let i = 0; i < 12 * 3; i++) h.stepWith(s, 1 / 12);
  const mixer = s.machines[0];
  assert.equal(mixer.cond, "stuck", "the taught fault is the core lie");
  assert.equal(mixer.sample.record.truth, "stuck",
    "and it rides a real recorded stuck window like every other fault");
  assert.ok(s.log.some((l) => /does that seem right/.test(l)),
    "the radio points at it instead of leaving the newcomer to mash buttons");
  // and nothing else faults until the lesson is cleared
  for (let i = 0; i < 12 * 30; i++) h.stepWith(s, 1 / 12);
  assert.ok(s.machines[1].cond === "none" && s.machines[2].cond === "none",
    "one lesson at a time - the rest of the line waits");
});

test("v26: the cascade cap - no machine draws a new fault while any lockout runs", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  const oven = s.machines[1];
  oven.lockout = 30;                              // one wrong verb, mid-penalty
  s.machines.forEach((m) => { m.nextFault = 0.05; });  // all about to fault
  for (let i = 0; i < 12 * 5; i++) h.stepWith(s, 1 / 12);
  assert.ok(s.machines.every((m) => m.cond === "none"),
    "the scheduler waits: a lockout is a lesson, not an invitation to pile on");
  assert.match(js, /PACING CANON/, "and the rule is written down as canon");
});

test("v26: THE PRODUCT CLAIM, executed - the informed operator out-earns service-spam", () => {
  /* The playtest's headline: as tuned in v25, blind SERVICE-spam beat
     verb-diagnosis by hundreds of coins, refuting the game's own sales
     pitch. This drives two identical plants through the SAME deterministic
     fault schedule - one operator services everything, one uses the
     doctrine's cheapest correct verb - and the informed one must come out
     ahead. If a rebalance ever flips this again, this test is the alarm. */
  const h = loadHook();
  const SCHEDULE = [
    [20, "mixer", "stuck"], [50, "oven", "drifting"], [80, "packer", "noisy"],
    [110, "mixer", "dropout"], [140, "oven", "railed"], [170, "packer", "stuck"],
    [200, "mixer", "noisy"], [230, "oven", "dropout"], [260, "packer", "drifting"],
    [290, "mixer", "stuck"], [320, "oven", "noisy"],
  ];
  function run(strategy) {
    const s = h.freshState();
    s.records = measured.records;
    s.contract.target = 1e9;                     // no win pause mid-drive
    let due = SCHEDULE.slice();
    for (let t = 0; t < 360; t += 1 / 12) {
      // isolate the maintenance economy: no creep, no natural faults
      s.machines.forEach((m) => { m.ambient = 0; m.event = null; m.eventLeft = 999; m.nextFault = 999; });
      while (due.length && t >= due[0][0]) {
        const [, id, kind] = due.shift();
        const m = s.machines.find((x) => x.id === id);
        if (m.cond === "none" && !m.servicing && !m.restarting) h.conditionWith(s, id, kind);
      }
      s.machines.forEach((m) => {
        if (m.cond === "none" || m.servicing > 0 || m.restarting > 0 || m.inspecting > 0 || m.lockout > 0) return;
        if (strategy === "spam") { h.serviceWith(s, m.id); return; }
        const verb = h.verbFor(m.cond);
        if (verb === "service") h.serviceWith(s, m.id);
        else h.maintainWith(s, m.id, verb);
      });
      h.stepWith(s, 1 / 12);
    }
    return s;
  }
  const informed = run("informed");
  const spam = run("spam");
  assert.ok(informed.coins > spam.coins,
    `diagnosis must pay: informed ${informed.coins.toFixed(0)} vs spam ${spam.coins.toFixed(0)}`);
  assert.ok(informed.coins - spam.coins > 100,
    "and by a margin a player would feel, not a rounding error");
});

test("v26: the win is a state, the next contract re-rolls harder, and Pico ads stop", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  s.cookies = 99.9;
  s.machines.forEach((m) => { m.nextFault = 999; });
  for (let i = 0; i < 12 * 3; i++) h.stepWith(s, 1 / 12);
  assert.ok(s.won, "crossing the target is a real state change");
  assert.ok(s.log.some((l) => /Contract filled/.test(l)), "and it is announced");
  const next = h.nextTargetOf(s.contract);
  assert.equal(next, 250, "the first re-roll is the 250-cookie contract");
  assert.match(js, /creep: G\.contract\.creep \* 1\.35/, "and conditions creep faster on it");
  assert.match(js, /if \(filled\) \{/, "the goal chip switches to the next contract");
  assert.match(js, /CONTRACT FILLED/, "the win card exists with its stamp");
  assert.match(js, /KEEP RUNNING THIS LINE/, "and staying on the current line is a choice");
});

test("v26: pico owns a recorded miss once the truth surfaces - character, not spam", () => {
  const h = loadHook();
  assert.equal(h.ownsMiss(null), null);
  assert.equal(h.ownsMiss({ kind: "caught", said: "stuck" }), null, "a catch owes no apology");
  assert.equal(h.ownsMiss({ kind: "unsure", said: "none" }), null, "doubt is not a miss");
  const owned = h.ownsMiss({ kind: "wrong", said: "none", margin: 2.1, truth: "stuck" });
  assert.match(owned, /I said none - I was wrong/, "the confident lie is owned in first person");
  // AMENDED v28 (voice pass): same guarantee - the admission is attributed
  // to the replay, not to drama - said like a colleague, not a protocol.
  assert.match(owned, /Same miss it made in the recording/, "and attributed to the replay, not to drama");
  assert.match(js, /ownsMiss\(m\.picoRead\)/, "and it fires where incidents clear");
});

test("v26: debt has one tooth - shipping pays less on loan, and COINS never vanishes", () => {
  const h = loadHook();
  const solvent = h.freshState(); solvent.machines.forEach((m) => { m.nextFault = 999; });
  const broke = h.freshState(); broke.machines.forEach((m) => { m.nextFault = 999; });
  broke.coins = -500;
  for (let i = 0; i < 12 * 20; i++) { h.stepWith(solvent, 1 / 12); h.stepWith(broke, 1 / 12); }
  const earnedSolvent = solvent.coins - 120;
  const earnedBroke = broke.coins - -500;
  assert.ok(earnedBroke > 0, "debt still climbs toward zero - no dead end");
  assert.ok(earnedBroke < earnedSolvent * 0.85,
    "but the crew takes its cut: on-loan shipping pays less than solvent shipping");
  assert.match(js, /DOM\.loanFlag\.hidden = G\.coins >= 0/,
    "the loan is a FLAG beside COINS - the money stat itself never disappears");
});

test("v26: quality-of-life locks - quotes, inspect timer, reset arming, no-reading label", () => {
  assert.ok(!js.includes('\\u201c " +'), "the stray space inside model quotes is gone");
  // INSPECT cannot be restarted mid-look
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  assert.ok(h.inspectWith(s, "mixer"), "an inspect starts");
  assert.equal(h.inspectWith(s, "mixer"), false, "and re-clicking cannot restart the timer");
  assert.match(js, /RESET\?/, "reset arms before it fires");
  assert.match(js, /NO READING - the wire went quiet/, "a dropped-out reading is labelled, not bare dashes");
  assert.match(js, /is already at/, "a dial at its stop says so instead of hinting the impossible");
  assert.match(js, /drift = Math\.min\(capSpan/, "and the unwatched walk is capped, not unbounded");
});

/* ---- v28: THE CERTIFICATE, THE BOARD, THE VISIBLE PATROL ----------------- */

test("v28: the robot voice is retired from every player-facing string", () => {
  // The founder: "don't speak like 'the ladder ends with a person' it's very
  // robotic." The facts stay (record ids, recorded-miss admissions, the
  // sim-vs-replay fine print); the delivery is a colleague on the radio.
  // EXTENDED (sitewide tone audit): the founder then caught "Buy the ladder
  // when you get tired of guessing." surviving on the PAGE (playbox.html) after
  // the in-game pass - salesy needling is the same offense as robot doctrine.
  // The lock now sweeps the page surface too, and retires the needling register.
  const page = readFileSync(path.join(SRC, "playbox.html"), "utf8");
  for (const phrase of [
    "the ladder ends with a person",
    "that is what the children are for",
    "That is what the record shows",
    "it follows the newest fault",          // pre-patrol copy: also untrue
    "tired of guessing",                    // needling: buy when you fail
    "buy the ladder",                       // imperative shop-speak (legend was "buy the ladder ·")
    "Buy the Wave ladder",
  ]) {
    assert.ok(!js.includes(phrase) && !page.includes(phrase),
      `retired phrasing must not return: "${phrase}"`);
  }
  assert.match(js, /Nice save - some reads just need a person/,
    "the human-catch beat stays, said like a person");
});

test("v28: the certificate - gear checklist, then a hands-off proof that resets on touch", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  s.machines.forEach((m) => { m.nextFault = 99999; });
  // an unfinished factory can never be mid-proof
  assert.equal(h.certReadyWith(s), false);
  assert.equal(h.certItemsWith(s).length, 5, "four gear items plus the proof");
  assert.equal(h.certItemsWith(s)[4].key, "proof", "the proof is the last line");
  for (let i = 0; i < 24; i++) h.stepWith(s, 1 / 12);
  assert.equal(s.cert.run, 0, "the proof clock does not move before the checklist is done");
  // finish the checklist: Mk III everywhere, Picos everywhere, full desk, all handed over
  s.machines.forEach((m) => { m.tier = 2; m.pico = true; m.auto = true; });
  s.nano = s.giga = true;   // desk coverage via the Giga route (micro stays a count)
  assert.equal(h.certReadyWith(s), true, "gear done - the proof may begin");
  for (let i = 0; i < 12 * 10; i++) h.stepWith(s, 1 / 12);
  assert.ok(s.cert.run > 9, "hands off, the proof clock climbs (autonomy moves do not count as touching)");
  h.touchWith(s);
  assert.equal(s.cert.run, 0, "a hand on the plant restarts the clock");
  // run the full window untouched and healthy
  for (let i = 0; i < 12 * 185; i++) h.stepWith(s, 1 / 12);
  assert.equal(s.cert.done, true, "three untouched minutes at full uptime: certified");
  // the uptime gate: a window that ran below the bar resets instead of certifying
  const s2 = h.freshState();
  s2.records = measured.records;
  s2.machines.forEach((m) => { m.nextFault = 99999; m.tier = 2; m.pico = true; m.auto = true; });
  s2.nano = s2.micro = s2.giga = true;
  s2.cert.run = 179.5; s2.cert.up = 100;   // 56% uptime so far - a bad window
  for (let i = 0; i < 12; i++) h.stepWith(s2, 1 / 12);
  assert.equal(s2.cert.done, false, "a window below the uptime bar does not certify");
  assert.ok(s2.cert.run < 10, "it starts a fresh window instead");
  assert.match(js, /FACTORY CERTIFICATE/, "the plaque panel exists");
  assert.match(js, /FACTORY CERTIFIED/, "and the campaign win has its stamp");
  assert.match(js, /KEEP IT RUNNING/, "the certified line keeps running as the exhibit");
});

test("v28: the site board is the game's own arithmetic, and it names the bottleneck", () => {
  const h = loadHook();
  const s = h.freshState();
  s.machines[0].upT = 30; s.machines[0].runT = 60;   // mixer: 50% uptime
  s.machines[1].upT = 60; s.machines[1].runT = 60;
  s.machines[2].upT = 59; s.machines[2].runT = 60;
  const sb = h.siteBoardWith(s);
  assert.equal(Math.round(sb.rows[0].up * 100), 50, "uptime is upT over runT, nothing else");
  assert.equal(sb.worst.id, "mixer", "the worst machine is the bottleneck");
  assert.match(sb.line, /the mixer is holding you back - 50% uptime/);
  // a clean line says so instead of inventing a problem (98.3% is still a
  // named bottleneck - the bar for "clean" is deliberately high)
  s.machines[0].upT = 60; s.machines[2].upT = 60;
  assert.match(h.siteBoardWith(s).line, /no bottleneck/);
  // one source of truth: the wall board and the desk card both read siteBoard()
  assert.match(js, /var sb2 = siteBoard\(\);\s*\/\/ the desk card and the wall board share one source/);
  assert.match(js, /SITE BOARD \\u00b7 MICRO/, "and the board hangs on the floor when Micro is owned");
});

test("v28: the patrol marker follows the gateway's belief, never the game's secret", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  s.nano = true;
  s.machines.forEach((m) => { m.nextFault = 99999; });
  s.sweepAt = "mixer"; s.sweepLeft = 6;
  // a secret fault lands on the oven - no model has read it
  h.conditionWith(s, "oven", "stuck");
  assert.equal(s.machines[1].nanoRead, null, "no read exists yet");
  assert.equal(h.watchWith(s), "mixer",
    "the watch target does not jump to a fault nobody has read - no answer leak");
  // and the floor tag renders from that same belief-driven target
  assert.match(js, /watchTarget\(\) === m\.id;?\s*\n\s*s\.gwtag\.hidden = !watchedHere/,
    "the gateway tag on the floor rides watchTarget() and nothing else");
});

test("v28: pico's tally counts only faults it actually named", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  const [mixer, oven, packer] = s.machines;
  // a catch: pico named the truth before the fix landed
  mixer.cond = "stuck"; mixer.pico = true;
  mixer.picoRead = { kind: "caught", said: "stuck", margin: 3.0, truth: "stuck" };
  h.clearWith(s, "mixer");
  assert.equal(mixer.picoCatches, 1, "a named fault lands on the badge");
  assert.equal(s.modelCalled, 1, "and on the HUD tally");
  // a recorded miss is not a catch
  packer.cond = "drifting"; packer.pico = true;
  packer.picoRead = { kind: "wrong", said: "none", margin: 2.0, truth: "drifting" };
  h.clearWith(s, "packer");
  assert.equal(packer.picoCatches, 0, "a recorded miss never pads the tally");
  assert.equal(s.modelCalled, 1);
  // the gateway's resolve counts for the HUD, not for a pico badge
  oven.cond = "noisy";
  oven.nanoRead = { kind: "resolved", said: "noisy", margin: 2.0, truth: "noisy" };
  h.clearWith(s, "oven");
  assert.equal(oven.picoCatches, 0);
  assert.equal(s.modelCalled, 2, "the gateway's call counts as a model call");
  assert.match(js, /clf-badge__n/, "the tally rides the badge itself");
  assert.match(js, /\[\"CAUGHT\", DOM\.caught\]/, "and CAUGHT rides the HUD beside BURNT");
});

test("v28: Micros stack to three, and more boxes is not more brain", () => {
  const h = loadHook();
  const s = h.freshState();
  s.nano = true; s.coins = 5000;

  // the purchase path: micro is a COUNT, repeatable to three, then it stops
  assert.equal(s.micro, 0, "micro starts as a count");
  assert.equal(h.buyDeskWith(s, "micro"), true);
  assert.equal(h.buyDeskWith(s, "micro"), true);
  assert.equal(h.buyDeskWith(s, "micro"), true);
  assert.equal(s.micro, 3, "three Micros on the desk");
  assert.equal(h.buyDeskWith(s, "micro"), false, "and no fourth");
  assert.equal(s.coins, 5000 - 3 * h.prices.micro, "each one was paid for");
  assert.ok(3 * h.prices.micro > h.prices.giga,
    "the documented economy choice: scaling OUT (3 Micros) costs more than scaling UP (Giga)");
  assert.equal(h.reachWith(s), 3, "three Micros reach every dial - coverage by quantity");

  // the coordination gap, executed: a Micro acts on its own slow cycle,
  // so within one cycle a second nudge does NOT land; Giga is continuous.
  // AMENDED v31, with cause: "continuous" used to mean a step EVERY FRAME,
  // which is how a lying sensor dragged the founder's oven to the 240-degree
  // stop in about a second. Giga now corrects on a bounded step budget
  // (GIGA_STEPS_PER_SEC) - still no per-machine cycle, still faster than a
  // Micro's 4-second look, no longer instantaneous. The gap this lock
  // protects survives: over one second Giga moves; mid-cycle, a Micro does
  // not.
  const m = s.machines[0];
  m.auto = true; m.cond = "stuck"; m.stuckAt = 0.2; m.real = 4.9;
  h.autoWith(s, "mixer", 1 / 12);
  const afterFirst = m.set;
  m.real = 4.9; m.stuckAt = 0.2;
  h.autoWith(s, "mixer", 1 / 12);
  assert.equal(m.set, afterFirst, "a Micro mid-cycle does nothing - its next look is seconds away");
  // Giga has no per-machine cycle: within one second of frames it corrects,
  // and within the next second it corrects again - while the Micro above
  // would still be waiting out its 4s look
  const s2 = h.freshState();
  s2.giga = true;
  const m2 = s2.machines[0];
  m2.auto = true; m2.cond = "stuck"; m2.stuckAt = 0.2; m2.real = 4.9;
  const g0 = m2.set;
  for (let i = 0; i < 12; i++) { m2.real = 4.9; m2.stuckAt = 0.2; m2.cond = "stuck"; h.autoWith(s2, "mixer", 1 / 12); }
  const g1 = m2.set;
  assert.notEqual(g1, g0, "Giga corrects within a second - no cycle gap");
  for (let i = 0; i < 12; i++) { m2.real = 4.9; m2.stuckAt = 0.2; m2.cond = "stuck"; h.autoWith(s2, "mixer", 1 / 12); }
  assert.notEqual(m2.set, g1, "and keeps correcting the next second - continuous, now rate-bounded");

  // the certificate accepts EITHER full-coverage route
  const s3 = h.freshState();
  s3.machines.forEach((mm) => { mm.tier = 2; mm.pico = true; mm.auto = true; });
  s3.nano = true; s3.micro = 3; s3.giga = false;
  assert.equal(h.certReadyWith(s3), true, "three Micros satisfy the desk item without a Giga");
  s3.micro = 2;
  assert.equal(h.certReadyWith(s3), false, "two Micros leave a dial unheld - no certificate");

  // attribution names the acting box, so the floor shows WHO nudged
  assert.match(js, /function holderOf\(/, "attribution goes through one holder function");
  assert.match(js, /"Micro-" \+/, "stacked Micros are numbered on their tags");
  assert.match(js, /don't talk to each other/, "the shop says the quiet part out loud");
});

test("v28: playtest round 2 - verdicts silence coaching, absurd readings confess, the win waits for the tutorial", () => {
  const h = loadHook();
  const s = h.freshState();
  const oven = s.machines[1];
  const t = h.tiers.oven[0];

  // a delivered verdict silences the process hint - no more contradictions
  const outside = t.hi + 3;
  assert.ok(h.dialHintWith(s, "oven", outside), "with nothing delivered, the coaching shows");
  oven.cond = "railed";
  oven.nanoRead = { kind: "resolved" };
  assert.equal(h.dialHintWith(s, "oven", outside), null,
    "a delivered verdict owns the card - the dial coaching yields");
  oven.nanoRead = null; oven.inspected = true;
  assert.equal(h.dialHintWith(s, "oven", outside), null, "INSPECT's verdict silences it too");
  oven.inspected = false; oven.picoRead = { kind: "caught", said: "railed", margin: 2 };
  assert.equal(h.dialHintWith(s, "oven", outside), null, "so does a Pico call");
  oven.picoRead = { kind: "unsure", said: "none", margin: 0.4 };
  assert.ok(h.dialHintWith(s, "oven", outside), "but an UNSURE is not a verdict - coaching stays");

  // an impossible reading is named as the sensor talking, not process-coached
  oven.picoRead = null; oven.cond = "none";
  const lie = h.dialHintWith(s, "oven", -13.6);
  assert.equal(lie.dir, "none", "no dial advice for a physically impossible number");
  assert.match(lie.label, /the sensor talking/, "the hint says whose voice that number is");

  // the win card waits for the taught sequence, and the buyer tips on the fill
  const s2 = h.freshState();
  s2.taught = true; s2.taughtCleared = false;
  s2.cookies = s2.contract.target + 5;
  h.stepWith(s2, 1 / 12);
  assert.equal(s2.won, false, "mid-tutorial, the contract holds its fire");
  s2.taughtCleared = true;
  const before = s2.coins;
  h.stepWith(s2, 1 / 12);
  assert.equal(s2.won, true, "tutorial done, the fill lands");
  assert.ok(s2.coins >= before + 100 + 50 * s2.contract.level,
    "and the completion bonus arrives - the mid-game ladder stays reachable");

  // the rest of the round-2 fixes, pinned to their strings
  assert.match(js, /did not take - try again/, "a failed correct verb speaks at the station");
  assert.match(js, /cl-ticker/, "the radio's last line rides a visible ticker");
  assert.match(js, /nobody was watching - a Pico would have been/,
    "the recorded-miss line is conditioned on actually owning a model");
  assert.match(js, /RIGHT VERB, FIRST TRY/, "the win card scores the diagnosis");
  assert.match(js, /unlocks with WAVE MICRO/, "the handover destination is visible before it unlocks");
});

/* ===================================================================== */
/* v29 - upgrades you can see, the glow, stable panels, SLA, the tape    */
/* ===================================================================== */

test("v29: a tier upgrade swaps the committed Mk plate, and the reveal is gated", () => {
  const h = loadHook();
  // the sprite rides data-mk off the SAME tier state the plates sell
  assert.match(js, /block\.dataset\.mk = String\(m\.tier \+ 1\)/, "the block is stamped at build");
  assert.match(js, /s\.block\.dataset\.mk !== mkNow/, "and re-stamped when the tier changes");
  assert.match(js, /is-upgrading",\s*!REDUCED/, "the reveal beat is reduced-motion gated");
  assert.match(js, /The new " \+ m\.spec\.name\.toLowerCase\(\) \+ " is in/,
    "the radio announces the new machine either way");
  // every Mk II/III plate the CSS points at is a real committed export
  for (const m of ["mixer", "oven", "packer"]) {
    for (const mk of ["2", "3"]) {
      assert.match(css, new RegExp(`clf-machine--${m}\\[data-mk="${mk}"\\] \\.clf-art`),
        `${m} Mk ${mk} has its own mask rule`);
      readFileSync(path.join(SRC, `assets/wave/game-${m}-mk${mk}-ink.png`));
    }
    // the shop's Mk rows show the plate the upgrade swaps in
    assert.match(css, new RegExp(`cl-node__thumb\\[data-m="${m}"\\]\\[data-mk="2"\\]`));
  }
  // executed: buying a tier stamps the reveal clock
  const s = h.freshState();
  s.coins = 1000;
  h.buyTierWith(s, "mixer");
  assert.equal(s.machines[0].tier, 1);
  assert.ok(s.machines[0].upgradeAt != null, "the upgrade moment is stamped for the reveal");
});

test("v29: one recommendation - the goal chip's NEXT row and the strong glow share recommendedNext", () => {
  const h = loadHook();
  const s = h.freshState();
  // models first: bare machines recommend a Pico, whatever else is affordable
  assert.deepEqual(h.recommendedNextWith(s)[0], { kind: "pico", id: "mixer" });
  s.machines.forEach((m) => { m.pico = true; });
  assert.deepEqual(h.recommendedNextWith(s), [{ kind: "nano" }]);
  s.nano = true;
  assert.deepEqual(h.recommendedNextWith(s), [{ kind: "micro" }]);
  // the fork is a genuine choice: BOTH are recommended, no winner
  s.micro = 1;
  assert.deepEqual(h.recommendedNextWith(s), [{ kind: "micro" }, { kind: "giga" }]);
  // after the model ladder, the Mk line inherits the recommendation
  s.giga = true;
  assert.deepEqual(h.recommendedNextWith(s), [{ kind: "tier", id: "mixer" }]);
  s.machines.forEach((m) => { m.tier = 2; });
  assert.deepEqual(h.recommendedNextWith(s), [], "a maxed plant recommends nothing");
  // the goal chip reads the same source (grep: the NEXT branch keys off it)
  assert.match(js, /\} else if \(recommendedNext\(\)\.length\) \{/,
    "paintGoals' NEXT row derives from recommendedNext");
  assert.match(js, /recommendedNext\(\)\.forEach\(function \(r0\) \{ DOM\.recoKinds\[r0\.kind\] = true/,
    "the glow sweep derives from recommendedNext");
  // glow discipline in CSS: soft afford, strong reco, reduced-motion steady
  assert.match(css, /\.is-afford:not\(:disabled\)/);
  assert.match(css, /\.is-reco:not\(:disabled\)/);
  assert.match(js, /is-reco", afford && !!p\.kind && !!DOM\.recoKinds\[p\.kind\]/,
    "the strong glow only lands on the recommended kind");
  assert.match(js, /G\.coins >= nextTier\.price && m\.pico/,
    "a machine's Mk tag stays quiet until that machine has its Pico - models first");
});

test("v29: the action row never rides on the weather - buttons above the slot", () => {
  // the founder's screenshot: advice blocks mounting above the buttons
  // shoved them up and down. The verbs now sit above the model slot, under
  // fixed-height rows only; the slot reserves space and grows downward.
  const acts = js.indexOf("card.appendChild(acts)");
  const slot = js.indexOf("card.appendChild(s.slot)");
  assert.ok(acts > -1 && slot > -1 && acts < slot,
    "panel(): the action row is appended BEFORE the model slot");
  assert.match(js, /LAYOUT STABILITY/, "and the order is documented as load-bearing");
  assert.match(css, /\.cl-slot \{ min-height: /, "the slot reserves a floor of space");
});

test("v29: SLA stakes - from contract 2 the buyer walks on sustained downtime, never into a dead end", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  // contract 1 has no SLA: a bad opening minute costs nothing extra
  assert.equal(s.contract.sla, 0);
  s.machines.forEach((m) => { m.stopped = true; m.nextFault = 999; m.eventLeft = 999; });
  // force the whole line down for a rolling minute at level 1: no walk
  for (let i = 0; i < 12 * 61; i++) h.stepWith(s, 1 / 12);
  assert.equal(s.contractsLost, 0, "contract 1 never walks");

  // contract 2: the same neglected minute loses the buyer
  const s2 = h.freshState();
  s2.records = measured.records;
  s2.contract = { target: 250, level: 2, creep: 1.35, sla: h.slaBar };
  s2.cookies = 120;
  const coinsBefore = s2.coins;
  s2.machines.forEach((m) => {
    m.cond = "railed"; m.stoppedFor = 99; m.stopped = true;
    m.nextFault = 999; m.eventLeft = 999;
  });
  for (let i = 0; i < 12 * 61 && !s2.contractsLost; i++) h.stepWith(s2, 1 / 12);
  assert.equal(s2.contractsLost, 1, "sustained downtime under the bar loses contract 2");
  assert.ok(s2.log.some((l) => /buyer walked/.test(l)), "and the radio says so plainly");
  assert.ok(s2.log.some((l) => /fresh order/.test(l)), "with the fresh order in the same breath");
  assert.ok(!s2.won && s2.coins <= coinsBefore, "no completion bonus was paid");
  // never a dead end: a same-size order re-opens from the current count
  assert.equal(s2.contract.level, 2, "the level does not reset");
  assert.equal(s2.contract.target, Math.ceil(s2.cookies / 10) * 10 + h.contractSize(2),
    "the fresh order is the same size, counted from here");
  assert.equal(s2.contract.sla, h.slaBar, "and the buyer's expectation stands");
  // accepting the next contract arms the SLA from level 2 on
  assert.match(js, /creep: G\.contract\.creep \* 1\.35, sla: SLA_BAR/,
    "every accepted re-roll carries the uptime stake");
  // the goal chip words the stake, warmly, with the no-dead-end promise
  assert.match(js, /this buyer expects " \+ Math\.round\(G\.contract\.sla \* 100\)/,
    "the SHIP row names the expectation");
  assert.match(js, /a fresh order always follows/, "and promises the desk is never empty");
});

test("v29: the session tape - recorded, capped, persisted on the way out, downloadable", () => {
  const h = loadHook();
  h.tapeReset();
  const s = h.freshState();
  s.records = measured.records;
  // a forced fault lands on the tape with its record id - the honesty trail
  h.conditionWith(s, "mixer", "stuck");
  let ev = h.tapeEvents();
  const fault = ev.find((e) => e.type === "fault");
  assert.ok(fault && fault.m === "mixer" && fault.kind === "stuck");
  assert.ok(fault.record, "the fault names the replayed record");
  // a verb start and its outcome, attributed and executed
  h.maintainWith(s, "mixer", "restart");
  for (let i = 0; i < 12 * 3; i++) h.stepWith(s, 1 / 12);
  ev = h.tapeEvents();
  assert.ok(ev.some((e) => e.type === "verb-start" && e.verb === "restart"));
  const outcome = ev.find((e) => e.type === "verb" && e.m === "mixer");
  assert.ok(outcome && ["cleared", "did-not-take", "locked"].includes(outcome.outcome),
    "the verb resolves to a recorded outcome");
  // purchases land with their what
  s.coins = 1000;
  h.buyPicoWith(s, "oven");
  assert.ok(h.tapeEvents().some((e) => e.type === "buy" && e.what === "pico" && e.m === "oven"));
  assert.ok(s.firstPicoAt != null, "time-to-first-Pico is stamped for the summary");
  // the export is parseable JSON with the summary header and the honesty note
  const tapeDoc = JSON.parse(JSON.stringify(h.buildTapeWith(s)));
  assert.ok(tapeDoc.summary && typeof tapeDoc.summary.shipped === "number");
  assert.match(tapeDoc.honesty, /replayed record fields/);
  // AMENDED v30: the plant radio means chat questions DO leave the machine
  // (only when the player sends one, carrying only summary numbers) - the
  // header now says exactly that instead of the blanket "transmitted
  // nowhere", which would have become a lie the moment TALK shipped.
  assert.match(tapeDoc.honesty, /Nothing leaves your machine except questions you send to Ping/);
  assert.match(tapeDoc.honesty, /only your line's summary numbers/);
  assert.ok(!/transmitted nowhere/.test(tapeDoc.honesty),
    "the obsolete blanket claim is gone");
  assert.ok(Array.isArray(tapeDoc.events) && tapeDoc.events.length > 0);
  // FIFO cap: the tape never grows past its ceiling
  for (let i = 0; i < 2200; i++) h.tapePush("coins", { i });
  assert.equal(h.tapeEvents().length, 2000, "the tape is a 2000-event FIFO");
  h.tapeReset();
  // persistence rides the leave signals, never the tick
  assert.match(js, /window\.addEventListener\("pagehide", persistTape\)/);
  assert.match(js, /persistTape\(\);\s*\/\/ serialize on the way out/);
  assert.match(js, /while \(store\.length > 3\) store\.shift\(\)/, "last three sessions kept");
  // the download affordance says it stays local
  assert.match(js, /SESSION TAPE · download/);
  assert.match(js, /stays " \+\s*"on your machine; download and share it if you want/);
  assert.match(js, /URL\.createObjectURL/, "the export is a Blob URL - CSP-safe, first party");
});

test("v29: the watching row is state - full Pico coverage removes it, partial keeps only bare choices", () => {
  const h = loadHook();
  const s = h.freshState();
  // partial coverage: bare machines are the only button choices
  s.machines[0].pico = true;
  let wo = h.watchOptionsWith(s);
  assert.equal(wo.show, true);
  assert.deepEqual(wo.bare, ["oven", "packer"]);
  assert.deepEqual(wo.covered, ["mixer"]);
  // full coverage: no selector at all - zero watch buttons to be dead
  s.machines.forEach((m) => { m.pico = true; });
  wo = h.watchOptionsWith(s);
  assert.equal(wo.show, false, "nothing to point the gateway at, so no row");
  // and the render follows the state rule, not a disabled-button habit
  assert.match(js, /var wo = watchOptions\(\);\s*if \(wo\.show\) \{/,
    "paintDesk renders the selector only when there is a choice");
  assert.match(js, /cl-watch cl-watch--covered/, "covered machines are chips, not buttons");
  assert.ok(!/wb\.disabled = true;\s*wb\.title = "This machine has a Pico/.test(js),
    "the dead disabled-button row is gone");
  // the status line for full coverage already exists and stays
  assert.match(js, /Every machine has a Pico - the gateway hears them all instantly\./);
});

test("v29: the desk sells the micro-vs-giga choice at the point of purchase", () => {
  const h = loadHook();
  const s = h.freshState();
  // the ladder up to the fork
  assert.deepEqual(h.deskOffersWith(s), ["nano"]);
  s.nano = true;
  assert.deepEqual(h.deskOffersWith(s), ["micro"]);
  // the founder's bug: with one Micro the desk hid "another Micro" behind
  // Giga. Now BOTH tags render - the scale-out-vs-scale-up tradeoff, sold
  // where the money is spent.
  s.micro = 1;
  assert.deepEqual(h.deskOffersWith(s), ["micro", "giga"]);
  s.micro = 2;
  assert.deepEqual(h.deskOffersWith(s), ["micro", "giga"]);
  // buying through to three Micros works from the desk offers alone
  s.coins = 2000;
  assert.ok(h.buyDeskWith(s, "micro"));
  assert.equal(s.micro, 3);
  assert.deepEqual(h.deskOffersWith(s), ["giga"], "full stack: only Giga remains");
  // after Giga, no more Micro tag - Giga already minds every dial
  s.giga = true;
  assert.deepEqual(h.deskOffersWith(s), []);
  assert.match(js, /deskOffers\(\)\.forEach\(function \(offer\)/, "the desk renders the offers rule");
});

/* =====================================================================
   v30 - THE GIGA UNIT: embodiment, belief-driven attention, and the
   plant radio (chat answered live by Ping, honestly labeled)
   ===================================================================== */
test("v30: the Unit's attention is belief-driven - a secret fault cannot summon it", () => {
  const h = loadHook();
  const s = h.freshState();
  s.giga = true;
  s.records = measured.records;
  // mixer has the worst PUBLIC uptime; packer carries a live fault that no
  // model surfaced (its replayed read said " none" - a recorded miss) and
  // has not stopped yet. The Unit must go where the surfaced numbers point.
  s.machines[0].runT = 100; s.machines[0].upT = 60;
  s.machines[1].runT = 100; s.machines[1].upT = 100;
  s.machines[2].runT = 100; s.machines[2].upT = 100;
  const pk = s.machines[2];
  pk.cond = "railed"; pk.stopped = false;
  pk.picoRead = { kind: "assert", said: "none", margin: 3.0 };
  assert.equal(h.unitFocusWith(s), "mixer",
    "worst public uptime wins; the hidden fault has no pull");
  // but once a model RAISES it, the incident leads
  pk.picoRead = { kind: "assert", said: "railed", margin: 2.2 };
  assert.equal(h.unitFocusWith(s), "packer", "a surfaced incident outranks uptime");
  assert.match(js, /never steered by hidden fault state|never where only the hidden fault state/,
    "the discipline is written where the code lives");
});

test("v30: Giga's dial move dispatches the Unit, and the floor tag lands on arrival", () => {
  const h = loadHook();
  const s = h.freshState();
  s.giga = true; s.records = measured.records;
  const m = s.machines[0];
  m.auto = true; m.cond = "stuck"; m.stuckAt = 0.2; m.real = 4.9;
  // AMENDED v31: Giga's steps ride a bounded budget now (see the v28 lock's
  // amendment) - the sim move lands within the second, not on frame one
  for (let i = 0; i < 12 && m.set === 5; i++) { m.real = 4.9; m.stuckAt = 0.2; h.autoWith(s, "mixer", 1 / 12); }
  assert.notEqual(m.set, 5, "the SIM dial move lands within the step budget - Giga's mind is continuous");
  assert.equal(m.unitTagHold, true, "but the floor tag waits for the body");
  assert.equal(s.unit.going, "mixer", "and the Unit is dispatched");
  // travel completes; the tag flushes with the arrival
  for (let i = 0; i < 40; i++) h.stepUnitWith(s, 0.1);
  assert.equal(s.unit.at, "mixer", "the Unit arrived");
  assert.equal(m.unitTagHold, false, "and the held tag flushed on arrival");
});

test("v30: the Unit's ambient lines come from the tally engines, one source", () => {
  const h = loadHook();
  const s = h.freshState();
  s.giga = true; s.records = measured.records;
  s.machines[0].runT = 100; s.machines[0].upT = 60;   // mixer is the bottleneck
  s.unit.topic = 0;
  const line = h.unitLineWith(s);
  assert.match(line, /mixer is holding you back/i,
    "topic zero IS siteBoard().line - the same sentence the wall board prints");
  assert.match(js, /SAME siteBoard\(\)\/plantView\(\)/,
    "the one-source rule is written at the function");
  assert.match(js, /never a number of its own|never mints a number/, "and says why");
});

test("v30: the plant radio asks Ping with the line's summary, prose-framed", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  s.giga = true; s.coins = 214; s.cookies = 61;
  s.contract = { target: 250, level: 2, creep: 1.35, sla: 0.4 };
  const framing = h.pingFramingWith(s, "what should I upgrade next?");
  assert.match(framing, /^On the RogerAI Playbox, the visitor is playing the cookie-line factory game/,
    "prose framing, the mesh lesson - a machine-tagged dump draws a decline");
  assert.match(framing, /% uptime/, "the summary carries real uptime numbers");
  assert.match(framing, /214 coins/, "and the wallet");
  assert.match(framing, /contract 2 at 61\/250/, "and the contract state");
  assert.match(framing, /using only the numbers above/, "and forbids invention");
  // the transport: the one documented outside call, credentials omitted
  assert.match(js, /var PING_URL = "https:\/\/broker\.rogerai\.fm\/concierge"/);
  assert.match(js, /credentials: "omit", cache: "no-store",\n      body: JSON\.stringify\(\{ messages/);
});

test("v30: Ping's reply is labeled Ping - never signed as Giga - and the fallback is local arithmetic", () => {
  // the labels, verbatim where the chat renders
  assert.match(js, /PING \\u00b7 LIVE over the Tower relay/, "live replies wear Ping's name");
  assert.match(js, /Ping is RogerAI's concierge, not a Wave model/, "and the header says what Ping is not");
  assert.ok(!/GIGA \u00b7 LIVE|Giga says/.test(js), "no surface signs the radio's words as Giga's");
  // the fallback is the tally engine, no radio required
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  s.machines[0].runT = 100; s.machines[0].upT = 55;
  const f = h.unitFallbackWith(s);
  assert.match(f, /^the radio's quiet - here's what I can see myself: /);
  assert.match(f, /mixer is holding you back/i, "the fallback body IS siteBoard().line");
});

test("v30: chat rides the tape as a truncated note, and the privacy copy tells the truth", () => {
  const h = loadHook();
  h.tapeReset();
  const longReply = "x".repeat(300);
  h.tapeChat("what should I upgrade next, and also a very long rambling question that overflows".repeat(3), "live", longReply);
  const ev = h.tapeEvents().filter((e) => e.type === "chat")[0];
  assert.ok(ev, "chat lands on the tape");
  assert.ok(ev.q.length <= 80, "the question is truncated");
  assert.equal(ev.note.length, 40, "the reply rides as a 40-char note, never the full text");
  assert.equal(ev.answered, "live");
  h.tapeReset();
});

test("v30: buying Giga spawns the Unit and sells the bundle", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  s.coins = 1000; s.nano = true; s.micro = 0;
  s.unit = null;                       // prove the purchase spawns it fresh
  assert.ok(h.buyDeskWith(s, "giga"));
  assert.ok(s.unit && s.unit.at === "desk", "the Unit spawns at the desk");
  assert.match(s.log[0], /Its Unit is rolling onto the floor/, "the purchase says what arrived");
  assert.match(js, /watch it work, press TALK to ask it anything|its Unit on the floor/i,
    "the desk offer sells the bundle");
});

/* ===================================================================== v31
   The founder reached Giga and it made everything worse: its optimizer
   chased a drifting oven display to the dial's 240-degree stop while its
   own plant view printed "2 sensor(s) currently lying to you". These locks
   are that burn, made unconstructible. */

test("v31: sensor trust is ONE source - the plant view and the policy read the same flag", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  const m = s.machines[0];
  // a model-raised fault word flags the sensor - public knowledge
  m.cond = "stuck";
  m.picoRead = { kind: "caught", said: "stuck", margin: 3, truth: "stuck" };
  assert.equal(h.flaggedWith(s, "mixer"), "a model raised it");
  assert.equal(h.plantWith(s).flagged, 1, "the plant view counts the SAME flag");
  // a recorded miss nobody surfaced flags nothing - the policy is fooled
  // with everyone else, which is the honesty rule
  m.picoRead = { kind: "wrong", said: "none", margin: 3, truth: "stuck" };
  m.inspected = false; m.lastShown = 2.4; m.senseSuspect = false;
  assert.equal(h.flaggedWith(s, "mixer"), null, "a miss nobody caught fools the policy too");
  assert.equal(h.plantWith(s).flagged, 0);
  // INSPECT is public knowledge
  m.inspected = true;
  assert.equal(h.flaggedWith(s, "mixer"), "you inspected it");
  // the one-source rule is structural too: the view calls the function
  assert.match(js, /flagged: G\.machines\.filter\(function \(m\) \{ return !!sensorFlagged\(m\); \}\)/,
    "plantView counts sensorFlagged - unified, not duplicated");
  assert.match(js, /caught lying/, "and the desk card speaks from caught knowledge, not the secret");
});

test("v31: the founder's 240-degree burn is unconstructible - a chased liar gets held, never pinned", () => {
  /* The exact scenario from the screenshot: Giga on autonomy, the oven
     DRIFTING with a recorded double-miss (no model raised it), the display
     sliding below the band. The old policy pinned HEAT at 240 within
     seconds. The doctrine now: bounded steps, and the no-answer deduction
     flags the sensor long before the stop; the hold restores the dial to
     the last trusted position. */
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  s.giga = true;
  const m = s.machines.find((x) => x.id === "oven");
  const c = m.spec.control;
  m.auto = true;
  // settle a trusted position first (healthy, in band)
  m.real = 180; m.cond = "none";
  for (let i = 0; i < 24; i++) h.autoWith(s, "oven", 1 / 12);
  // now the lie: drifting, display sliding down, models silent (double miss)
  m.cond = "drifting";
  m.picoRead = { kind: "wrong", said: "none", margin: 3, truth: "drifting" };
  m.nanoRead = { kind: "missed", said: "none", margin: 2, truth: "drifting" };
  m.driftLie = 0;
  let maxSet = m.set;
  for (let t = 0; t < 90; t += 1 / 12) {
    m.driftLie += (1 / 12) * 1.5;          // the sim's own oven drift-lie rate
    m.real = 180;                           // whatever the dial does, the display keeps lying
    h.autoWith(s, "oven", 1 / 12);
    maxSet = Math.max(maxSet, m.set);
  }
  assert.ok(maxSet < c.max,
    `the dial must never reach its stop chasing a liar (peaked at ${maxSet} of ${c.max})`);
  assert.equal(m.heldForFlag, true, "the no-answer deduction flagged the sensor and the hold engaged");
  assert.equal(m.set, m.lastTrustedSet, "and the dial went back to the last trusted position");
  assert.match(js, /sensor is lying \(/, "the attribution line says why, at the machine");
  assert.match(js, /GIGA_STEPS_PER_SEC/, "and the step budget is named doctrine, not a magic number");
});

test("v31: automation that cannot clear a machine asks for a person, loudly", () => {
  const h = loadHook();
  const s = h.freshState();
  s.records = measured.records;
  s.giga = true; s.unit = { at: "desk", pose: "idle", going: null, dir: 1, pauseLeft: 1, say: "", sayUntil: 0, topic: 0, travelLeft: 0 };
  const m = s.machines.find((x) => x.id === "oven");
  m.auto = true; m.cond = "drifting"; m.stopped = true;
  m.picoRead = { kind: "wrong", said: "none", margin: 3, truth: "drifting" };  // chain miss: nothing raised
  const help = h.helpWith(s);
  assert.ok(help && help.id === "oven", "the unclearable machine is named");
  // a machine with a live alarm and no lockout is NOT a plea - automation can act
  m.picoRead = { kind: "caught", said: "drifting", margin: 3, truth: "drifting" };
  assert.equal(h.helpWith(s), null, "an alarmed machine is automation's job, not the player's");
  // unless the crew is locked out
  m.lockout = 30;
  assert.ok(h.helpWith(s), "a lockout hands it back to the person - INSPECT never locks");
  assert.match(js, /needs your eyes/, "the goal chip says it in person-words");
  assert.match(js, /I can't read the /, "and the Unit says it standing there");
});

test("v31: Giga is never worse than three Micros - the founder's inversion, locked", () => {
  /* "i got to giga and it seemed to have made everything worse. it was
     better when only micro was driving." Two identical plants, same
     deterministic fault schedule - lying-sensor heavy (drifting, stuck,
     railed), the exact kinds that burned the founder - one running three
     Micros, one running Giga. Giga must never come out behind. */
  const h = loadHook();
  const SCHEDULE = [
    [15, "oven", "drifting"], [40, "mixer", "stuck"], [70, "packer", "railed"],
    [100, "oven", "stuck"], [130, "mixer", "drifting"], [160, "packer", "dropout"],
    [190, "oven", "railed"], [220, "mixer", "noisy"], [250, "packer", "drifting"],
  ];
  function run(config) {
    const st = h.freshState();
    st.records = measured.records;
    st.contract.target = 1e9;
    st.nano = true;
    if (config === "giga") st.giga = true; else st.micro = 3;
    st.machines.forEach((m) => { m.pico = true; m.auto = true; });
    let due = SCHEDULE.slice();
    for (let t = 0; t < 300; t += 1 / 12) {
      st.machines.forEach((m) => { m.ambient = 0; m.event = null; m.eventLeft = 999; m.nextFault = 999; });
      while (due.length && t >= due[0][0]) {
        const [, id, kind] = due.shift();
        const m = st.machines.find((x) => x.id === id);
        if (m.cond === "none" && !m.servicing && !m.restarting) h.conditionWith(st, id, kind);
      }
      h.stepWith(st, 1 / 12);
    }
    return st;
  }
  const giga = run("giga");
  const micros = run("micros");
  assert.ok(giga.coins >= micros.coins - 1,
    `one coordinated mind must never lose to three uncoordinated ones: giga ${giga.coins.toFixed(0)} vs micros ${micros.coins.toFixed(0)}`);
});

test("v31: the crown reads the room, and the plea wears amber", () => {
  assert.match(js, /THE PLANT IS STRUGGLING - THE MODELS NEED YOU/,
    "a struggling plant is not crowned as running itself");
  assert.match(js, /upPct < 0\.6/, "conditioned on the run's own uptime");
  assert.match(css, /\.clf-unit__say\.is-help \{ border-color: #C99700/,
    "the plea is amber - urgency without the alarm red");
  assert.match(js, /tape\("hold"/, "a hold goes on the session tape");
});
