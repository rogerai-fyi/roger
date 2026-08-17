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
  assert.match(js, /DOM\.priced = DOM\.priced \|\| \[\]\)\.push\(\{ b: tag, cost: MODEL_PRICE\.pico \}/,
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
  // so within one cycle a second nudge does NOT land; Giga is continuous
  const m = s.machines[0];
  m.auto = true; m.cond = "stuck"; m.stuckAt = 0.2; m.real = 4.9;
  h.autoWith(s, "mixer", 1 / 12);
  const afterFirst = m.set;
  m.real = 4.9; m.stuckAt = 0.2;
  h.autoWith(s, "mixer", 1 / 12);
  assert.equal(m.set, afterFirst, "a Micro mid-cycle does nothing - its next look is seconds away");
  // Giga ignores the per-machine cycle entirely: same mid-cycle spot, fresh state
  const s2 = h.freshState();
  s2.giga = true;
  const m2 = s2.machines[0];
  m2.auto = true; m2.cond = "stuck"; m2.stuckAt = 0.2; m2.real = 4.9;
  h.autoWith(s2, "mixer", 1 / 12);
  const g1 = m2.set;
  m2.real = 4.9; m2.stuckAt = 0.2; m2.cond = "stuck";
  h.autoWith(s2, "mixer", 1 / 12);
  assert.notEqual(m2.set, g1, "Giga is one continuous mind - it keeps correcting without a cycle gap");

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
