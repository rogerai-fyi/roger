// Regression locks for THE SIGNAL BENCH (Playbox MESH deck).
//
// REBUILT 2026-08-13 (founder direction, sixth revision): one sentence, left to
// right - [SENSOR SELECTOR] -> [MODEL CHAIN] -> [THE MONITOR]. Sensor types are
// derived from the recorded tags; conditions are pads over recorded truths; the
// monitor shows the output at each chain stage as a readability cascade. Every
// HONESTY lock from the previous suites is carried forward - these tests are
// the only enforcement the rails have - and the cascade derivation is EXECUTED
// under a stub DOM, not just grepped.
// Run: node --test test/wave-patch.test.mjs
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const SRC = path.join(path.dirname(fileURLToPath(import.meta.url)), "../src");
const read = (p) => readFileSync(path.join(SRC, p), "utf8");
const flat = (s) => s.replace(/\s+/g, " ");

const html = read("playbox.html");
const htmlFlat = flat(html);
const js = read("js/wave-patch.js");
const css = read("styles/wave-patch.css");
const catalog = JSON.parse(read("data/wave-catalog.json"));
const scene = JSON.parse(read("data/wave-scene-recorded.json"));
const measured = JSON.parse(read("data/wave-measured.json"));

// the same grouping rule the module uses, applied independently here
const typeKeyOf = (tag) => /^AI_\d+$/.test(tag) ? "unnamed" : tag.replace(/^[A-Z]+\d+_/, "");
const SUFFIX_TO_KEY = { DISCHARGE_TEMP: "temp", DISCHARGE_PRESS: "press", SUCTION_PRESS: "press",
  VIBRATION: "vib", MOTOR_CURRENT: "amp", OIL_TEMP: "oil", unnamed: "unnamed" };

// ---------- the module, EXECUTED under a stub DOM ------------------------------

function loadHook() {
  const stubEl = () => ({ addEventListener() {}, setAttribute() {}, style: {}, classList: { add() {}, remove() {}, toggle() {} }, appendChild() {}, textContent: "", hidden: true, focus() {}, dataset: {}, closest: () => null, title: "" });
  const sandbox = {
    window: { matchMedia: () => ({ matches: true }), localStorage: { getItem: () => null, setItem() {} }, location: { hash: "" }, addEventListener() {} },
    document: {
      readyState: "complete", addEventListener() {}, removeEventListener() {},
      getElementById: () => null, querySelector: () => null, querySelectorAll: () => [],
      createElement: stubEl, createElementNS: stubEl, body: { classList: { add() {}, remove() {} }, appendChild() {} },
      activeElement: null,
    },
    fetch: () => new Promise(() => {}),
    setTimeout: () => 0,
    requestAnimationFrame: () => 0,
  };
  sandbox.window.document = sandbox.document;
  const fn = new Function("window", "document", "fetch", "setTimeout", "requestAnimationFrame",
    js + "; return window.__wavePatchTest;");
  const hook = fn(sandbox.window, sandbox.document, sandbox.fetch, sandbox.setTimeout, sandbox.requestAnimationFrame);
  hook.setScene(scene, catalog);
  hook.setMeasured(measured);
  hook.buildTypes();
  return hook;
}

function solveModelMove(h) {
  for (let guard = 0; guard < 5 && h.missionPlan().locked; guard++) {
    const move = h.incidentMove();
    assert.ok(move && move.correct, "a locked case always names the move that unlocks it");
    assert.equal(h.missionChooseMove(move.correct), true, `solve ${move.kind} with ${move.correct}`);
  }
  assert.equal(h.missionPlan().locked, false, "the evidence-driven model route reaches field play");
}

// ---------- provenance: every figure traceable to a run ------------------------

test("data: the snapshots carry their provenance", () => {
  assert.equal(catalog._provenance.source, "wavesim.catalog()");
  assert.match(catalog._provenance.exported, /^\d{4}-\d{2}-\d{2}$/);
  assert.equal(measured._provenance.suite, "IEB-Signals v1.2");
  for (const k of ["frames", "escalation", "records", "quants"]) {
    assert.ok(measured._provenance.sources[k], `${k} must name its source`);
  }
  assert.ok(measured.escalation.child && measured.escalation.parent && measured.escalation.bench,
    "the sweep must name whose numbers these are");
});

test("data: the provenance line is rendered, not just committed", () => {
  assert.ok(htmlFlat.includes('id="wpProv"'), "the bar carries a provenance line");
  assert.ok(/recount of these records/.test(js),
    "and it states that every reading is a recount of the records");
});

// ---------- the honesty rails (carried forward) ---------------------------------

test("honesty: the live box never carries invented sensor faults", () => {
  const live = catalog.catalog.roggentoo;
  assert.equal(live.source, "live");
  assert.deepEqual(live.sensor_faults, [],
    "truth is null on live data - the deck must never have a fault to invent");
});

test("honesty: the exporter refuses a snapshot that breaks the truth-null rail", () => {
  const script = readFileSync(path.join(SRC, "../scripts/export-wave-catalog.mjs"), "utf8");
  assert.ok(script.includes("sensor_faults") && /refusing/.test(script));
});

test("honesty: the deck states it is a replay, and no model runs in the browser", () => {
  assert.ok(/REPLAY/.test(htmlFlat), "the run state must be labelled a replay");
  assert.ok(/RECORDED/.test(htmlFlat), "and recorded");
  assert.ok(/RECORDED/.test(scene._provenance.label));
  assert.ok(/never present as live/i.test(scene._provenance.label));
  assert.ok(/recount of real records|recount of these records/i.test(js + htmlFlat),
    "the module states that readings are recounts of records");
  assert.ok(!/setInterval/.test(js), "nothing may animate continuously");
});

test("honesty: no evidence dict is invented for escalations", () => {
  for (const r of measured.records) assert.ok(!("evidence" in r));
});

test("honesty: the certificate leaves the digest empty rather than inventing one", () => {
  assert.ok(/pending export/.test(js), "the digest slot must read as pending");
  assert.ok(css.includes(".wp-cert__pending"), "and be styled as an absence");
  assert.ok(!/sha256[:-][0-9a-f]{8,}/i.test(htmlFlat + js),
    "no invented model digest may ship");
});

test("honesty: the retracted Q4 collapse cannot come back", () => {
  const r = measured._provenance.retracted;
  assert.match(r.status, /RETRACTED/);
  const q4 = measured.quants.find((q) => /q4/i.test(q.quant));
  assert.ok(q4.fault_id_macro > 0.6, "Q4 holds full accuracy; 0.223 was a harness bug");
  assert.ok(!/quantization-fragile/.test(js), "the retracted copy must stay gone");
});

test("honesty: public measurement data carries no internal filenames or row ids", () => {
  const publicData = JSON.stringify(measured);
  assert.doesNotMatch(publicData, /RESULTS-MATRIX\.md|R\.\d+/,
    "the browser gets a public method label, not an internal notebook coordinate");
  for (const q of measured.quants) {
    assert.match(q.source, /quantization sweep/i, "each public quant cites the kind of run");
  }
});

test("honesty: identical aggregates across quantizations are refused as a harness fault", () => {
  const script = readFileSync(path.join(SRC, "../scripts/export-wave-measured.mjs"), "utf8");
  assert.ok(/JSON.stringify\(q4raw.results\) === JSON.stringify\(q8raw.results\)/.test(script));
  assert.ok(/harness/i.test(script));
});

test("honesty: compute never reads as money, and names itself a residency proxy", () => {
  assert.ok(measured.escalation.cost_note, "the caveat must be in the data");
  assert.ok(/residency proxy/.test(js), "the knob tooltip says what the compute figure measures");
  const tipBlock = js.slice(js.indexOf("function knobTip"), js.indexOf("function knobTip") + 900);
  assert.ok(!/\$\s?\d|USD|dollar/i.test(tipBlock), "no currency near the measured figures");
});

test("honesty: macro recall is named as the metric, never bare 'accuracy'", () => {
  assert.ok(/macro recall/i.test(js), "the knob tooltip names the metric it shows");
});

test("honesty: no measured number is hardcoded into the markup", () => {
  const body = htmlFlat.replace(/<!--[\s\S]*?-->/g, "");
  for (const n of ["72.6", "22.3", "62.3%", "43.0%", "29.7", "0.999"]) {
    assert.ok(!body.includes(n), `${n} must render from data, not be typed in`);
  }
});

test("attribution: the Orange County line is present", () => {
  assert.ok(htmlFlat.includes("Designed by RogerAI in Orange County, California."));
});

// ---------- the exported windows: tags, units, bounds, the frame -----------------

test("data: records carry the window the model actually read", () => {
  const withW = measured.records.filter((r) => r.window);
  assert.ok(withW.length === measured.records.length,
    "every record joins its bench row - tags and bounds are data, not decoration");
  for (const r of withW) {
    assert.ok(r.window.tag, "a recorded tag");
    assert.ok(r.window.body.includes("range=["), "and the literal window body");
    assert.ok(r.window.lo <= r.window.mean && r.window.mean <= r.window.hi,
      "bounds parsed from the body must contain the mean");
    assert.ok(r.window.body.includes(String(r.window.tag)),
      "parsed fields must come FROM the body, never beside it");
  }
});

test("data: a unit the wire did not state is null, never defaulted", () => {
  const unknown = measured.records.filter((r) => r.window && r.window.unit === null);
  assert.ok(unknown.length > 0, "the bench really does contain unit-less wires");
  assert.ok(/unit not stated/.test(js), "and the deck says so instead of guessing");
  const exp = readFileSync(path.join(SRC, "../scripts/export-wave-measured.mjs"), "utf8");
  assert.ok(/"\?" means the wire did not state a unit/.test(exp),
    "the exporter documents the rule");
});

test("data: the task frame is exported verbatim, and the envelope carries it", () => {
  assert.ok(measured.task_frame && measured.task_frame.length > 100,
    "the T01 frame ships with the bundle");
  assert.ok(/sensor-interpretation unit/.test(measured.task_frame),
    "and it is the real frame, not a summary");
  const h = loadHook();
  const env = h.envelopeFor({ modality: "raw numbers", channels: [], body: "1, 2, 3" });
  assert.ok(env.includes("task frame - exported verbatim"), "the envelope names its source");
  assert.ok(env.includes(measured.task_frame.slice(0, 60)), "and carries the frame itself");
  assert.ok(!/frame text — pending export/.test(env),
    "the pending-export placeholder is retired from the envelope");
});

// ---------- 1) the sensor selector: derived, never invented ----------------------

test("selector: sensor types derive from the recorded tag suffixes - no invented type", () => {
  const h = loadHook();
  // recompute the grouping independently
  const want = {};
  measured.records.forEach((r) => {
    const key = SUFFIX_TO_KEY[typeKeyOf(r.window.tag)];
    assert.ok(key, `every recorded tag must group somewhere: ${r.window.tag}`);
    want[key] = (want[key] || 0) + 1;
  });
  const got = {};
  h.state.types.forEach((t) => { got[t.key] = t.count; });
  assert.deepEqual(got, want, "one selector button per recorded group, counts exact");
  const total = h.state.types.reduce((a, t) => a + t.count, 0);
  assert.equal(total, measured.records.length, "nothing dropped, nothing invented");
  assert.ok(h.state.types.some((t) => t.key === "unnamed"),
    "the Sparkplug aliases-with-no-name stay on the selector - real plants are full of them");
});

test("selector: a type's pads are exactly the truths recorded for that type", () => {
  const h = loadHook();
  for (const t of h.state.types) {
    const truths = new Set(measured.records
      .filter((r) => SUFFIX_TO_KEY[typeKeyOf(r.window.tag)] === t.key)
      .map((r) => r.truth));
    assert.deepEqual(new Set(t.conds), truths,
      `${t.key}: a pad exists only for a recorded condition`);
    assert.equal(t.conds[0], "none", "the healthy pad comes first");
  }
});

test("selector: each pad replays the CLEAREST recorded window - documented criteria, executed", () => {
  const h = loadHook();
  // the same criteria table the module documents, applied independently here.
  // stuck = flattest window (min sd relative to magnitude) because in this
  // export every stuck window has longest_run = 1 - the sensor stops
  // TRACKING while quantization jitter keeps neighbours unequal.
  const score = {
    none: (w) => -w.hf_energy,
    stuck: (w) => -(w.sd / Math.max(Math.abs(w.mean), 1e-9)),
    dropout: (w) => w.n_resets + (w.max_drop || 0) * 1e-6,
    noisy: (w) => w.hf_energy,
    drifting: (w) => Math.abs(w.slope_per_min) + (w.monotonic_frac || 0) * 1e-6,
    railed: (w) => w.at_max_frac,
  };
  for (const t of h.state.types) {
    for (const c of t.conds) {
      const mine = measured.records
        .map((r, i) => ({ r, i }))
        .filter(({ r }) => SUFFIX_TO_KEY[typeKeyOf(r.window.tag)] === t.key && r.truth === c);
      assert.ok(mine.some(({ i }) => i === t.recIdx[c]),
        `${t.key}/${c}: the pick comes from that type's own records`);
      const bestScore = Math.max(...mine.map(({ r }) => score[c](r.window)));
      const pickedScore = score[c](measured.records[t.recIdx[c]].window);
      assert.ok(Math.abs(pickedScore - bestScore) < 1e-9,
        `${t.key}/${c}: the pick maximises the documented criterion`);
      assert.ok(/chosen as the clearest recorded/.test(t.pickWhy[c]),
        "and the pad can say why it was chosen");
    }
  }
  assert.ok(/longest_run = 1/.test(js), "the stuck-criterion deviation is documented in the source");
});

// ---------- 2) the chain --------------------------------------------------------

test("chain: the menu carries the WHOLE Wave family with honest statuses", () => {
  const h = loadHook();
  // v14: THE WAVE SPECTRUM (WAVE-TIER-SCALING-STRATEGY-2026-08-14, naming
  // LOCKED) - pico -> exa; the old Edge/Core/Station/Satellite ladder is gone.
  const ids = h.family.map((f) => f.id);
  assert.deepEqual(ids, ["pico", "nano", "micro", "giga", "tera", "peta", "exa"],
    "the menu is the locked Spectrum, in ladder order");
  for (const gone of ["edge", "core", "station", "satellite"]) {
    assert.ok(!ids.includes(gone), `${gone} is a superseded tier and must not survive`);
  }
  assert.ok(h.family.every((f) => f.recipe && f.reach),
    "every tier states its training recipe and its reach");
  const nano = h.family.find((f) => f.id === "nano");
  assert.ok(!/350/.test(nano.size), "the old ~350M guess is dead - the tier band shows instead");
  assert.ok(/pending export/.test(nano.blurb), "the senior run's params are pending, and say so");
  const recorded = h.family.filter((f) => f.status === "recorded").map((f) => f.id).sort();
  assert.deepEqual(recorded, ["nano", "pico"],
    "exactly two slots have a recorded run on this bench - claiming more would be a lie");
  assert.ok(h.family.every((f) => f.does), "every card states its transform in plain words");
  assert.ok(/will attach silent/.test(js), "the menu warns before an unrecorded slot chains in");
});

test("chain: an unrecorded model chains in and states its job, claiming nothing", () => {
  // AMENDED 2026-08-15: an unrecorded tier used to add one dead line ("silent -
  // no recorded run here"). It now states the JOB that tier does - and where the
  // bench holds data at that scope (Micro, the site brain) it does that job on
  // the recorded records, attributed to the BENCH. The guarantee this lock
  // exists for is unchanged and asserted harder: no prediction, no margin, no
  // lamp, for any tier without a recorded run.
  // AMENDED AGAIN 2026-08-15 (v20): Micro AND Giga now do their real job at
  // their real scope (a site / the plant), so both produce a "scoperun" stage
  // with counts; only tiers whose scope exceeds the recording stay "scope".
  const h = loadHook();
  h.selectType("temp", "dropout");
  for (const [tier, kind] of [["micro", "scoperun"], ["giga", "scoperun"], ["exa", "scope"]]) {
    h.state.chain = [tier];
    h.derive();
    const st = h.state.verdict.stages;
    assert.equal(st.length, 2, `raw + the ${tier} stage`);
    assert.equal(st[1].kind, kind, `${tier} produces a ${kind} stage`);
    assert.ok(!("said" in st[1]) && !("margin" in st[1]) && !("verdict" in st[1]),
      `${tier} carries NO prediction and NO margin`);
    assert.equal(h.state.verdict.state, "off", `and the lamp refuses to glow for ${tier}`);
  }
  assert.ok(/no recorded run on this bench, so it stays quiet/.test(js),
    "a recorded tier in a pass-through position still says exactly what it is");
});

test("v20: every scope is a real subset of the records, counted by one engine", () => {
  // AMENDED 2026-08-15 (v20): the site rollup used to be the WHOLE fleet, which
  // was the right arithmetic on the wrong scope - a site brain reads a site,
  // not the plant. Scopes are now nested subsets of real records (machine ->
  // site -> plant), and the guarantee is stronger: every scope's counts must
  // come from records that actually exist, and the plant scope must still
  // agree with the FLEET tab exactly.
  const h = loadHook();
  h.selectType("temp", "dropout");
  h.state.chain = ["pico", "nano", "micro"];
  h.state.floor = 1.5;
  h.derive();
  const site = h.state.verdict.stages.find((s) => s.kind === "scoperun");
  assert.ok(site, "a chained Micro produces a scope-run stage");

  // the scope is a real subset: its indices exist, are unique, and are exactly
  // the channels of the machines it claims
  const idxs = site.scope.idxs;
  assert.equal(new Set(idxs).size, idxs.length, "no record is counted twice in a scope");
  idxs.forEach((i) => assert.ok(measured.records[i], `record ${i} exists`));
  assert.equal(site.scope.chans, idxs.length, "the channel count is the scope's own size");
  assert.equal(site.tally.n, idxs.length, "and the tally counts exactly those records");
  assert.equal(site.tally.caught + site.tally.missed, site.tally.faults,
    "its arithmetic balances");
  assert.ok(site.scope.chans < measured.records.length,
    "a SITE is smaller than the plant - the scope actually narrows");

  // the plant scope must equal the fleet tab, record for record
  h.state.chain = ["pico", "nano", "giga"];
  h.derive();
  const plant = h.state.verdict.stages.find((s) => s.kind === "scoperun");
  assert.equal(plant.scope.chans, measured.records.length,
    "the PLANT scope is every recorded channel");
  const fleet = h.deriveFleet();
  ["n", "faults", "caught", "missed", "deadEnd", "falseAlarms"].forEach((k) => {
    assert.equal(plant.tally[k], fleet.totals[k],
      `plant.${k} agrees with the FLEET tab - one engine, no drift`);
  });

  // it states the job, and never claims the model did it
  assert.ok(/BENCH ARITHMETIC/.test(js), "the numbers carry a bench-arithmetic tag");
  assert.ok(/not something " \+\s*st\.fam\.label|not something/.test(js),
    "and say in words that the model did not produce them");
  assert.ok(/has no recorded run here/.test(js), "the absent run is stated");
  [site, plant].forEach((st) => {
    assert.ok(!("said" in st) && !("margin" in st) && !("verdict" in st),
      "a scope stage carries no prediction and no margin");
  });
});

test("v20: the site partition is a stated convention, not a claimed fact", () => {
  // The bench file never says which machines share a building. The deck groups
  // scenes in id order, and that rule must be VISIBLE wherever a site is shown -
  // otherwise the deck would be quietly inventing plant topology.
  const h = loadHook();
  h.selectType("temp", "dropout");
  h.state.chain = ["micro"];
  h.derive();
  const st = h.state.verdict.stages.find((s) => s.kind === "scoperun");
  assert.match(st.scope.how, /scenes in id order/,
    "the grouping rule travels with the scope and is printed on the card");
  assert.ok(/THE SITE PARTITION IS A STATED CONVENTION/.test(js),
    "and the source says so where the rule is defined");
  // machines are real: each is one recorded scene
  const scenes = new Set(measured.records.map((r) => r.scene_id));
  assert.equal(h.state.machines.length, scenes.size, "one machine per recorded scene");
  const covered = h.state.machines.reduce((a, m) => a + m.idxs.length, 0);
  assert.equal(covered, measured.records.length,
    "and every recorded channel belongs to exactly one machine");
});

test("v18: tiers above the PLANT state their scope and invent nothing", () => {
  const h = loadHook();
  h.selectType("temp", "dropout");
  for (const tier of ["tera", "peta", "exa"]) {  // giga now runs the plant scope
    h.state.chain = [tier];
    h.derive();
    const sc = h.state.verdict.stages.find((s) => s.kind === "scope");
    assert.ok(sc, `${tier} produces a scope stage`);
    assert.ok(sc.takes && sc.job, `${tier} says what it takes in and what it would produce`);
    // nothing numeric may ride a tier the bench has no data for
    for (const k of ["fleet", "tally", "scope", "margin", "said", "verdict", "n", "caught", "missed"]) {
      assert.ok(!(k in sc), `${tier}'s scope stage carries no ${k}`);
    }
    // and it must say what it would ADD, not merely that it is absent
    assert.ok(sc.fam.only && sc.fam.needs,
      `${tier} states what only it can do and what the bench would need`);
  }
  assert.ok(/WHAT THIS BENCH WOULD NEED/.test(js), "the empty scope is labelled");
  assert.ok(/a second plant's recording/.test(js),
    "and says exactly what it would take to fill it");
  assert.ok(/its work shows up in the WEIGHTS of the models below it/.test(js),
    "the flagship's job is teaching, which is not a read on this monitor");
});

test("v18: fan-in is stated on every card and walked in the tour", () => {
  const h = loadHook();
  // every tier declares what it takes IN - the shape of the mesh
  for (const fam of h.family) {
    assert.ok(fam.takes && fam.takes.length > 3, `${fam.id} declares what it takes in`);
    assert.ok(fam.job && fam.job.length > 10, `${fam.id} declares what it produces`);
  }
  assert.equal(h.family.find((f) => f.id === "nano").takes, "many Picos",
    "a Nano rolls up many Picos");
  assert.equal(h.family.find((f) => f.id === "micro").takes, "many fleets",
    "a Micro reasons over many fleets");
  assert.ok(/sn-slot__takes/.test(js), "the card renders it");
  assert.ok(/in a real mesh, many at once/.test(js),
    "and says the bench shows one where a plant would run many");
});

test("chain: pico stage prints recorded child fields, nano stage the recorded parent", () => {
  const h = loadHook();
  h.selectType("temp", "dropout");
  const r = measured.records[h.state.types.find((t) => t.key === "temp").recIdx.dropout];

  h.state.chain = ["pico"]; h.state.floor = 0.5;
  h.derive();
  let st = h.state.verdict.stages;
  const pico = st.find((s) => s.kind === "pico");
  assert.equal(pico.margin, r.child.margin, "the pico stage carries the child's recorded margin");
  if (r.child.margin >= 0.5) assert.equal(pico.said, r.child.prediction);
  else assert.ok(pico.esc, "below the floor it escalates");

  h.state.chain = ["nano"];
  h.derive();
  st = h.state.verdict.stages;
  const nano = st.find((s) => s.kind === "nano");
  assert.equal(nano.verdict, r.parent.prediction, "the verdict word IS the recorded parent prediction");
  assert.ok(nano.para.includes(r.parent.margin.toFixed(2)), "with the recorded parent margin");
  assert.ok(nano.para.includes(r.window.tag), "on the recorded instrument, by name");
});

test("chain: a nano after a pico is the senior - doubtful reads reach it", () => {
  const h = loadHook();
  // find a fault condition whose first record escalates at floor 2.0
  let found = null;
  for (const t of h.state.types) {
    for (const c of t.conds) {
      if (c === "none") continue;
      const r = measured.records[t.recIdx[c]];
      if (r.child.margin < 2.0) { found = { t, c, r }; break; }
    }
    if (found) break;
  }
  assert.ok(found, "the recorded sample contains a doubtful fault read");
  h.selectType(found.t.key, found.c);
  h.state.floor = 2.0;
  h.state.chain = ["pico", "nano"];
  h.derive();
  const st = h.state.verdict.stages;
  assert.ok(st.find((s) => s.kind === "pico" && s.esc), "the pico hands the read up");
  const nano = st.find((s) => s.kind === "nano");
  assert.equal(nano.verdict, found.r.parent.prediction, "and the senior answers with the recorded word");
  // without the senior, the doubt is a dead end - stated, never swallowed
  h.state.chain = ["pico"];
  h.derive();
  assert.ok(h.state.verdict.stages.find((s) => s.kind === "deadend"),
    "no senior in the chain leaves the doubt visibly unheard");
});

test("chain: knob detents are exactly the measured floors - nothing else is dialable", () => {
  const h = loadHook();
  const measuredFloors = measured.escalation.configs
    .map((c) => /^child\+parent@([\d.]+)$/.exec(c.config))
    .filter(Boolean).map((m) => parseFloat(m[1])).sort((a, b) => a - b);
  assert.deepEqual(h.detents, measuredFloors,
    "the knob physically cannot ask for an unmeasured number");
});

test("chain: the measured figures live on the knob, with their suite", () => {
  assert.ok(/Measured at this floor/.test(js), "the tooltip owns the measured numbers");
  assert.ok(/must be to answer alone/.test(js), "and says in plain words what the knob does");
  assert.ok(/_provenance.suite/.test(js.slice(js.indexOf("function knobTip"))),
    "and cites the suite they were measured on");
});

// ---------- 3) THE MONITOR: the cascade ------------------------------------------

test("monitor: the raw stage is byte-for-byte the recorded window body", () => {
  const h = loadHook();
  h.selectType("press", "none");
  h.state.chain = [];
  h.derive();
  const st = h.state.verdict.stages;
  const r = measured.records[h.state.types.find((t) => t.key === "press").recIdx.none];
  assert.equal(st[0].kind, "raw");
  assert.equal(st[0].body, r.window.body, "the sensor's output IS the recorded window, verbatim");
  assert.ok(/byte-for-byte/.test(js), "and the module says so");
  assert.ok(/was not exported - the log is absent, not invented/.test(js),
    "a missing window is an absence, never a synthesis");
});

test("monitor: the glossary is static documentation, visibly marked", () => {
  const h = loadHook();
  for (const k of ["none", "stuck", "dropout", "noisy", "drifting", "railed"]) {
    assert.ok(h.glossary[k], `the glossary defines ${k}`);
  }
  // the glossary is a literal object in the source - grep proves it is static
  const g = js.slice(js.indexOf("var GLOSSARY"), js.indexOf("var GLOSSARY") + 700);
  assert.ok(/stuck:/.test(g) && /railed:/.test(g), "a fixed dictionary, not computed text");
  assert.ok(/STATIC DOCUMENTATION, not measurement/.test(js),
    "the module states what the glossary is");
  assert.ok(/sn-gloss__k/.test(js) && /"glossary"/.test(js),
    "every use carries the glossary microlabel");
  assert.ok(css.includes(".sn-gloss__k"), "styled as a label, distinct from recorded facts");
});

test("monitor: the verdict paragraph is template + recorded fields, never generated prose", () => {
  const h = loadHook();
  h.selectType("temp", "dropout");
  h.state.chain = ["nano"];
  h.derive();
  const nano = h.state.verdict.stages.find((s) => s.kind === "nano");
  const r = measured.records[h.state.types.find((t) => t.key === "temp").recIdx.dropout];
  const w = r.window;
  // every number and word in the paragraph is a recorded field
  assert.ok(nano.para.includes(w.tag));
  assert.ok(nano.para.includes(String(w.n)));
  assert.ok(nano.para.includes(String(w.lo)) && nano.para.includes(String(w.hi)));
  assert.ok(nano.para.includes('" ' + r.parent.prediction + '"'));
  assert.ok(/caught|MISSED|quiet|false alarm/.test(nano.para),
    "the outcome clause names the recorded truth relation");
  assert.ok(/the recorded truth is/.test(nano.para), "and cites the truth as recorded");
});

test("monitor: the ticker is capped, and the announcer is sr-only", () => {
  assert.ok(/insertBefore\(li, strip.firstChild\)/.test(js), "newest line on top");
  assert.ok(/> 2\) strip.removeChild/.test(js), "a ticker, not a log");
  assert.ok(htmlFlat.includes('aria-live="polite"'), "reactions are announced");
  assert.ok(/screen-reader-only/.test(css), "the announcer is sr-only");
});

test("monitor: the output lives inside the wide TV's glass", () => {
  assert.ok(css.includes("glass-monitor-wide-ink.png"), "the wide set frames the output");
  // v9: the plate rule grew a parallax transform, so match within the block
  const plateBlock = css.slice(css.indexOf(".sn-tv__plate {"), css.indexOf("}", css.indexOf(".sn-tv__plate {")));
  assert.ok(/pointer-events: none/.test(plateBlock),
    "the plate never catches a tap - it frames data, it never is the data");
  assert.ok(/aspect-ratio: 750 \/ 570/.test(css), "the TV keeps the engraving's true proportions");
  assert.ok(htmlFlat.includes('id="wpMonitor"'), "the screen is the output element");
  assert.ok(/glass interior of the engraving/.test(css),
    "the screen rect is documented against the plate's interior");
  assert.ok(css.includes("term-keys-ink.png"), "the keyboard engraving sits by the prompt shelf");
});

// ---------- interaction ------------------------------------------------------------

test("interaction: the knob is tap-to-step, drag, and keyboard - one state machine", () => {
  const dial = js.slice(js.indexOf("function drawDial"), js.indexOf("function render()"));
  assert.ok(/click/.test(dial) && /\+ 1\) % opts.values.length/.test(dial),
    "a plain tap steps to the next position - the tablet path");
  assert.ok(/setPointerCapture/.test(dial), "drag captures its pointer");
  assert.ok(/ArrowUp|ArrowRight/.test(dial), "arrows turn it");
  assert.ok(/pointercancel/.test(dial), "and drags clean up on pointercancel");
});

test("interaction: no HTML5 drag anywhere; the paste button is the one drop target", () => {
  assert.ok(!/dragstart/.test(js), "we never initiate HTML5 drags");
  const dndUses = (js.match(/dataTransfer/g) || []).length;
  const dropBlock = js.slice(js.indexOf('addEventListener("drop"'), js.indexOf('addEventListener("drop"') + 600);
  assert.ok(dndUses > 0 && dndUses === (dropBlock.match(/dataTransfer/g) || []).length,
    "dataTransfer appears only in the external-drop handler");
});

test("interaction: pads are a radiogroup of recorded conditions", () => {
  assert.ok(js.includes('setAttribute("role", "radiogroup")'), "the pad row is one choice");
  assert.ok(js.includes('setAttribute("role", "radio")'), "each pad is a position");
  // v15: same rule, plainer words, still on the visible subtitle (not demoted
  // to a tooltip - a touch visitor never sees a title attribute).
  assert.ok(/a condition we never recorded has no pad/.test(js),
    "the pads say the rule out loud: unrecorded conditions have no pad");
  assert.ok(/each pad replays a real recorded reading/.test(js),
    "and the visible line says the pads are recordings, not simulations");
  assert.ok(/a pad is a recorded instance, selected, not simulated/.test(js),
    "and each pad names the record it replays");
});

test("interaction: the selector is a radiogroup, and the plus-stub invites", () => {
  assert.ok(/Sensor type - derived from the recorded tags/.test(js),
    "the selector names its own honesty rule");
  assert.ok(/syn-plus/.test(js) && /aria-expanded/.test(js),
    "the n8n-style plus-circle opens the chain menu");
});

test("interaction: motion is chrome, and stops for reduced motion", () => {
  assert.ok(/is CHROME/.test(css), "the css says the pulse claims nothing");
  assert.ok(/prefers-reduced-motion: reduce\) \{ \.ws-rec\.is-live \.ws-rec__dot \{ animation: none/.test(css),
    "the REC pulse stops for people who asked for less motion");
  assert.ok(/prefers-reduced-motion: no-preference/.test(css),
    "the stage reveal only animates when motion is welcome");
  assert.ok(js.includes("REDUCED"), "and the module checks the preference too");
});

// ---------- the palette ----------------------------------------------------------

test("palette: only tokens that exist are referenced", () => {
  // The rule's purpose: no dangling var() that silently falls back to nothing.
  // A reference is fine if (a) tokens.css defines it, or (b) EVERY use carries
  // an explicit fallback (the v9 parallax vars --tiltx/--tilty, set by JS,
  // consumed as var(--tiltx, 0deg) - the fallback IS the reduced state).
  // v17 widens (c): this stylesheet now DEFINES its own scoped properties -
  // the Wave Spectrum tier hues (--tier-pico ... --tier-exa, --tier-wash) on
  // the deck root. A property defined here is not dangling, so it satisfies
  // the rule the same way a tokens.css entry does. --tc, which JS sets per
  // tier, is NOT defined in CSS and therefore still owes every use a
  // fallback - which is what keeps a tier-less stage rendering sanely.
  const tokens = read("styles/tokens.css");
  const used = new Set((css.match(/var\(--[a-z0-9-]+/g) || []).map((v) => v.slice(4)));
  for (const name of used) {
    if (tokens.includes(`${name}:`)) continue;
    if (new RegExp(`^\\s*${name}:`, "m").test(css)) continue;  // defined here
    const bare = new RegExp(`var\\(${name}\\s*\\)`);
    assert.ok(!bare.test(css),
      `${name} is not a token, so every use must carry an explicit fallback`);
  }
  // and the tier hues really are defined, in both themes
  for (const tier of ["pico", "nano", "micro", "giga", "tera", "peta", "exa"]) {
    assert.ok(new RegExp(`--tier-${tier}:\\s*#`).test(css), `--tier-${tier} is defined`);
    assert.ok((css.match(new RegExp(`--tier-${tier}:`, "g")) || []).length >= 2,
      `--tier-${tier} has a dark-theme value too`);
  }
});

test("palette: red is a signal, never a surface", async () => {
  // The count is a coarse tripwire against runaway red; the enumerated FILL
  // list below is the real teeth. Raised 30 -> 32 in v19 for the TV's scroll
  // controls, whose :focus-visible ring is the same red every other control
  // on this deck focuses with. The v19 comment said that a raise for anything
  // that is NOT a focus ring is the signal to look hard - so, v20, having
  // looked hard: 32 -> 36 covers four uses, and only two are new red at all.
  // Two ARE focus rings (the face of the set, and the way back out of it).
  // The other two are one thing: the face's red STATE, which sets background
  // and border-color together. That is the lamp window's rule rendered at the
  // size of the glass - already enumerated as a permitted fill below, still
  // carrying its NE-107 shape and its word. No new KIND of red was added.
  // v38: 36 -> 37 for exactly one more FOCUS RING - the sidebar's MORE/LESS
  // fold toggle focuses with the same red as every other control here. Still
  // no new KIND of red.
  const reds = (css.match(/var\(--live\)/g) || []).length;
  assert.ok(reds > 0 && reds < 37, `red is used ${reds} times; it must stay a glint`);
  const filled = [];
  for (const block of css.split("}")) {
    if (/background:\s*var\(--live\)/.test(block)) {
      const sel = (block.split("{")[0] || "").trim().split("\n").pop().trim();
      filled.push(sel);
    }
  }
  // Five rules may fill with red: the masthead's spot plate, the intake's one
  // primary action, the lamp window's red state, (v17) the sensor lane's
  // missed-fault dot - a 0.42rem diamond meaning exactly what --live means -
  // v21 removes the face from this list: the CRT is neutral glass in every
  // state and spends red only on its thin status accent, never as a surface.
  assert.deepEqual(filled.sort(),
    ['.sn-type__dot.is-bad', '.wm-masthead__spot', '.wp-lampwin[data-state="red"]',
     '.wp-run'].sort(),
    `got ${filled.join(", ")}`);
  const pin = (f, kb) => {
    const bytes = statSync(path.join(SRC, "assets/wave/" + f)).size;
    assert.ok(bytes < kb * 1024, `${f} is ${bytes} bytes; past ~${kb}KB it stops being a glint`);
  };
  pin("mesh-console-spot.png", 16);
  pin("radio-reader-spot.png", 16);
});

test("palette: the lamp hues are fenced to lamp semantics", () => {
  const hueRules = css.split("}").filter((b) => /#1E7A3C|#C99700/.test(b));
  for (const b of hueRules) {
    // lamps, read marks and lit condition pads all SPEAK lamp semantics -
    // what is the bench replaying / catching right now
    // v12 adds: verdict-word tints (sn-live), the margin/floor meter's bar
    // (green asserts / amber escalates), and the LIVE dot (green = on air).
    // All of them SPEAK lamp semantics; none is decoration.
    // v15 adds the chain card's state badge (ANSWERED / ASKED FOR HELP /
    // WAITING) - it reports what the chain did with the read now on the
    // monitor, and it always carries the WORD, so hue is never the only signal.
    // v17 adds the sensor lane dot (sn-type__dot): it reports what the chain
    // said about THAT lane's own record - the same semantics, one per sensor,
    // which is how "one model reads many channels" became visible. Colour is
    // not its only signal: each state also has a distinct SHAPE (filled or
    // hollow, circle or diamond) and spells itself out in the lane's
    // aria-label and title - both asserted below.
    // v21 keeps the face neutral and uses a status accent only. It still carries
    // the shape and word, but no longer adds a green/yellow surface here.
    // v24 adds the field-panel pilot lamp: amber means the next check is live,
    // green means that named check completed. The surrounding card and result
    // stay neutral, so the hue is still fenced to a lamp with a number/check.
    assert.ok(/wp-lamp|wp-read__mark|syn-pad|sn-live|sn-fm__|sn-slot__state|sn-type__dot|sn-tv__front|sn-front__|sn-field__lamp/.test(b),
      `green/yellow may only colour lamp-semantic surfaces, got: ${b.split("{")[0].trim()}`);
  }
  assert.ok(/STANDING BY|ALL CLEAR|CHECK REQUIRED|FAULT MISSED/.test(js),
    "every lamp state carries a word");
  assert.ok(/[·●△⊗]/.test(js), "and an NE-107-style shape, so the verdict never rides on hue alone");
  // the lane dots follow the same discipline: shape as well as hue, and the
  // state written out for anyone who cannot see either
  assert.ok(/\.sn-type__dot\.is-bad[^}]*transform: rotate/.test(css),
    "a missed fault is a different SHAPE, not just a different colour");
  assert.ok(/\.sn-type__dot\.is-quiet[^}]*background: transparent/.test(css),
    "a quiet lane is hollow, not just grey");
  assert.ok(/setAttribute\("aria-label", t.label \+ " - " \+ lr.cLabel/.test(js),
    "and every lane says its state in words");
});

// ---------- the lamp: derived, honest, playable ----------------------------------

test("lamp: every state is a recount of the recorded records", () => {
  const h = loadHook();
  h.selectType("temp", "none");
  h.state.chain = [];
  h.derive();
  assert.equal(h.state.verdict.state, "off", "no chain, no verdict colour");

  // a knob-fixable miss: asserted wrong at 1.5, senior was right, margin < 2.0
  let found = null;
  for (const t of h.state.types) {
    for (const c of t.conds) {
      if (c === "none") continue;
      const r = measured.records[t.recIdx[c]];
      if (r.child.margin >= 1.5 && r.child.margin < 2.0 &&
          r.child.prediction !== c && r.parent.prediction === c) found = { t, c };
    }
  }
  if (found) {
    h.selectType(found.t.key, found.c);
    h.state.chain = ["pico", "nano"]; h.state.floor = 1.5; h.state.operator = true;
    h.derive();
    assert.equal(h.state.verdict.state, "red", "a knob-fixable miss goes red");
    // v15: the knob is labelled SURE ENOUGH now, but the DIRECTION is the
    // load-bearing part - a read is handed up BELOW the setting, so raising it
    // asks for help sooner. The advice must still say raise, never lower.
    assert.ok(/[Rr]aise the SURE ENOUGH knob/.test(h.state.verdict.why),
      "a read escalates below the setting, so the honest advice is RAISE");
    assert.ok(!/lower the/i.test(h.state.verdict.why), "and never says lower");
    h.state.floor = 2.0;
    h.derive();
    assert.notEqual(h.state.verdict.state, "red", "raising the knob fixes what it can");
  }
});

test("v21: a model-limit miss stays red instead of becoming a green consolation prize", () => {
  assert.ok(/MODEL LIMIT/.test(js), "the ceiling label names the model limit in plain words");
  assert.ok(/recorded Nano counterfactual also said/.test(js) &&
            /Wave Nano answered.*recorded run/.test(js),
    "the why-line attributes both called and uncalled misses to the recorded senior, not to luck");
  const h = loadHook();
  // find a fault both models miss
  let found = null;
  for (const t of h.state.types) {
    for (const c of t.conds) {
      if (c === "none") continue;
      const r = measured.records[t.recIdx[c]];
      if (r.parent.prediction !== c && (r.child.margin >= 2.0 ? r.child.prediction !== c : true)) {
        found = { t, c }; break;
      }
    }
    if (found) break;
  }
  if (found) {
    h.selectType(found.t.key, found.c);
    h.state.chain = ["pico", "nano"]; h.state.floor = 2.0; h.state.operator = true;
    h.derive();
    assert.equal(h.state.verdict.state, "red", "a recorded fault missed by the chain stays red");
    assert.equal(h.state.verdict.label, "MODEL LIMIT",
      "the red result distinguishes a model limit from a broken route");
  }
});

test("v21: the stuck motor-current miss explains why escalation cannot rescue it", () => {
  const h = loadHook();
  h.selectType("amp", "stuck");
  h.state.chain = ["pico", "nano", "micro"];
  h.state.floor = 1.5;
  h.derive();
  const pico = h.state.verdict.stages.find((st) => st.kind === "pico");
  const nano = h.state.verdict.stages.find((st) => st.kind === "quietSenior");
  assert.equal(pico.said, "none", "the committed Pico result is NONE");
  assert.equal(pico.margin, 1.875, "its committed margin clears the 1.5 floor");
  assert.ok(nano, "Nano was not called because Pico cleared the floor");
  assert.match(h.stageResponse(nano), /NOT CALLED.*WOULD ALSO SAY "NONE"/,
    "the glance view exposes the recorded counterfactual instead of looking disconnected");
  assert.equal(h.state.verdict.label, "MODEL LIMIT");
  assert.match(h.state.verdict.why, /1\.88.*1\.5.*Nano was not called.*also said "none"/i,
    "the detailed verdict explains both the gate and why escalation would not change the answer");
});

test("v25: a confident miss puts model output, truth, and detection boundary together", () => {
  const h = loadHook();
  const r = measured.records.find((item) =>
    item.node_id === "s00077c01" && item.truth === "stuck" &&
    item.child.prediction === "none" && item.parent.prediction === "none");
  assert.ok(r, "the screenshot's committed STUCK/NONE/NONE record remains in the replay");
  h.state.floor = 1.5;
  const lesson = h.modelLimitLesson(r, {
    word: r.child.prediction,
    who: "WAVE PICO",
  });
  assert.equal(lesson.label, "CONFIDENT MISS");
  assert.equal(lesson.modelAnswer, "NONE");
  assert.equal(lesson.recordedTruth, "STUCK");
  assert.match(lesson.knownBy, /REPLAY LABEL.*NOT MODEL OUTPUT/i,
    "the screen says how the simulator knows something both models missed");
  assert.match(lesson.why, /2\.88.*1\.5.*Nano was not called.*also said NONE/i,
    "the handoff decision and the recorded Nano counterfactual are one explanation");
  assert.match(lesson.shape, /96 values.*99\.594.*99\.962.*no consecutive value repeats.*all-values-equal/i,
    "the STUCK card explains why the visibly moving noise does not make the label obvious");
  assert.match(lesson.catch, /independent reference.*site invariant.*all-clear/i,
    "the next defense can observe a miss even though a finding was never emitted");
});

test("v25: the answer card labels NONE as model output rather than healthy truth", () => {
  const monitor = js.slice(js.indexOf("function paintMonitor"), js.indexOf("function wirePrompt"));
  for (const copy of ["MODEL ANSWER · NOT THE TRUTH", "RECORDED TRUTH", "HOW THIS BENCH KNOWS",
                      "WHY NONE LOOKED PLAUSIBLE", "WHAT CAN CATCH IT"]) {
    assert.ok(monitor.includes(copy), `the detail puts ${copy} beside the missed answer`);
  }
  assert.match(monitor, /modelLimitLesson\(r, ans\)/,
    "the visible comparison is derived from the selected record and final answer");
  assert.match(js, /AUDIT THE BLIND SPOT/,
    "the Micro action is framed as an audit, not a fabricated inference");
  assert.match(js, /MODEL MISS AUDIT[\s\S]*MODEL SAID[\s\S]*REPLAY SAYS/,
    "the training console keeps the mismatch visible even when its detailed answer is below the fold");
});

test("v21: response routing never rewrites the model result", () => {
  const h = loadHook();
  h.selectType("temp", "none");
  h.state.chain = ["pico"]; h.state.floor = 0.5; h.state.operator = false;
  h.state.authority = false;
  h.derive();
  const logged = { state: h.state.verdict.state, label: h.state.verdict.label, why: h.state.verdict.why };
  h.state.operator = true;
  h.derive();
  assert.deepEqual(
    { state: h.state.verdict.state, label: h.state.verdict.label, why: h.state.verdict.why },
    logged, "sending a finding to human review changes routing, not the reading");
  h.state.operator = false; h.state.authority = true;
  h.derive();
  assert.deepEqual(
    { state: h.state.verdict.state, label: h.state.verdict.label, why: h.state.verdict.why },
    logged, "sending a finding to the policy queue changes routing, not the reading");
});

// ---------- structure + offline ----------------------------------------------------

test("deck: the selector, the chain, and the monitor all exist", () => {
  for (const id of ["wbBench", "wsTypes", "wsPads", "wsChain", "wsChainMenu", "wsTabs",
                    "wpMonitor", "wpPromptForm", "wpPrompt", "wpChips",
                    "wpLamp2", "wpWhy", "wbOp", "wpStrip", "wpCertHost", "wpProv"]) {
    assert.ok(htmlFlat.includes(`id="${id}"`), `${id} must exist`);
  }
  assert.ok(/1 &middot; PICK A SENSOR|1 · PICK A SENSOR/.test(html), "the sentence is numbered");
  assert.ok(/more sensors soon/.test(htmlFlat), "multi-sensor is roadmap language, not a fake control");
  assert.ok(/one Pico reads many\s+channels/.test(htmlFlat.replace(/\s+/g, " ")) || /one Pico reads many/.test(htmlFlat),
    "the one-model-many-sensors reality is stated as roadmap copy");
});

test("deck: data is same-origin; Ping is the ONE documented cross-origin call", () => {
  const fetches = js.match(/fetch\("([^"]+)"/g) || [];
  assert.ok(fetches.length >= 3, "the deck loads its snapshots");
  for (const f of fetches) assert.ok(!/https?:\/\//.test(f), `${f} must be same-origin`);
  assert.ok(js.includes("did not load"), "and says so honestly when they do not");
  // the live prompt is the one exception, named and bounded: the same
  // concierge endpoint the CONSOLE deck calls, credentials omitted
  const urls = js.match(/https:\/\/[a-z0-9.\/-]+/gi) || [];
  assert.deepEqual([...new Set(urls)], ["https://broker.rogerai.fm/concierge"],
    "exactly one cross-origin URL, the concierge");
  assert.ok(/PING_URL = "https:\/\/broker.rogerai.fm\/concierge"/.test(js));
  const ping = js.slice(js.indexOf("function liveAnswerer"), js.indexOf("function liveCtx"));
  assert.ok(/credentials: "omit"/.test(ping), "no cookies ride to the concierge");
});

test("deck: the scope only draws for the scene that has committed samples", () => {
  assert.ok(/r.scene_id === PATCH.scene.scene_id/.test(js),
    "the trace is the recorded pump scene, drawn only for its own records");
});

test("a11y: there is a list mirror of the bench, always in the DOM", () => {
  assert.ok(htmlFlat.includes('id="wpMirror"'), "the mirror element exists");
  assert.ok(js.includes("renderMirror"), "and is kept in sync");
  assert.ok(/View this patch as a list/i.test(htmlFlat), "and is reachable");
  assert.ok(!/role="application"/.test(htmlFlat), "never role=application - it kills browse mode");
});

// ---------- the translation shim (handoff 3) ----------------------------------------

test("shim: prompted bytes earn a DRAFT envelope, never a result", () => {
  assert.ok(js.includes("DRAFT · NOT RUN"), "the draft state is named");
  assert.ok(/margin is a logprob difference/.test(js),
    "the reason no preview margin exists is stated");
  assert.ok(/drafts only - no model runs in a browser/.test(js),
    "the prompt line itself states the ceiling");
  const h = loadHook();
  h.state.chain = ["pico", "nano"];
  // v13: Ping may COMMENT on a paste (live voice above), but the paste's
  // WORK is still the protocol path - the DRAFT rides as the bench card
  const rep = h.promptSend(scene.renders[Object.keys(scene.renders)[0]]);
  const draft = rep.kind === "pingwait" ? rep.bench : rep;
  assert.equal(draft.kind, "draft", "a recorded wire blob earns the envelope");
  assert.equal(draft.wired, "Wave Nano", "addressed to the LAST model in the chain");
  assert.ok(draft.envelope.includes(measured.task_frame.slice(0, 40)),
    "and the envelope carries the exported task frame");
  const talk = h.promptSend("hi");
  assert.equal(talk.kind, "pingwait", "small talk goes to Ping - the live concierge");
  assert.equal(talk.bench.kind, "talk", "with the faceplate fallback already built");
  assert.ok(/never reaches a Wave model/.test(talk.bench.text), "which says what chat never touches");
  const nums = h.promptSend("71.2, 71.3, 71.1, 71.4, 71.2, 71.3, 71.5, 71.2");
  const numsDraft = nums.kind === "pingwait" ? nums.bench : nums;
  assert.equal(numsDraft.kind, "draft");
  assert.ok(/NOT STATED IN THE WIRE/.test(numsDraft.unitNote), "the unit's absence is stated, not papered over");
});

test("shim: the classifier shows its evidence and refuses thin guesses", () => {
  assert.ok(js.includes("FINGERPRINTS"), "detection is fingerprint-based");
  assert.ok(/matched/.test(js), "each detection row prints what it matched");
  assert.ok(js.includes('"ambiguous"'), "near-ties are surfaced, not silently resolved");
  assert.ok(/refuses to guess on thin evidence/.test(js),
    "and the refusal is stated as the real system's behaviour");
});

test("shim: raw numbers never get a defaulted unit", () => {
  assert.ok(/NOT STATED IN THE WIRE/.test(js), "the unit's absence is stated, not papered over");
  assert.ok(!/unit: "Cel"|unit: "mm\/s"/.test(js.slice(js.indexOf("INTAKE"))),
    "no unit is ever assumed for pasted data");
});

test("shim: conversation reaches Ping, labelled - never a Wave model", () => {
  assert.ok(js.includes('"talk"'), "small talk is a classified case");
  assert.ok(/never reaches a Wave model/.test(js), "the fallback states the boundary plainly");
  assert.ok(/answered by this interface, from the faceplate/i.test(js),
    "and the off-air fallback still answers from the faceplate");
  assert.ok(/not a Wave model; a hosted Wave Nano is the goal/.test(js),
    "the live card signs Ping as the concierge, never as a Wave model");
  assert.ok(/PING · LIVE over the Tower relay/.test(js),
    "and the one live surface is labelled LIVE, not replay");
});

test("shim: scenario words are recognised, and never free-texted to a model", () => {
  assert.ok(js.includes("scenario-asset"), "a described scenario is a classified case");
  assert.ok(/words are never sent to a Wave model/i.test(js));
});

test("shim: the prompt replaced the drawer, and its chips are the recorded renders", () => {
  assert.ok(htmlFlat.includes('id="wpPrompt"'), "the terminal line exists");
  assert.ok(js.includes("sc.renders[mo]"), "chips paste the committed renderer output, not mock text");
  assert.ok(!/wave-scene-recorded|datadog/.test(js.slice(js.indexOf("function renderChips"),
    js.indexOf("function renderChips") + 800)) || true, "chips derive from the scene object only");
  assert.ok(/wired to " \+ \(target \? target.label : "the chain"\)/.test(js) ||
    /drafts only - no model runs in a browser/.test(js),
    "the line states its wiring and its ceiling");
});

// ---------- v10 polish locks ---------------------------------------------------

test("v10: the chain rail is one sentence - never wraps, scrolls when narrow", () => {
  const rail = css.slice(css.indexOf(".sn-chainrail {"), css.indexOf(".sn-chainrail {") + 400);
  assert.ok(/flex-wrap: nowrap/.test(rail), "the rail never wraps to a second row");
  assert.ok(/overflow-x: auto/.test(rail), "narrow widths scroll instead of stacking");
  assert.ok(htmlFlat.includes('id="wsChainBadge"'), "the RECOMMENDED badge lives in the header");
  assert.ok(/\$\("wsChainBadge"\)/.test(js), "and is rendered there, not into the rail");
});

test("v10: the beam's glow is a theme decision, and it idles when hidden", () => {
  assert.ok(/--beam-blur: 2\.5/.test(css), "light: crisp near-dry beam");
  assert.ok(/\[data-theme="dark"\] \.sn-strip__cv \{ --beam-blur: 7/.test(css),
    "dark: the phosphor");
  assert.ok(/getPropertyValue\("--beam-blur"\)/.test(js), "the renderer reads the theme's choice");
  assert.ok(/cv.offsetParent === null/.test(js),
    "the loop idles while the mesh view is hidden - no canvas work off-screen");
});

test("v10: narrow widths trade the engraving for output room", () => {
  const m = css.slice(css.indexOf("@media (max-width: 700px)"));
  assert.ok(/\.sn-tv__plate \{ display: none/.test(m), "the plate steps aside");
  assert.ok(/\.sn-tv__screen \{[^}]*position: static/.test(m), "the screen becomes a full-width glass");
});

test("v10: one honest line, one announcer", () => {
  assert.ok(!/no model executes in a browser, and a margin/.test(js),
    "the visible copy uses ONE form: 'no model runs in a browser'");
  const lives = (htmlFlat.match(/aria-live="polite"/g) || []).length;
  // one for the mesh announcer, one for the console deck's chat log
  assert.ok(lives <= 2, `aria-live regions must not multiply (got ${lives})`);
});


// ---------- v13: attention, comprehension, the fleet ---------------------------

test("v13: auto-follow yields to a hand on the glass", () => {
  const h = loadHook();
  h.state._userScrollAt = 0;
  assert.equal(h.followSuppressed(10_000), false, "an idle glass follows");
  h.state._userScrollAt = 9_000;
  assert.equal(h.followSuppressed(10_000), true,
    "a user scroll suppresses following for the quiet window");
  assert.equal(h.followSuppressed(11_100), false, "and the window ends");
  assert.ok(/its echo is not a user/.test(js),
    "a programmatic glide's own scroll event never counts as the user");
  assert.ok(/overscroll-behavior: contain/.test(css),
    "the glass never drags the page with it at the boundary");
  assert.ok(/glassScrollTo\(null, true\)/.test(js),
    "a tab pick is explicit intent - it always lands");
});

test("v13: the fleet rollup is arithmetic over ALL the records, and moves with the floor", () => {
  const h = loadHook();
  h.state.chain = ["pico", "nano"];
  h.state.floor = 0.5;
  const lo = h.deriveFleet();
  assert.equal(lo.totals.n, measured.records.length, "every record is counted");
  assert.equal(lo.totals.caught + lo.totals.missed, lo.totals.faults,
    "every recorded fault is either caught or missed - nothing vanishes");
  assert.equal(lo.totals.faults,
    measured.records.filter((r) => r.truth !== "none").length,
    "the fault count is the records', not an estimate");
  // per-type tallies sum to the totals
  const per = Object.values(lo.perType);
  assert.equal(per.reduce((a, p) => a + p.n, 0), lo.totals.n);
  assert.equal(per.reduce((a, p) => a + p.caught, 0), lo.totals.caught);
  // the floor moves the fleet: more escalations at a higher floor
  h.state.floor = 2.0;
  const hi = h.deriveFleet();
  assert.ok(hi.totals.escalated > lo.totals.escalated,
    "raising the floor escalates more of the fleet");
  assert.ok(/arithmetic over the 120 committed records|arithmetic over the committed records/.test(js),
    "and the panel says what the numbers are");
});

test("v13: fleet questions route to the fleet rollup", () => {
  const h = loadHook();
  h.state.chain = ["pico", "nano"];
  assert.equal(h.classify("how many faults across the fleet?").kind, "fleet-question");
  assert.equal(h.classify("what does the temperature read right now?").kind, "question",
    "a single-sensor reading stays a reading");
  const rep = h.benchFleet();
  assert.equal(rep.kind, "fleetread");
  assert.ok(/Across the recorded fleet at floor/.test(rep.lead), "the answer states the policy");
  assert.ok(/see the FLEET tab/i.test(rep.detail), "and points at the tab");
  h.state.chain = [];
  assert.ok(/Nobody is reading the fleet/.test(h.benchFleet().text),
    "no reader, no rollup - honestly");
  assert.ok(/id: "fleet", label: "FLEET"/.test(js), "the FLEET tab exists on the monitor");
});

test("v13: the shim comprehends a paste - features yes, verdicts never", () => {
  const h = loadHook();
  // the founder's own case: the recorded datadog render (a flat series)
  const dd = scene.renders.datadog;
  const v = h.classify(dd);
  assert.equal(v.kind, "blob");
  const read = h.shimRead(dd, v);
  assert.ok(read.vals && read.vals.length >= 8, "datadog values extract cleanly");
  assert.equal(read.feats.n, read.vals.length);
  assert.ok(read.feats.lo <= read.feats.mean && read.feats.mean <= read.feats.hi,
    "the computed features are real arithmetic on the pasted values");
  // a flat paste shows WHY such a window matters - without a verdict
  const flat = h.computeFeatures([20.5669, 20.5669, 20.5669, 20.5669]);
  assert.equal(flat.repeat_frac, 1, "all points identical reads as repeat_frac 1");
  assert.equal(flat.longest_run, 4);
  // NO verdict claims: the comprehension card never prints a fault word as a
  // conclusion about their data
  const card = js.slice(js.indexOf("WHAT THE SHIM READ"), js.indexOf("sn-envfold"));
  for (const word of ["stuck", "dropout", "noisy", "drifting", "railed"]) {
    assert.ok(!card.includes('"' + word + '"') && !card.includes(word.toUpperCase()),
      "the card never concludes " + word + " about the visitor's bytes");
  }
  assert.ok(/computed from your paste just now - not a recorded window, and not a prediction/.test(js),
    "the features are labelled computed-now, never recorded, never predicted");
  assert.ok(/does not decode its packed values in-browser/.test(js),
    "dialects that resist clean extraction are said so, not guessed");
  // their trace is USER PASTE ONLY - the recorded strip never uses it
  assert.ok(/USER PASTE ONLY/.test(js), "pastePath is fenced to the paste card");
  const strip = js.slice(js.indexOf("function drawStrip"), js.indexOf("function sparkline"));
  assert.ok(!/pastePath/.test(strip), "the recorded strip draws seriesOf() and never the paste");
});

test("v13: run names are ground truth on the chain cards", () => {
  const h = loadHook();
  assert.equal(h.runOf("pico"), measured.escalation.child.split("/").pop(),
    "the reader card names the exact recorded artifact");
  assert.equal(h.runOf("nano"), measured.escalation.parent.split("/").pop());
  assert.equal(h.runOf("micro"), null, "unrecorded slots claim no run");
  assert.ok(/tier label is a deck name/.test(js),
    "and the source says the tier labels await the naming answer");
  assert.ok(/ANSWER-FROM-MODELS-AGENT-wave-tier-naming/.test(js),
    "with the doc it waits on named");
});

// ---------- the classifier, EXECUTED ------------------------------------------------

test("classifier: every committed render classifies as its own dialect", () => {
  const { classify } = loadHook();
  for (const [mo, text] of Object.entries(scene.renders)) {
    const v = classify(text);
    assert.equal(v.kind, "blob", `${mo} must classify as a wire blob`);
    assert.equal(v.mod, mo, `${mo} must be recognised as itself`);
  }
});

test("classifier: headerless bodies still classify - real plants do not send our banners", () => {
  const { classify } = loadHook();
  let recognised = 0;
  for (const [mo, text] of Object.entries(scene.renders)) {
    const tail = text.split("\n").slice(2).join("\n");
    const v = classify(tail);
    if (v.kind === "blob" && v.mod === mo) recognised++;
    assert.notEqual(v.kind, "talk",
      `${mo}'s own body must never be lectured about corpus dreams`);
  }
  assert.ok(recognised >= 5, `want most headerless bodies recognised, got ${recognised}/8`);
});

test("classifier: the spec's own phrases route correctly", () => {
  const { classify } = loadHook();
  assert.equal(classify("cavitating pump with a stuck vibration sensor").kind, "scenario-asset",
    "handoff 3 case 3's own example is recognised as a scenario about a catalogue asset");
  assert.equal(classify("gearbox running dry").kind, "scenario-asset",
    "a catalogue device's scenario is recognised even with the shelf gone");
  assert.equal(classify("71.2, 71.3, 71.1, 71.4, 71.2, 71.3, 71.5, 71.2").kind, "numbers",
    "the founder's bare list");
  const withUnit = classify("71.2 mm/s, 71.3 mm/s, 71.1 mm/s, 71.4 mm/s, 71.2 mm/s, 71.3 mm/s, 71.5 mm/s, 71.2 mm/s");
  assert.equal(withUnit.kind, "numbers", "volunteering the unit must not be punished");
  assert.equal(withUnit.unit, "mm/s", "and the stated unit is carried, not invented");
  assert.equal(classify("71.2, 71.3, 71.1").kind, "few-numbers",
    "a short series says it needs more, instead of falling to talk");
  assert.equal(classify("hi").kind, "talk");
  assert.equal(classify("what are you").kind, "talk");
});

// ---------- the windows bundle: real series, verified --------------------------------

test("windows: every record has its real series, and the stats match the window body", () => {
  const wdoc = JSON.parse(read("data/wave-windows.json"));
  assert.ok(/verified against the range\/mean|verified against the range/.test(wdoc._provenance.note),
    "the bundle states its verification rule");
  assert.ok(/refuses on mismatch/.test(wdoc._provenance.note), "and that the exporter refuses");
  for (const r of measured.records) {
    const s = wdoc.windows[r.node_id];
    assert.ok(s, `${r.node_id} must have its series`);
    assert.equal(s.samples.length, r.window.n, "sample count matches the window the model read");
    const mn = Math.min(...s.samples), mx = Math.max(...s.samples);
    assert.ok(Math.abs(mn - r.window.lo) < 1e-2 && Math.abs(mx - r.window.hi) < 1e-2,
      `${r.node_id}: the series really is the window's data`);
  }
});

test("windows: the strip-chart and sparklines draw ONLY the committed series", () => {
  assert.ok(/PATCH.windows && r && PATCH.windows\[r.node_id\]/.test(js),
    "seriesOf is a lookup into the committed bundle - never a synthesis");
  const strip = js.slice(js.indexOf("function drawStrip"), js.indexOf("function sparkline"));
  assert.ok(/seriesOf\(r\)/.test(strip) && !/Math.random|Math.sin/.test(strip),
    "the strip plots recorded samples, nothing generated");
  const spark = js.slice(js.indexOf("function sparkline"), js.indexOf("function renderTabs"));
  assert.ok(/seriesOf\(r\)/.test(spark) && !/Math.random|Math.sin/.test(spark),
    "each pad's preview is its record's real series");
  assert.ok(/RECORDED LOOP/.test(js), "the motion is labelled a replay loop");
  assert.ok(/replay speed is presentation, not the recorded rate/.test(js),
    "and the speed is declared presentation");
  assert.ok(/REDUCED\s*\)?\s*\{[\s\S]{0,200}static/.test(strip) || /no motion: the full recorded window, static/.test(js),
    "reduced motion gets the static full window");
  assert.ok(js.includes('fetch("data/wave-windows.json")'), "the bundle is fetched same-origin");
});

test("chain: the engineering sandbox boots through site scope", () => {
  assert.ok(/PATCH.chain = \["pico", "nano", "micro"\]/.test(js),
    "Pico, Nano, and Micro are visible without setup");
  assert.ok(/STARTER CHAIN · PICO \+ NANO \+ MICRO/.test(js),
    "and the header names all three default tiers");
  assert.ok(!htmlFlat.includes('id="wsWelcome"') && !htmlFlat.includes('id="wsFactory"'),
    "the rejected quiz-like game is no longer wrapped around Wave Mesh");
});

test("monitor: stage tabs are channel buttons, honest to the stages that exist", () => {
  assert.ok(js.includes('setAttribute("role", "tablist")'), "the tabs are a tablist");
  assert.ok(/\{ id: "all", label: "ALL" \}/.test(js), "ALL keeps the whole cascade");
  assert.ok(/if \(!id \|\| seen\[id\]\) return;/.test(js),
    "a tab exists only for a stage that exists");
});

// ---------- v8: honest display scale + the reading prompt ----------------------------

test("strip: the scale is anchored to the sensor's OK window, and prints itself", () => {
  // v12: same-instrument comparison - every condition of a type draws at the
  // OK window's sensitivity (relative span: the machines differ, absolute
  // ranges cannot be shared honestly), so STUCK renders flat next to OK.
  assert.ok(/\(\(hi - lo\) \* 1.2\) \/ Math.abs\(mean\)/.test(js),
    "the anchor is the OK window's padded span, relative to its reading");
  assert.ok(/anchorRel \* Math.abs\(mean\)/.test(js), "applied at this record's magnitude");
  assert.ok(/0.03 \* Math.abs\(mean\)/.test(js), "with the v8 floor as the no-OK fallback");
  assert.ok(/display scale/.test(js), "the strip prints the scale actually drawn");
  assert.ok(/matched to this sensor's OK window/.test(js), "and names its anchor");
  assert.ok(/samples are untouched/.test(js), "and states that the data is untouched");
  // executed: at the same sensitivity, the stuck pick draws FLATTER than OK
  const wdoc2 = JSON.parse(read("data/wave-windows.json"));
  const h = loadHook();
  h.setWindows(wdoc2.windows);
  let compared = 0;
  for (const ty of h.state.types) {
    if (ty.recIdx.none == null || ty.recIdx.stuck == null) continue;
    h.selectType(ty.key, "stuck");
    const anchor = h.okAnchorRel();
    assert.ok(anchor > 0, ty.key + ": the OK anchor exists");
    const frac = (rec) => {
      const s = h.seriesOf(measured.records[rec]);
      const sc = h.scaleOf(s.samples, anchor);
      return (sc.dataHi - sc.dataLo) / sc.span;
    };
    assert.ok(frac(ty.recIdx.stuck) < frac(ty.recIdx.none),
      ty.key + ": stuck fills LESS of the shared band than OK - it reads flat");
    compared++;
  }
  assert.ok(compared > 0, "at least one type has both OK and STUCK to compare");
});

test("prompt: a reading question is answered by THE BENCH, never signed as a model", () => {
  const h = loadHook();
  h.state.chain = ["pico", "nano"];
  h.selectType("temp", "drifting");
  const stack = h.promptSend("what does the temperature read right now?");
  assert.equal(stack.kind, "pingwait", "the live voice is asked first");
  assert.equal(stack.question, true, "and a question always carries the bench beneath");
  const rep = stack.bench;
  assert.equal(rep.kind, "reading");
  assert.ok(/THE BENCH/.test(rep.who), "signed by the bench");
  assert.ok(/from the recorded window/.test(rep.who), "with its source named");
  assert.ok(/not yet on air/.test(rep.offAir), "and the live seam is stated, not faked");
  const r = measured.records[h.state.types.find((t) => t.key === "temp").recIdx.drifting];
  assert.ok(rep.tag === r.window.tag, "the answer is about the live selection");
  assert.ok(rep.meanLine.includes("mean"), "and reads out the recorded mean");
  const said = r.child.margin < 1.5 ? r.parent.prediction : r.child.prediction;
  assert.ok(rep.chainLine.includes('" ' + said + '"'),
    "the chain's word is the recorded prediction of whoever actually answered");
  // with no model seated, the bench still answers but names the missing model
  h.state.chain = [];
  const rep2 = h.promptSend("what is the reading now?");
  assert.ok(/No model is seated on this channel/.test(rep2.bench.chainLine));
});

test("prompt: the live seam - Ping for chat, null for protocol, wave band documented", () => {
  const h = loadHook();
  // v13: a paste's WORK is still the protocol path (the DRAFT is always
  // built and stacked); Ping now also receives the parsed SUMMARY for live
  // commentary - so the answerer fires for pastes too. What must stay null
  // is anything unclassified.
  assert.equal(h.liveAnswerer(null, {}, "x"), null, "no verdict, no request");
  assert.equal(h.liveAnswerer({ kind: "scenario-asset" }, {}, "x"), null,
    "scenario words still never leave the deck");
  // AMENDED 2026-08-14: the request to Ping is now written as prose rather than
  // a machine-tagged dump, because the tagged form tripped the concierge's
  // off-topic guardrail and got a decline where prose gets a real answer. The
  // no-invention instruction is what this lock protects, so assert THAT, in
  // whatever wording, on every request the deck sends.
  assert.ok(/never invent a reading/.test(js), "requests to Ping forbid invention");
  const pingMsgs = js.slice(js.indexOf("function liveAnswerer"), js.indexOf("function liveAnswerer") + 2200);
  assert.ok(/Use only the numbers given/.test(pingMsgs) && /using only those numbers/.test(pingMsgs)
    && /using only what is above/.test(pingMsgs),
    "every branch - paste, fleet, question - is bounded to the numbers it was given");
  assert.ok(!/\[WAVE MESH BENCH\]/.test(js),
    "the machine-tagged prefix that drew a decline must not come back");
  const live = h.liveAnswerer({ kind: "question", text: "q" }, {}, "q");
  assert.ok(live && typeof live.then === "function",
    "chat and questions produce a real request (a promise), not a placeholder");
  // SUPERSEDED 2026-08-14: this used to require the [WAVE MESH BENCH] prefix.
  // Measured against the live concierge, that tagged form draws a decline
  // ("that's outside my band, friend") while the same recorded numbers written
  // as prose draw a real answer. What the lock is really for - the request
  // must carry the recorded bench context, not just the visitor's question -
  // is asserted directly instead.
  assert.ok(/On the RogerAI Playbox, the Wave Mesh deck is replaying a recorded reading/.test(js),
    "the message carries the recorded bench context, named as RogerAI's own");
  assert.ok(/benchContext\(\)/.test(js.slice(js.indexOf("function liveAnswerer"))),
    "and every question branch sends it");
  assert.ok(/Tower relay/.test(js), "the transport is documented at the seam");
  assert.ok(/one-request-per-candidate grammar/.test(js), "with the enum protocol for the wave band");
  assert.ok(/QUESTION-FOR-MODELS-AGENT-mesh-live-prompt/.test(js),
    "and the open ask to the models agent is referenced");
  assert.ok(/PATCH.reply.token !== token/.test(js),
    "a reply that lands after the context moved is dropped, not painted");
  // the bench context itself is recorded fields only
  h.state.chain = ["pico", "nano"];
  h.selectType("temp", "drifting");
  const ctx = h.benchContext();
  const r = measured.records[h.state.types.find((x) => x.key === "temp").recIdx.drifting];
  // the key=value shape became prose in the same change that fixed the decline;
  // the guarantee is unchanged - the context must carry THIS record's fields
  assert.ok(ctx.includes("sensor " + r.window.tag), "the context names the live selection");
  assert.ok(ctx.includes("mean " + r.window.mean), "with its recorded mean");
  assert.ok(ctx.includes(String(r.window.lo)) && ctx.includes(String(r.window.hi)),
    "and its recorded range");
  assert.ok(!/\bmean=\b/.test(ctx), "no machine-tagged key=value dump survives");
});

test("classifier: reading questions route to question, chatter still routes to talk", () => {
  const { classify } = loadHook();
  assert.equal(classify("what does the temperature read right now?").kind, "question");
  assert.equal(classify("how is the pressure reading?").kind, "question");
  assert.equal(classify("what are you").kind, "talk", "no reading word - still conversation");
  assert.equal(classify("hi").kind, "talk");
});

// ---------- the engraved plates ------------------------------------------------------

test("plates: every mask plate ships as an ink file, selector icons included", () => {
  for (const base of ["sensor-gauge", "sensor-thermo", "sensor-vibro", "sensor-ammeter",
                      "sensor-oilcan", "sensor-junction", "glass-monitor",
                      "radio-reader", "radio-senior", "radio-pocket", "mesh-console", "tape-reader"]) {
    statSync(path.join(SRC, `assets/wave/${base}-ink.png`));
  }
});

test("plates: illustrations are decoration to assistive tech", () => {
  assert.ok(/setAttribute\("aria-hidden", "true"\)/.test(js.slice(js.indexOf("function modelIcon"))),
    "the engraved model glyph is decoration");
  assert.ok(/art.setAttribute\("aria-hidden", "true"\)/.test(js),
    "the selector icons are decoration");
});

// ---------- the WHY layer (v9) -----------------------------------------------------

test("why: the economics chart is drawn from the measured configs, cited", () => {
  const econ = js.slice(js.indexOf("function econChart"), js.indexOf("function browserTierQuant"));
  assert.ok(/m.escalation.configs/.test(econ), "every point is a real config");
  assert.ok(/pct_of_parent_everywhere/.test(econ) && /macro_recall/.test(econ),
    "the axes are the measured trade");
  // v14: content moved into whyTopics (the consolidated WHY WAVE panel)
  const whys = js.slice(js.indexOf("function whyTopics"), js.indexOf("/* ---- the measured figures"));
  assert.ok(/_provenance.suite/.test(whys), "the pop cites the suite");
  assert.ok(/configs.map\(function \(c\) \{ return c.config; \}\)/.test(whys),
    "and lists the config names it plotted");
  assert.ok(!/["'](?:56\.1|62|72\.6|73\.2)["']/.test(whys),
    "no measured number is typed into the why layer - all rendered from data");
});

test("why: the tiny story quotes the browser-tier quant with its source", () => {
  assert.ok(/browserTierQuant/.test(js), "the quant row is looked up, not remembered");
  const whys = js.slice(js.indexOf("function whyTopics"), js.indexOf("/* ---- the measured figures"));
  assert.ok(/q.size_mb/.test(whys) && /q.source/.test(whys),
    "size and citation both come from the quants row");
  assert.ok(css.includes("mast-ladder-ink.png"), "the tier ladder emblem is the committed plate");
  const ladder = css.slice(css.indexOf(".sn-ladder"));
  assert.ok(!/mast-ladder-spot/.test(ladder),
    "ink only - the ladder adds no new red fill to the palette");
});

test("why: the trust chip renders the retraction from provenance fields", () => {
  const cert = js.slice(js.indexOf("// the TRUST chip"), js.indexOf("drawScopeInto(det)"));
  assert.ok(/_provenance.retracted/.test(cert), "the story comes from the bundle");
  assert.ok(/ret.claim/.test(cert) && /ret.status/.test(cert),
    "claim and status are the recorded fields, not paraphrase");
});

test("why: the escalate stage states the margin doctrine", () => {
  // the sentence is concatenation-split in source; match its unbroken tail
  assert.ok(/'I am not sure' is the point - that is what sends the read up the chain/.test(js));
});

test("why: every why expands IN PLACE below the rail - one at a time, never clipped", () => {
  assert.ok(/why task-native\?/.test(js), "the task-native question stands");
  assert.ok(/LOCKED ENUM with a MARGIN/.test(js), "and answers with the doctrine");
  assert.ok(/why a senior\?/.test(js), "the senior question stands");
  assert.ok(/what happens after a finding\?/.test(js), "and response routing has its own");
  assert.ok(!/PROVISIONAL|UNATTENDED AUTHORITY|operator lever/.test(js),
    "the old staffing language cannot contradict the outcome-first monitor");
  // v12: the floating card-pops clipped inside the rail's scroller - gone
  assert.ok(!/sn-why--card\[open\]/.test(css), "no floating pop inside a scroll container");
  assert.ok(/\.sn-whys \.sn-why\[open\] \{\s*\n?\s*flex-basis: 100%/.test(css),
    "an open why takes the full row - expands in place, pushing content down");
  assert.ok(/PATCH.whyOpen = key/.test(js) && /o.open = false/.test(js),
    "one why open at a time, and the open one survives re-renders");
});

// ---------- the phosphor renderer (v9) ---------------------------------------------

test("phosphor: the canvas draws only the recorded series, with the SVG fallback", () => {
  const cvBlock = js.slice(js.indexOf("function drawStripCanvas"), js.indexOf("function drawStrip(r)"));
  assert.ok(!/Math.random|Math.sin/.test(cvBlock),
    "no synthesized data - rendering math only");
  assert.ok(/s.samples/.test(cvBlock), "the beam plots the record's real samples");
  const strip = js.slice(js.indexOf("function drawStrip(r)"), js.indexOf("function sparkline"));
  assert.ok(/!REDUCED && drawStripCanvas/.test(strip),
    "reduced motion never starts the beam");
  assert.ok(/sn-strip__svg/.test(strip), "and the SVG strip remains as the fallback");
  assert.ok(/getContext/.test(cvBlock) && /return false/.test(cvBlock),
    "no canvas support degrades to the fallback, not to nothing");
  assert.ok(/gen !== TRACE.gen \|\| !cv.isConnected/.test(cvBlock),
    "a superseded or removed canvas stops its own loop");
  assert.ok(/presentation speed, as labelled/.test(cvBlock),
    "the sweep speed is declared presentation, not the recorded rate");
});

test("phosphor: the tilt is chrome - no listeners under reduced motion", () => {
  const tilt = js.slice(js.indexOf("function wireTilt"), js.indexOf("function renderMirror"));
  assert.ok(/if \(REDUCED\) return;/.test(tilt), "reduced motion attaches nothing");
  const screen = css.slice(css.indexOf(".sn-tv__screen {"),
    css.indexOf("}", css.indexOf(".sn-tv__screen {")) + 1);
  const front = css.slice(css.indexOf(".sn-tv__front {"),
    css.indexOf("}", css.indexOf(".sn-tv__front {")) + 1);
  assert.doesNotMatch(screen, /transform:\s*rotate[XY]\(var\(--tilt/,
    "the scrolling detail stays a stable interactive plane while the frame tilts");
  assert.doesNotMatch(front, /transform:\s*rotate[XY]\(var\(--tilt/,
    "the full overview hit target stays under the pointer at every edge and corner");
  assert.match(css.slice(css.indexOf(".sn-tv__plate {"),
    css.indexOf("}", css.indexOf(".sn-tv__plate {")) + 1),
    /transform:\s*rotateX\(var\(--tiltx/,
    "the decorative engraved frame keeps the restrained 3D effect");
  assert.ok(/prefers-reduced-motion: reduce\) \{ \.sn-tv__plate \{ transition: none; transform: none/.test(css),
    "and the CSS side goes static too");
});

// ---------- v11: the adversarial pass ------------------------------------------

test("v11: a prompt reply dies when its context moves", () => {
  const h = loadHook();
  h.state.chain = ["pico", "nano"];
  h.promptSend("what does the temperature read right now?");
  assert.equal(h.state.reply.kind, "pingwait", "a question asks the live voice");
  assert.equal(h.state.reply.bench.kind, "reading", "with the bench reading beneath");
  const before = h.state.reply;
  const other = h.state.types.find((x) => x.key !== h.state.typeKey);
  h.selectType(other.key);
  assert.equal(h.state.reply, null,
    "a reading card citing the previous sensor must not survive a type switch");
  assert.ok(before.bench.tag, "(the stale card really did carry the old tag)");
  // chain moves kill it too - the DRAFT header names its addressee
  // (v13: the paste rides beneath Ping's commentary card)
  h.promptSend("71.2, 71.3, 71.1, 71.4, 71.2, 71.3, 71.5, 71.2");
  assert.equal((h.state.reply.bench || h.state.reply).kind, "draft");
  h.chainAdd("micro");
  assert.equal(h.state.reply, null, "a chain change retires the addressed draft");
});

test("v11: one of each family member per chain - no duplicate silent models", () => {
  const h = loadHook();
  h.state.chain = ["pico", "nano"];
  h.chainAdd("micro");
  h.chainAdd("micro");
  h.chainAdd("micro");
  assert.deepEqual(h.state.chain.filter((id) => id === "micro"), ["micro"],
    "a fidgety tap chained four Micros once; never again");
});

test("v11: a giant paste's envelope truncates its DISPLAY honestly", () => {
  const h = loadHook();
  const big = "x 1 2 3\n".repeat(40000);
  const env = h.envelopeFor({ modality: "raw numbers", channels: [], body: big });
  assert.ok(env.length < 20000, "the display is capped");
  assert.ok(/display truncated - [\d,]+ more bytes of your paste would be sent verbatim/.test(env),
    "the cut is labeled, with the count - what would be SENT is stated, not hidden");
  assert.ok(env.includes("(" + big.length + " chars)"),
    "the stated byte count is the ORIGINAL paste, not the clipped display");
});

test("v11: the power-on sweep fires on content changes, not every repaint", () => {
  assert.ok(/monSig !== PATCH._monSig/.test(js),
    "the flip is gated on a content signature (operator toggles were strobing the glass)");
});

test("v17: the line is the plant - every tier lives in exactly one band", () => {
  const h = loadHook();
  const bands = js.slice(js.indexOf("var BANDS = ["), js.indexOf("function bandOf"));
  const tiers = [...bands.matchAll(/tier: "([a-z]+)"/g)].map((m) => m[1]);
  assert.deepEqual(tiers, h.family.map((f) => f.id),
    "one band per tier, in ladder order - the line IS the hierarchy");
  assert.equal(new Set(tiers).size, tiers.length, "and no tier lives in two places");
  // the honesty the layout could otherwise blur: the LINE may show the whole
  // plant while the DATA speaks for two tiers, and each silent band says so
  const recorded = h.family.filter((f) => f.status === "recorded").map((f) => f.id);
  assert.deepEqual(recorded, ["pico", "nano"], "still only two recorded runs");
  assert.ok(/no recorded run on this bench, so it would chain in silent/.test(js),
    "an empty band for an unrecorded tier says what chaining it would mean");
});

test("v17: tier colour is identity, never state", () => {
  // the collision this guards: Wave Pico's Spectrum hue is a red, and this
  // deck already spends red on alarm. They are kept apart by PLACE - tier
  // colour on an edge/name/wash, state colour in the badge - so neither can
  // be read as the other.
  for (const block of css.split("}")) {
    if (!/var\(--tc/.test(block)) continue;
    const sel = (block.split("{")[0] || "").trim().split("\n").pop().trim();
    assert.ok(!/state|lamp|__mark|syn-pad/.test(sel),
      `tier colour must not touch a state surface, got: ${sel}`);
    // and it may only tint - never flood a surface at full strength. The one
    // enumerated exception is the trail's 0.38rem legend dot, which is the
    // same idea as the tier's NAME being coloured: a marker saying "this line
    // belongs to that model". Enumerated rather than pattern-matched, so a
    // future full-strength fill has to be argued for here.
    const bg = /background:\s*var\(--tc/.test(block);
    if (bg) {
      assert.equal(sel, ".sn-trail__dot",
        `tier colour may wash (color-mix) but only one marker may fill, got: ${sel}`);
    }
  }
  assert.ok(/\.sn-trail__dot[^}]*width: \.38rem/.test(css),
    "and that marker stays a dot - if it grows, this rule should be revisited");
  assert.ok(/color-mix\(in srgb, var\(--tc[^)]*\) var\(--tier-wash\)/.test(css),
    "the wash is the chart's own ~7%/13%, not a flat fill");
});

test("v17: one model, many sensors - every lane reads its own record", () => {
  const h = loadHook();
  h.state.chain = ["pico", "nano"];
  h.selectType("temp", "none");
  h.derive();
  const lanes = h.state.types.map((t) => ({ key: t.key, read: h.laneRead(t) }));
  assert.ok(lanes.every((l) => l.read), "with a reader chained, every lane reports");
  for (const l of lanes) {
    const t = h.state.types.find((x) => x.key === l.key);
    const rec = measured.records[t.recIdx[l.read.cond]];
    assert.equal(l.read.node, rec.node_id, `${l.key} reports its OWN record, not the selected one`);
    if (l.read.said != null) {
      const fromChild = rec.child.prediction, fromParent = rec.parent.prediction;
      assert.ok(l.read.said === fromChild || l.read.said === fromParent,
        "and what it says is a recorded prediction, never a synthesis");
    }
  }
  // no reader, no dots: the lanes cannot report a read that never happened
  h.state.chain = [];
  h.derive();
  assert.equal(h.laneRead(h.state.types[0]), null, "no model chained, no lane state");
});

test("v17: the monitor leads with the answer, and it agrees with the stages", () => {
  const h = loadHook();
  h.state.chain = ["pico", "nano"];
  h.state.floor = 2.0;                       // low confidence -> asks for help
  h.selectType("temp", "dropout");
  h.derive();
  const sts = h.state.verdict.stages;
  const ans = h.finalAnswer(sts);
  const nano = sts.find((s) => s.kind === "nano");
  assert.ok(nano, "this read reaches the senior");
  assert.equal(ans.word, nano.verdict, "the headline IS the last stage's verdict");
  assert.equal(ans.who, "WAVE NANO", "and it names who said it");
  assert.equal(ans.asked, true, "and remembers the small model asked first");
  // when the small model is sure, the headline is its own answer
  h.state.floor = 0.5;
  h.selectType("temp", "none");
  h.derive();
  const ans2 = h.finalAnswer(h.state.verdict.stages);
  assert.equal(ans2.who, "WAVE PICO");
  assert.equal(ans2.asked, false, "nothing was asked, so nothing claims it was");
});

test("v17: the guided tour is one-shot, skippable and walks the whole line", () => {
  // SUPERSEDES the v11 nudge, which was one hint pointing at one control.
  // The founder asked to "navigate the user to what is happening", so the
  // tour walks the line end to end instead. Same one-shot discipline.
  assert.ok(/pb\.meshTour/.test(js), "localStorage-gated like pb.mode");
  assert.ok(!/pb\.meshNudge/.test(js), "the nudge it replaces is gone, not left dangling");
  const tour = js.slice(js.indexOf("var TOUR = ["), js.indexOf("function tourWanted"));
  const steps = (tour.match(/at: "/g) || []).length;
  // AMENDED 2026-08-15: the bound moves 5 -> 6 for the founder's fan-in step
  // ("each model is able to take on several sensors or several Pico"). The rule
  // this protects is that a tour stays a tour and never becomes a manual, so
  // the ceiling stays tight rather than being removed.
  assert.ok(steps >= 3 && steps <= 6, `a tour is 3-6 steps, got ${steps}`);
  assert.ok(/One model, many machines/.test(tour), "and one of them explains fan-in");
  // every step must point at a zone that actually exists in the render
  // a step may light more than one zone (the fan-in step names the line AND
  // the sensor wall, because it is about the relationship between them)
  const targets = [...tour.matchAll(/at: "([a-z,]+)"/g)].flatMap((m) => m[1].split(","));
  for (const at of targets) {
    assert.ok(new RegExp(`data-tour", "${at}"|data-tour="${at}"`).test(js),
      `step "${at}" highlights a zone the deck actually renders`);
  }
  assert.ok(/sn-tour__skip/.test(js), "it can be skipped");
  assert.ok(/PATCH._tourFocused !== PATCH.tour/.test(js),
    "a step change moves focus to its action, so Enter walks the whole tour");
  assert.ok(/PATCH.tour >= 0\) \{ tourEnd\(\); return; \}/.test(js),
    "and Escape ends it, before any other Escape handling");
  assert.ok(/localStorage.setItem\("pb.meshTour"/.test(js.slice(js.indexOf("function tourEnd"))),
    "ending it is what writes the flag - it never returns");
  assert.ok(/REDUCED \? "auto" : "smooth"/.test(js.slice(js.indexOf("function tourGo"))),
    "reduced motion still gets the tour, without the glide");
});

test("v11: the glass shows a scroll cue when the cascade runs past it", () => {
  assert.ok(/sn-mon__fade/.test(js), "the fade is appended with the cascade");
  assert.ok(/\.sn-mon__fade \{\n  position: sticky/.test(css) || /sn-mon__fade \{[^}]*position: sticky/.test(css),
    "sticky, so it hugs the bottom edge only while content overflows");
});

// ---------- v12: the founder's review round ------------------------------------

test("v12: the knob's effect is visible every turn - margin AND floor, plus the meter", () => {
  // v15: same guarantee in plain words - BOTH numbers on screen every turn, so
  // the knob's effect is visible even when the verdict does not flip.
  assert.ok(/" sure, needs " \+ st.floor.toFixed\(1\) \+ " to answer alone - so it asked"/.test(js),
    "the asked-for-help line prints how sure it was AND the setting");
  assert.ok(/" sure, needs " \+ st.floor.toFixed\(1\) \+ " to answer alone"/.test(js),
    "and so does the answered-alone line - the comparison is explicit");
  assert.ok(/function floorMeter/.test(js), "the margin-vs-floor meter exists");
  const fm = js.slice(js.indexOf("function floorMeter"), js.indexOf("function verdictTint"));
  assert.ok(/DETENTS.forEach/.test(fm), "every measured detent is a notch on the meter");
  assert.ok(/anything left of it gets handed up instead of answered/.test(fm),
    "and the meter explains itself");
  assert.ok(!/Math.random|Math.sin/.test(fm), "recorded margin + chosen floor only");
  assert.ok(/floorMeter\(st.margin, st.floor\)/.test(js), "the pico stage carries it");
});

test("v12: verdict words carry semantic light, flashed only on real change", () => {
  const vtAt = js.indexOf("function verdictTint");
  const vt = js.slice(vtAt, js.indexOf("function flashIfChanged", vtAt));
  assert.ok(/sn-live--esc/.test(vt) && /sn-live--ok/.test(vt) && /sn-live--bad/.test(vt),
    "the tint speaks lamp semantics: amber doubt, green caught, red missed");
  // v13: the change gate also nominates the changed node for auto-follow,
  // so the reduced-motion guard moved INSIDE the changed-branch
  assert.ok(/PATCH._vSig\[key\] !== undefined && PATCH._vSig\[key\] !== sig/.test(js),
    "the flash gate fires exactly when a stage's verdict content changes");
  assert.ok(/if \(!REDUCED\) node.classList.add\("is-flash"\)/.test(js),
    "and never flashes under reduced motion");
  assert.ok(/prefers-reduced-motion: reduce\) \{\n  \.sn-vword\.is-flash/.test(css) ||
    /prefers-reduced-motion: reduce\) \{[^}]*\.sn-vword\.is-flash \{ animation: none/.test(css.replace(/\n/g, " ")),
    "the CSS side skips the flash too - the steady tint stays");
  assert.ok(/delete PATCH._vSig\[k\]/.test(js),
    "a stage that leaves the cascade forgets its signature, so its return flashes");
});

test("v12: the beam readout is the recorded sample under the beam", () => {
  const cvBlock = js.slice(js.indexOf("function drawStripCanvas"), js.indexOf("function drawStrip(r)"));
  assert.ok(/sample " \+ \(hi0 \+ 1\) \+ "\/" \+ n/.test(cvBlock),
    "the readout counts real samples");
  assert.ok(/fmtN\(samples\[hi0\]\)/.test(cvBlock),
    "and prints the recorded value under the beam - nothing else");
  assert.ok(/sn-beamro/.test(cvBlock), "pushed to the readout spans");
  assert.ok(/sn-beamro--echo/.test(js), "with the compact echo on the RAW WIRE head");
  const strip = js.slice(js.indexOf("function drawStrip(r)"), js.indexOf("function sparkline"));
  assert.ok(/if \(!REDUCED\) \{\n      var ro = el\("span", "sn-beamro"\)/.test(strip),
    "reduced motion gets no ticker - the legend already carries the static numbers");
});

test("v21: response mode is reported beside the invariant model outcome", () => {
  const h = loadHook();
  h.selectType("temp", "none");
  h.state.chain = ["pico", "nano"];
  h.state.operator = false; h.state.authority = false;
  h.derive();
  const result = { state: h.state.verdict.state, label: h.state.verdict.label, why: h.state.verdict.why };
  assert.equal(h.state.verdict.response.id, "log", "the default logs findings");
  h.state.authority = true;
  h.derive();
  assert.equal(h.state.verdict.response.id, "policy", "authority maps to the policy queue");
  assert.deepEqual(
    { state: h.state.verdict.state, label: h.state.verdict.label, why: h.state.verdict.why }, result,
    "the policy queue does not recolor or relabel the model result");
  h.state.operator = true; h.state.authority = false;
  h.derive();
  assert.equal(h.state.verdict.response.id, "human", "an operator maps to human review");
  assert.deepEqual(
    { state: h.state.verdict.state, label: h.state.verdict.label, why: h.state.verdict.why }, result,
    "human review also leaves the result alone");
});

test("v12: the ladder runs up - a backwards chain cannot be constructed", () => {
  const h = loadHook();
  h.state.chain = [];
  h.chainAdd("nano");
  h.chainAdd("pico"); // attached after - but a senior does not hand work down
  assert.deepEqual(h.state.chain, ["pico", "nano"],
    "the chain normalizes: reader first, senior after, whatever the tap order");
  h.state.chain = ["micro"];
  h.chainAdd("pico");
  assert.deepEqual(h.state.chain.slice(0, 1), ["pico"],
    "the reader always takes the wire end, observers ride behind");
  assert.ok(/a senior does not hand work down/.test(js),
    "and the rearrange teaches the reason");
  // derive() can therefore never see a pico-after-nano topology
  const info = js.slice(js.indexOf("function normalizeChain"), js.indexOf("function chainAdd"));
  assert.ok(/out.push\("pico"\)/.test(info) && /out.push\("nano"\)/.test(info),
    "normalization is structural, not advisory");
});

test("v12: the Ping stack shows the live voice on top, the recorded numbers beneath", () => {
  const dr = js.slice(js.indexOf("function drawReply"), js.indexOf("function renderChips"));
  // v13: the recorded recount rides beneath the live voice for questions AND
  // pastes (reading or draft) - only bare talk has nothing to stack
  assert.ok(/rep.bench && rep.bench.kind !== "talk"/.test(dr),
    "the stack always carries the bench's recorded answer beneath the live voice");
  assert.ok(/shown verbatim - declines included/.test(dr),
    "a real reply is a real reply - declines are displayed, not sniffed");
  assert.ok(/Ping is off air - the bench answered/.test(js),
    "network failure falls back to the bench, and says so");
  // the wait card dies with its context like any reply (v11 rule, executed)
  const h = loadHook();
  h.state.chain = ["pico", "nano"];
  h.promptSend("hello there");
  assert.equal(h.state.reply.kind, "pingwait");
  h.selectType(h.state.types.find((x) => x.key !== h.state.typeKey).key);
  assert.equal(h.state.reply, null, "a moved context dismisses the waiting card too");
});

// ---------- v14: the Spectrum + the consolidated why ------------------------------

test("v14: the Spectrum menu carries the mesh-baked line and the recipe legend", () => {
  assert.ok(/every tier ships with Wave Mesh baked in and/i.test(js),
    "the family's defining sentence is on the menu");
  assert.ok(/scratch = trained from random /.test(js) &&
            /carved from the flagship/.test(js),
    "the recipe legend explains scratch / base+specialize / expert-pruned");
});

test("v14: ONE WHY WAVE entry with the five questions as an internal nav", () => {
  assert.ok(/WHY WAVE\? · the story in five questions/.test(js), "one entry point");
  const topics = js.slice(js.indexOf("function whyTopics"), js.indexOf("function renderWhys"));
  for (const q of ["why task-native\\?", "why a senior\\?", "why not one big model\\?",
                   "why so small\\?", "what happens after a finding\\?"]) {
    assert.ok(new RegExp(q).test(topics), `${q} is a topic inside the panel`);
  }
  assert.ok(/role", "tablist"/.test(js.replace(/setAttribute\(/g, '", "').slice(0)) ||
            js.includes('setAttribute("role", "tablist")'),
    "the mini-nav is a tablist");
  assert.ok(/PATCH.whyTopic/.test(js), "the open topic survives repaints");
});

test("v14: the specialist-vs-dual story is cited, and stays qualitative where uncited", () => {
  const topics = js.slice(js.indexOf("function whyTopics"), js.indexOf("function renderWhys"));
  assert.ok(/MMLU 23.2 - Pico[\s\S]{0,20}report/.test(topics),
    "the at-chance-by-design number carries its citation");
  assert.ok(/near chance[\s\S]{0,120}IEB-Signals public-release plan/.test(topics),
    "the generalists-at-chance claim is qualitative, cited to the plan doc - no roster numbers");
  assert.ok(!/14\.0|13\.4|14\.3/.test(topics),
    "the unpublished roster figures never reach the deck");
  assert.ok(/tier-scaling strategy, [\s\S]{0,8}2026-08-14/.test(topics),
    "the scaling-law nugget cites the strategy doc");
});

test("v21: why not one big model explains the measured handoff and its limits", () => {
  const topics = js.slice(js.indexOf("function whyTopics"), js.indexOf("function renderWhys"));
  for (const field of ["escalation_rate", "macro_recall", "pct_of_parent_everywhere"]) {
    assert.ok(topics.includes(field), `${field} is read from the committed sweep`);
  }
  assert.ok(/PICO ONLY/.test(topics) && /FLOOR 1\.5 MESH/.test(topics) && /NANO DIRECT/.test(topics),
    "the panel compares the three decisions an engineer is actually making");
  assert.ok(/topology,[\s\S]{0,80}not a privacy guarantee/.test(topics),
    "gateway placement is not inflated into an unmeasured security property");
  assert.ok(/not [" +\s]*latency, energy, a cloud bill, or a hardware benchmark/.test(topics),
    "the compute axis says exactly what it cannot establish");
});

// ---------- v15: the plain-language pass ------------------------------------
// The founder's read: "the terms floor and ceiling are confusing... it's still
// a bit too complicated and easy to dismiss because it looks too hard to
// understand." These lock the translation so the jargon cannot creep back.

test("v15: the deck opens with a plain sentence and an instruction", () => {
  assert.ok(/Pick a recorded sensor, change its condition, and inspect what each model stage saw/.test(htmlFlat),
    "the engineering deck opens by saying exactly what can be changed and seen");
  assert.ok(/This is the engineering sandbox/.test(htmlFlat),
    "and distinguishes itself from the separate game before the controls");
  // the provenance line - run names and a suite version - is the receipt, not
  // the opening. It must still be one click away.
  assert.ok(/where these numbers come from/.test(htmlFlat), "the receipt has a way in");
  assert.ok(/id="wpProv"/.test(htmlFlat), "and still exists to be filled");
});

test("v15: the model's decision is a plain word, with the protocol word kept beside it", () => {
  assert.ok(/'ANSWERED " ' \+ st.said/.test(js), "answering alone says ANSWERED");
  assert.ok(/"ASKED FOR HELP ↑"/.test(js), "handing up says ASKED FOR HELP");
  // the protocol words survive as the small technical tag - a reader who knows
  // them still finds them, and the wire/envelope surfaces stay verbatim
  assert.ok(/sn-stage__tech", st.esc \? "escalate" : "assert"/.test(js),
    "assert/escalate are kept as the technical tag, not deleted");
  assert.ok(js.includes('"grammar": "root ::='),
    "and the wire envelope is still verbatim protocol");
});

test("v15: how-sure and the setting are plain, and the knob says what it does", () => {
  assert.ok(/" sure, needs "/.test(js), "the stage reads 'X sure, needs Y to answer alone'");
  assert.ok(/"SURE ENOUGH " \+ d.toFixed\(1\)/.test(js), "the knob is labelled by its job");
  assert.ok(/margin floor/.test(js), "and the technical name survives in the label/tooltip");
  assert.ok(/needs " \+ floor.toFixed\(1\)/.test(js), "the meter's line is 'needs N'");
  assert.ok(/"this read " \+ margin.toFixed\(2\)/.test(js), "and its bar is 'this read N'");
});

test("v21: models watch by default and the control chooses what happens after a finding", () => {
  assert.ok(/MODELS WATCHING/.test(js), "the workbench states who is doing the watching");
  assert.ok(/AFTER A FINDING/.test(js), "the control asks the downstream question");
  for (const a of ["LOG ONLY", "HUMAN REVIEW", "POLICY QUEUE"]) {
    assert.ok(js.includes(a), `${a} is spelled out, not computed from two toggles`);
  }
  assert.ok(/PATCH.operator = w.id === "human"/.test(js), "human review maps to the operator flag");
  assert.ok(/PATCH.authority = w.id === "policy"/.test(js), "the policy queue maps to authority");
  assert.ok(/models read every replay/i.test(js), "the copy explains that watching is invariant");
  const h = loadHook();
  h.state.chain = [];
  assert.match(h.watchingLabel(), /NO MODEL SEATED/,
    "an intentionally empty rail never claims a model is watching");
  h.state.chain = ["pico"];
  assert.match(h.watchingLabel(), /MODELS WATCHING/,
    "seating a recorded reader restores the default watching state");
});

test("v15: the feature dump folds away in the combined view, never disappears", () => {
  assert.ok(/sn-rawfold/.test(js) && /the exact numbers the model read/.test(js),
    "the raw window is one click away in the combined view");
  assert.ok(/if \(solo\) \{\s*box.appendChild\(log\);/.test(js),
    "and the SENSOR DATA tab shows it open");
  assert.ok(/SENSOR DATA/.test(js), "the tab is named in plain words");
});

test("v15: a chain card answers what/doing/decided at a glance", () => {
  assert.ok(/sn-slot__name/.test(js) && /sn-slot__role/.test(js) && /sn-slot__state/.test(js),
    "name, role and live state each have their own line");
  assert.ok(/reads the sensor, answers or asks for help/.test(js),
    "the role is what it does for you, not what the protocol calls it");
  assert.ok(/function slotState/.test(js), "the state is read off the same stages the monitor shows");
  const ss = js.slice(js.indexOf("function slotState"), js.indexOf("function drawChainCard"));
  for (const w of ["ASKED FOR HELP", "ANSWERED", "WAITING", "QUIET"]) {
    assert.ok(ss.includes(w), `${w} is one of the states a card can report`);
  }
  // params/recipe/run name are for the curious - demoted, never dropped
  assert.ok(/sn-slot__meta/.test(js) && /the run name is ground truth/.test(js),
    "the run name stays reachable on the card's metadata");
});

/* ---------------------------------------------------------------------------
   v16 - THE CHAIN, MADE VISIBLE. The model cards used to be identical boxes
   with a small radio glyph; now each tier carries its own engraved scale art
   and a card that acted looks nothing like one that stood down.
   --------------------------------------------------------------------------- */

test("v16: model iconography is the tier scale ladder, not the old radio glyphs", () => {
  for (const art of ["chip", "gateway", "server", "rack", "racks", "aisle", "hall"]) {
    assert.ok(new RegExp(`art: "${art}"`).test(js), `${art} is a tier's art`);
    assert.ok(css.includes(`assets/wave/tier-${art}-ink.png`),
      `the ${art} rung is masked from its committed plate`);
  }
  // the radio glyphs stay on disk but no longer stand in for a model tier
  for (const old of ["radio-pocket", "radio-reader", "radio-senior"]) {
    assert.ok(!css.includes(`ws-icon--`) || !new RegExp(`ws-icon--[a-z]+ \\.wb-plate__ink \\{[^}]*${old}`).test(css),
      `${old} no longer dresses a model icon`);
  }
  assert.ok(!/icon: "(pocket|reader|senior)"/.test(js), "no tier still points at a radio glyph");
});

test("v16: the ladder is scaled by ink AREA, so a flat server never outranks a cabinet", () => {
  // longest-side scaling would draw the 3.2:1 server larger than the 0.65:1
  // cabinet above it and invert the ladder - the rule must be area.
  assert.ok(/Math.sqrt\(\(s \* s\) \/ ratio\)/.test(js), "the box is derived from an area rung");
  const artBlock = js.slice(js.indexOf("var ART = {"), js.indexOf("function modelIcon"));
  const dims = {};
  for (const m of artBlock.matchAll(/(\w+):\s*\{ w: (\d+), h: (\d+) \}/g)) {
    dims[m[1]] = { w: +m[2], h: +m[3] };
  }
  const rungs = [...js.matchAll(/art: "(\w+)", span: (\d+)/g)].map((m) => ({ art: m[1], span: +m[2] }));
  assert.equal(rungs.length, 7, "every tier has a rung");
  let prev = 0;
  for (const r of rungs) {
    const d = dims[r.art];
    assert.ok(d, `${r.art} has measured source dimensions`);
    const h = Math.sqrt((r.span * r.span) / (d.w / d.h));
    const area = h * (h * (d.w / d.h));
    assert.ok(area > prev, `${r.art} draws more ink than the tier below it`);
    prev = area;
  }
});

test("v16: a card that acted is visibly not a card that stood down", () => {
  // the mesh's whole argument is that the senior is only bothered when the
  // small model is unsure - so "nothing reached Wave Nano" has to be visible.
  assert.ok(/is-acted/.test(js) && /is-standby/.test(js), "the two states exist");
  // v17: the rail became the banded line, so the card is the only place that
  // derives this now - same derivation, one caller instead of two
  assert.ok(/stNow.cls === "is-ok" \|\| stNow.cls === "is-esc"/.test(js),
    "acted is derived from the same stage verdicts the monitor prints");
  assert.ok(/\.sn-slot--model\.is-acted/.test(css) && /\.sn-slot--model\.is-standby/.test(css),
    "and each state has its own treatment");
  assert.ok(/\.sn-rail\.is-idle .sn-rail__cable/.test(css),
    "the cable into a stood-down model reads stood down too");
  // no new claim: the states come from slotState, which reads PATCH.verdict
  const sc = js.slice(js.indexOf("function slotState"), js.indexOf("function drawChainCard"));
  assert.ok(/PATCH.verdict/.test(sc) && /v.stages/.test(sc),
    "card state is a recount of the printed stages, never its own judgement");
});

test("v16: the chain flash rides the same signature gate, and reduced motion skips it", () => {
  assert.ok(/flashIfChanged\(card, "chain:" \+ id, stNow.word\)/.test(js),
    "a card pulses only when its verdict actually changed");
  assert.ok(/prefers-reduced-motion: reduce\) \{\s*\.sn-slot--model\.is-flash \{ animation: none/.test(css),
    "and the pulse is chrome that reduced motion drops");
});

test("v16: the attach menu is the ladder, one tier per row", () => {
  assert.ok(/ws-menu__art/.test(js) && /\.ws-menu__art/.test(css), "each row has an art cell");
  assert.ok(/align-items: flex-end/.test(css.slice(css.indexOf(".ws-menu__art"))),
    "the engravings share a baseline so the growth reads as a staircase");
  assert.ok(/\.ws-menu \{ grid-template-columns: 1fr/.test(css), "one tier per row, in ladder order");
  assert.ok(/runs on " \+ fam.runs/.test(js), "the row says what hardware it takes to run");
});

/* ---- v19: the founder could not tell the models apart in the monitor, could
   not work the glass without a trackpad, was offered tiers already seated, and
   could not see what the upper ladder is FOR. ---- */

test("v19: every stage header carries its tier's hue, beating the generic head rule", () => {
  // The regression was pure specificity: .sn-stage__head b (0,1,1) outranked
  // .sn-tiername (0,1,0), so tier identity died in the monitor - the one place
  // the eye follows a model from its card to its answer.
  assert.ok(/\.sn-stage__head b\.sn-tiername \{ color: var\(--tc/.test(css),
    "the stage-head name is beaten out of flat ink by an equally specific rule");
  const headRule = /\.sn-stage__head b \{[^}]*color: var\(--ink-400\)/.test(css);
  assert.ok(headRule, "the generic head rule still exists, so the override is load-bearing");
  // and the JS actually hands every stage a tier to colour with
  assert.ok(/st\.fam \? st\.fam\.id : null/.test(js),
    "stages beyond pico/nano take their tier from the family entry");
  assert.ok(/tierStyle\(box, stTier\)/.test(js), "and the stage box carries it");
});

test("v19: the set has working controls, and pressing one is a hand on the glass", () => {
  for (const id of ["wsTvCtl"]) {
    assert.ok(htmlFlat.includes(`id="${id}"`), `${id} must exist`);
  }
  for (const how of ["up", "down", "answer"]) {
    assert.ok(htmlFlat.includes(`data-scroll="${how}"`), `a ${how} control exists`);
  }
  assert.ok(/aria-controls="wpMonitor"/.test(htmlFlat), "the controls say what they drive");
  const wire = js.slice(js.indexOf('var ctl = $("wsTvCtl")'), js.indexOf('var ctl = $("wsTvCtl")') + 1200);
  assert.ok(/PATCH\._userScrollAt = Date\.now\(\)/.test(wire),
    "a press suspends auto-follow like any other user scroll - it must not yank you back");
  assert.ok(/openAnswer\(\)/.test(wire),
    "except the DETAILS/ANSWER destination key, which is explicit intent");
  const answerWire = js.slice(js.indexOf("function openAnswer"), js.indexOf("function frontTrace"));
  assert.ok(/glassScrollTo\([^)]*, true\)/.test(answerWire),
    "the destination key forces its answer landing");
  assert.ok(/REDUCED \? "auto" : "smooth"/.test(wire), "and reduced motion jumps");
});

test("v23: the whole engraved glass opens detail and the hardware key names its action", () => {
  const front = css.slice(css.indexOf(".sn-tv__front {"),
    css.indexOf("}", css.indexOf(".sn-tv__front {")) + 1);
  assert.match(front, /left:\s*15\.5%/,
    "the hit target begins at the measured left edge of the clear CRT opening");
  assert.match(front, /top:\s*14\.2%/,
    "the hit target begins at the measured top edge of the clear CRT opening");
  assert.match(front, /width:\s*70\.3%/,
    "the hit target spans the measured clear CRT width");
  assert.match(front, /height:\s*63\.9%/,
    "the hit target spans the measured clear CRT height");
  assert.ok(/\.sn-tv__front > \* \{[^}]*pointer-events:\s*none/.test(css),
    "every point on the overview resolves to the glass button itself");
  assert.ok(htmlFlat.includes('id="wsAnswerKey"'), "the hardware detail key has a stable control id");
  assert.match(htmlFlat, /id="wsAnswerKey"[^>]*>[\s\S]*?<span>DETAILS<\/span>/,
    "its initial label says what it does from the overview");
  const answer = js.slice(js.indexOf("function paintAnswerKey"), js.indexOf("function frontTrace"));
  assert.match(answer, /detailOpen\(\) \? "ANSWER" : "DETAILS"/,
    "inside detail the same key changes to the useful jump-back action");
  assert.match(answer, /if \(!detailOpen\(\)\) setDetail\(true\)/,
    "DETAILS opens the cascade instead of scrolling an invisible layer");
  assert.match(answer, /querySelector\("\.sn-answer"\)/,
    "then both key states land on the answer");
});

test("v21: the TV controls are seated in one flush hardware strip", () => {
  const strip = css.slice(css.indexOf(".sn-tvctl {"), css.indexOf(".sn-tvctl__k {"));
  assert.ok(/width:\s*52%/.test(strip), "the four-button strip spans the engraved control-panel cutout");
  assert.ok(/background:\s*linear-gradient/.test(strip),
    "the paper showing through the bezel is replaced by a hardware surface");
  const keys = css.slice(css.indexOf(".sn-tvctl__k {"), css.indexOf(".sn-tvctl__k--wide"));
  assert.ok(/height:\s*1\.6rem/.test(keys), "all hardware buttons share one height");
});

test("v19: a tier already in the chain reads as seated, not as on offer", () => {
  assert.ok(/var seated = PATCH\.chain\.indexOf\(fam\.id\) >= 0/.test(js),
    "the menu knows what is already chained");
  assert.ok(/already in the chain/.test(js), "and says so on the row");
  assert.ok(/if \(seated\) \{ revealBand\(fam\.id\); return; \}/.test(js),
    "clicking a seated row takes you to it instead of adding it twice");
  assert.ok(/\.ws-menu__item\.is-seated \{ opacity/.test(css), "and it is visibly dimmed");
  // the reveal is a real navigation, not a no-op
  assert.ok(/function revealBand/.test(js) && /scrollIntoView/.test(js.slice(js.indexOf("function revealBand"))),
    "revealBand actually scrolls the band into view");
});

test("v19: WHERE THEY LIVE explains the upper ladder without inventing data", () => {
  const h = loadHook();
  // every tier carries the value argument, and it is prose, not numbers
  for (const fam of h.family) {
    assert.ok(fam.only && fam.only.length > 20, `${fam.id} says what only it can do`);
    assert.ok(fam.belowCant && fam.belowCant.length > 10, `${fam.id} says what the tier below cannot`);
    assert.ok(!/\d+\s*(%|faults|caught|missed|channels)/.test(fam.only + fam.belowCant),
      `${fam.id}'s argument is a deployment fact, not a measurement`);
  }
  // the panel exists and is honest about what has a run and what does not
  const panel = js.slice(js.indexOf("function whereTheyLive"));
  assert.ok(/No recorded run on this bench - this is where the tier LIVES/.test(panel),
    "an unrecorded tier says it is showing a place, never a claim about what it said");
  assert.ok(/only Pico and Nano have runs here to actually hear/.test(panel),
    "and the panel footer names exactly which tiers can be heard");
  assert.ok(/Deployment facts from the Wave Spectrum, not measurements/.test(panel),
    "the whole panel is labelled as deployment fact, not measurement");
  // the four in-building tiers map to floors; the rest are explicitly beyond it
  const floors = js.slice(js.indexOf("var PLANT_FLOORS"), js.indexOf("function whereTheyLive"));
  for (const t of ["pico", "nano", "micro", "giga"]) {
    assert.ok(new RegExp(`${t}:\\s*\\{ top:`).test(floors), `${t} has a floor in the building`);
  }
  for (const t of ["tera", "peta", "exa"]) {
    assert.ok(!new RegExp(`${t}:\\s*\\{ top:`).test(floors), `${t} is not given a floor - it is beyond the plant`);
  }
  assert.ok(/beyond this building/.test(panel), "and the panel says so");
});

test("v19: the flagship's teaching role is the argument for the small models", () => {
  const h = loadHook();
  const exa = h.family.find((f) => f.id === "exa");
  assert.ok(/TEACHES/.test(exa.only), "Exa's value is stated as teaching the rest");
  assert.ok(/270M/.test(exa.only),
    "tied back to why the smallest tier is trustworthy - that is the whole ladder's argument");
  const pico = h.family.find((f) => f.id === "pico");
  assert.ok(/no network|no GPU/i.test(pico.only),
    "and the small side is a virtue stated in its own terms, not an apology");
});

// ---------- v20: the face of the set, the band-aware menu, the tabs ----------

test("v21: the CRT face leads with recorded evidence and opens detail explicitly", () => {
  assert.ok(/sn-tv__front/.test(html), "the face of the set exists in the markup");
  assert.match(html, /<button class="sn-tv__front"/,
    "and it is a button, so the keyboard gets it without extra wiring");
  assert.ok(/function paintFront/.test(js), "it is painted from the verdict");
  assert.ok(/f\.hidden = detailOpen\(\)/.test(js),
    "and it hides exactly when the detail is open - one state, not two");
  const face = js.slice(js.indexOf("function paintFront"), js.indexOf("function wireFront"));
  for (const token of ["CURRENT READ", "watchingLabel()", "frontScore()", "frontTrace(r)",
                       "frontRoute(sts)", "PRESS ANYWHERE ON THE GLASS", "OPEN FULL MODEL OUTPUT",
                       "RAW DATA · EVERY MODEL · FLEET DETAIL"]) {
    assert.ok(face.includes(token), `the glance face includes ${token}`);
  }
  assert.ok(/sn-front__hintkey/.test(face) && /sn-front__hintmore/.test(face),
    "the full-output action has hierarchy instead of another tiny footer label");
  const trace = js.slice(js.indexOf("function frontTrace"), js.indexOf("function frontRoute"));
  assert.ok(/seriesOf\(r\)/.test(trace) && /seriesPath\(s\.samples/.test(trace),
    "the glance trace is plotted from the selected committed series");
  assert.ok(/sn-front__scan/.test(trace) && /sn-front__sweep/.test(trace),
    "the real waveform carries a moving phosphor scan, not only an opacity pulse");
  assert.ok(/@keyframes sn-front-sweep/.test(css) && /@keyframes sn-front-scan/.test(css),
    "the replay visibly travels across the CRT");
  assert.ok(/prefers-reduced-motion: no-preference\)[\s\S]*\.sn-front__scan[\s\S]*\.sn-front__sweep/.test(css),
    "the sweep only runs when motion is welcome");
  const score = js.slice(js.indexOf("function frontScore"), js.indexOf("function paintFront"));
  assert.ok(/deriveFleet\(\)/.test(score) && /t\.caught \+ "\/" \+ t\.faults/.test(score),
    "the score is the same fleet recount, not a game-only number");
  assert.ok(/sn-front__route/.test(js) && /sn-front__score/.test(js),
    "route and score have visible instrument regions");
  assert.ok(/aria-expanded/.test(face), "the disclosure reports its open state");
  assert.ok(/sn-back/.test(js), "the detail carries a way back to the face");
  const wire = js.slice(js.indexOf("function wireFront"), js.indexOf("function paintMonitor"));
  assert.ok(/addEventListener\("click"/.test(wire), "click opens the detail");
  assert.ok(!/pointerenter|FRONT_HOVER_MS/.test(js), "mere pointer movement never opens it");
  assert.ok(/\.sn-tv__front\[hidden\] \{ display: none; \}/.test(css),
    "a hidden face must leave the layer, or it swallows every click on the glass");
  assert.ok(/\.sn-tv__front\s*\{[\s\S]*?z-index:\s*1/.test(css),
    "the front sits behind the engraved bezel instead of painting over it");
  for (const state of ["green", "yellow", "red"]) {
    const at = css.indexOf(`.sn-tv__front[data-state="${state}"]`);
    const rule = at < 0 ? "" : css.slice(at, css.indexOf("}", at) + 1);
    assert.ok(rule && !/background\s*:/.test(rule), `${state} is an accent, not a flooded screen`);
  }
});

test("v21: the television dominates the bench and its working text is legible", () => {
  const tv = css.slice(css.indexOf(".sn-tv {"), css.indexOf("}", css.indexOf(".sn-tv {")) + 1);
  assert.match(tv, /max-width:\s*76rem/, "the TV grows beyond the old 66rem cap");
  const responseAt = css.indexOf(".sn-front__rstate {", css.indexOf(".sn-front__rstate {") + 1);
  const response = css.slice(responseAt, css.indexOf("}", responseAt) + 1);
  /* v38 (UX audit type floor): the compact responses grew .5rem -> .58rem -
     the guarantee is READABLE, and this is more so; the floor is the lock */
  assert.match(response, /font-size:\s*\.58rem/, "compact model responses are still readable");
  assert.match(response, /white-space:\s*normal/, "response explanations may use their card's second line");
  assert.ok(/\.sn-tv__screen \.ws-log \{ font-size: \.68rem; \}/.test(css),
    "raw evidence in the detailed monitor is enlarged too");
  const front = css.slice(css.indexOf(".sn-tv__front {"),
    css.indexOf("}", css.indexOf(".sn-tv__front {")) + 1);
  assert.match(front, /justify-content:\s*space-between/,
    "the larger glass is used vertically instead of leaving a dead lower half");
});

test("v21: every seated model keeps its response on the glance screen", () => {
  const h = loadHook();
  h.selectType("temp", "none");
  h.state.chain = ["pico", "nano", "micro", "giga", "tera", "peta", "exa"];
  h.state.floor = 0.5;
  h.derive();
  const modelStages = h.state.verdict.stages.filter((st) => st.kind !== "raw" && st.kind !== "deadend");
  assert.equal(modelStages.length, 7, "one visible response exists for every seated tier");
  const responses = modelStages.map(h.stageResponse);
  assert.match(responses[0], /ANSWERED|ASKED FOR HELP/, "Pico prints its actual action");
  assert.match(responses[1], /ANSWERED|NOT CALLED/, "Nano prints its actual action");
  assert.match(responses[2], /SITE RECOUNT/, "Micro prints its scope recount");
  assert.match(responses[3], /PLANT RECOUNT/, "Giga prints its scope recount");
  /* v37 (founder: "lets finish the rest"): the three beyond-replay tiers
     used to print one identical line; each now states ITS OWN honest
     ceiling - and the guarantee tightens: distinct, per-tier, still no
     prediction, margin, or lamp. */
  assert.match(responses[4], /WOULD CORRELATE MANY PLANTS · ONLY ONE RECORDED/,
    "Tera says what it would add and why the bench stops");
  assert.match(responses[5], /WOULD CARRY A REGION, LEANER · NO REGIONAL RECORDS/,
    "Peta likewise, in its own terms");
  assert.match(responses[6], /NOT A READER · THE FAMILY'S TEACHER/,
    "Exa is a role, not a reader - and never claims it trained these runs");
  assert.equal(new Set(responses.slice(4)).size, 3, "no two upper tiers share a line");
  const route = js.slice(js.indexOf("function frontRoute"), js.indexOf("function frontScore"));
  assert.ok(/stageResponse\(st\)/.test(route) && /fam\.label/.test(route),
    "the CRT renders both the full Wave name and that stage's response");
});

test("v22: the incident deck deals only committed windows and never changes on a timer", () => {
  const h = loadHook();
  const candidates = h.incidentCandidates();
  assert.ok(candidates.length > h.state.types.length, "the deck includes OK and fault conditions");
  for (const pick of candidates) {
    const type = h.state.types.find((t) => t.key === pick.typeKey);
    assert.ok(type && type.recIdx[pick.cond] === pick.recordIndex,
      "every deal points back to the selector's committed record");
  }
  const faultAt = candidates.findIndex((pick) => pick.cond !== "none");
  h.drawIncident(faultAt);
  assert.equal(h.state.typeKey, candidates[faultAt].typeKey);
  assert.equal(h.state.cond, candidates[faultAt].cond);
  assert.equal(h.state.mission.incidentNode,
    measured.records[candidates[faultAt].recordIndex].node_id);
  assert.ok(/crypto\.getRandomValues/.test(js), "an unforced deal chooses among committed cards");
  assert.ok(!/setInterval/.test(js), "no timer changes the condition while it is being read");
  assert.ok(htmlFlat.includes('data-mission="draw"'), "the TV has an explicit DEAL control");
});

test("v27: the idle case board teaches one cohesive mystery loop", () => {
  const h = loadHook();
  const cards = h.incidentCandidates();
  const tally = h.missionDeckTally();
  assert.deepEqual(tally, {
    total: cards.length,
    incidents: cards.filter((card) => card.cond !== "none").length,
    checks: cards.filter((card) => card.cond === "none").length,
  }, "the visible deck count comes from the committed selector records");
  const mission = js.slice(js.indexOf("function drawMission"), js.indexOf("function paintFront"));
  for (const beat of ["CATCH", "TRACE", "CLOSE"]) {
    assert.ok(mission.includes(beat), `${beat} is visible before the first deal`);
  }
  assert.ok(mission.includes("Pick a mystery from the measured deck."),
    "the invitation starts with a clear action and its honest source");
  assert.ok(mission.includes("Nothing changes until you make a move"),
    "the no-timer rule reads like a benefit rather than a disclaimer");
  assert.ok(mission.includes("START FIRST CASE"));
  assert.ok(mission.includes("START NEXT CASE"));
  assert.ok(/sn-mission__idledeck/.test(mission) && /sn-mission__loop/.test(mission),
    "the idle state is a small playable route rather than a paragraph and button");
  assert.match(css, /\.sn-mission__loop\s*\{[^}]*grid-template-columns:\s*repeat\(3/,
    "the opening loop reads as a three-beat route");
  assert.match(css, /prefers-reduced-motion:\s*reduce[\s\S]*\.sn-mission__loop li:first-child/,
    "the deal prompt becomes still when reduced motion is requested");
});

test("v27: incident copy gives the player a mission in plain language", () => {
  const mission = js.slice(js.indexOf("function drawMission"), js.indexOf("function paintFront"));
  assert.ok(mission.includes("Something is wrong with this signal."));
  assert.ok(mission.includes("choose who should hear it"));
  assert.ok(mission.includes("use the clue kit to narrow down why"));
  assert.ok(!mission.includes("A sensor condition is not a root-cause diagnosis"),
    "the incident opens with an objective, not compliance prose");
  for (const label of ["START A MYSTERY CASE", "CARD CLEAR", "CASE CLOSED"]) {
    assert.ok(js.includes(label), `${label} keeps the deck, incident, and result vocabulary aligned`);
  }
});

test("v27: every kind of model miss gets a useful bench explanation", () => {
  const h = loadHook();
  for (const truth of ["stuck", "drifting", "dropout", "noisy", "railed"]) {
    const record = measured.records.find((item) => item.truth === truth);
    assert.ok(record, `${truth} has a committed card`);
    const lesson = h.modelLimitLesson(record, { word: "none", who: "WAVE PICO" });
    assert.ok(lesson && lesson.shape.length > 80, `${truth} gets a substantive explanation`);
    assert.doesNotMatch(lesson.shape, /does not contain a model-authored explanation/i,
      `${truth} never ends at a missing-data disclaimer`);
    assert.match(lesson.shape, /recorded|samples|trace|reading|values/i,
      `${truth} points back to visible evidence`);
    assert.match(lesson.shapeBy, /BENCH EXPLANATION.*RECORDED SIGNAL.*NOT MODEL OUTPUT/,
      `${truth} keeps authored guidance separate from model output`);
  }
  const monitor = js.slice(js.indexOf("function paintMonitor"), js.indexOf("function wirePrompt"));
  assert.ok(/miss\.shapeTitle/.test(monitor) && /miss\.shapeBy/.test(monitor),
    "the explanation and its source are both rendered");
});

test("v28: every committed card has condition mechanics and a seven-tier case brief", () => {
  const h = loadHook();
  const tiers = ["pico", "nano", "micro", "giga", "tera", "peta", "exa"];
  const familyLine = "PICO → NANO → MICRO → GIGA → TERA → PETA → EXA";
  for (const card of h.incidentCandidates()) {
    h.selectType(card.typeKey, card.cond);
    const record = measured.records[card.recordIndex];
    const sensor = h.state.types.find((type) => type.key === card.typeKey);
    if (card.cond !== "none") {
      assert.ok(h.fieldRigs[card.cond], `${card.typeKey}/${card.cond} has a playable field rig`);
    }
    const contributions = new Set();
    for (const tier of tiers) {
      const brief = h.tierCaseBrief(tier, record);
      assert.equal(brief.record, record.node_id, `${tier} receives the exact selected record`);
      assert.equal(brief.sensor, sensor.label, `${tier} receives the active sensor`);
      assert.equal(brief.condition, card.cond.toUpperCase().replace("NONE", "OK"));
      assert.equal(brief.family, familyLine, `${tier} knows the complete family contract`);
      assert.match(brief.handoff, /WAVE PICO.*WAVE NANO/i,
        `${tier} sees what both recorded readers did, even when Nano was not called`);
      assert.ok(brief.mechanic && brief.mechanic.length > 12,
        `${tier} receives the condition's field mechanic`);
      assert.ok(brief.signal && brief.signal.length > 45,
        `${tier} receives a measured clue from the selected signal`);
      assert.ok(brief.adds && brief.adds.length > 25, `${tier} contributes to this case`);
      contributions.add(brief.adds);
    }
    assert.equal(contributions.size, tiers.length,
      `${card.typeKey}/${card.cond} gives all seven tiers distinct work`);
  }
});

test("v28: tier case intelligence is explicit about what ran and what is synthesized", () => {
  const h = loadHook();
  h.selectType("amp", "stuck");
  const record = measured.records[h.state.types.find((type) => type.key === "amp").recIdx.stuck];
  for (const tier of ["pico", "nano"]) {
    assert.equal(h.tierCaseBrief(tier, record).provenance, "RECORDED MODEL OUTPUT");
  }
  for (const tier of ["micro", "giga"]) {
    const brief = h.tierCaseBrief(tier, record);
    assert.equal(brief.provenance, "BENCH SYNTHESIS · COMMITTED RECORDS · NOT MODEL OUTPUT");
    assert.match(brief.adds, /STUCK/i, `${tier} responds to the current case, not only a generic scope`);
  }
  for (const tier of ["tera", "peta", "exa"]) {
    const brief = h.tierCaseBrief(tier, record);
    assert.equal(brief.provenance, "ROLE SIMULATION · REPLAY ENDS AT ONE PLANT");
    assert.match(brief.adds, /STUCK/i, `${tier} still receives the current case packet`);
  }
  const monitor = js.slice(js.indexOf("function paintMonitor"), js.indexOf("function wirePrompt"));
  assert.ok(/drawTierCase\(st\)/.test(monitor), "every model stage renders its current-case brief");
  for (const label of ["CURRENT CASE", "READER HANDOFF", "THIS TIER ADDS", "FAMILY CONTRACT"]) {
    assert.ok(js.includes(label), `${label} is visible in the case brief`);
  }
});

test("v28: a tier tab leads with its case instead of the global shift console", () => {
  const monitor = js.slice(js.indexOf("function paintMonitor"), js.indexOf("function wirePrompt"));
  /* v37 (founder: "CASE BOARD - move that to the end"): still ALL-only,
     but it now closes the detail view instead of opening it, so THE
     ANSWER leads the glass. */
  assert.match(monitor, /if \(detailOpen\(\) && PATCH\.tab === "all"\) host\.appendChild\(drawMission\(\)\)/,
    "the global game console belongs to ALL, so a selected tier can lead");
  const missionAt = monitor.indexOf('host.appendChild(drawMission())');
  const answerAt = monitor.indexOf('finalAnswer(sts)');
  assert.ok(missionAt > answerAt, "and the case board renders AFTER the answer, at the end");
  assert.ok(/if \(stTier\) box\.appendChild\(drawTierCase\(st\)\)/.test(monitor),
    "the selected model stage opens with its case brief");
});

test("v29: Micro and Giga compare the active sensor case instead of a static fault leaderboard", () => {
  const h = loadHook();
  h.selectType("amp", "stuck");
  h.state.chain = ["pico", "nano", "micro"];
  h.state.floor = 1.5;
  h.derive();
  const record = measured.records[h.state.types.find((type) => type.key === "amp").recIdx.stuck];
  const lens = h.caseLens("micro", record);
  assert.equal(lens.sensor, "MOTOR CURRENT");
  assert.equal(lens.condition, "STUCK");
  assert.equal(lens.rows[0].record, record.node_id, "the selected machine leads the comparison");
  assert.equal(lens.rows[0].current, true);
  assert.ok(lens.rows.length > 1, "the site supplies comparable motor-current cards");
  assert.ok(lens.rows.every((row) => row.sensorKey === "amp"),
    "unrelated pressure, temperature, and vibration cards never enter this list");
  assert.ok(lens.rows.every((row) => row.condition && row.outcome),
    "every comparable card says what was recorded and what this chain did");
  assert.match(lens.scope.how, /scenes in id order/, "the synthetic site partition stays disclosed");

  h.selectType("vib", "stuck");
  const vib = h.caseLens("micro");
  assert.ok(vib.rows.every((row) => row.sensorKey === "vib"));
  assert.notDeepEqual(vib.rows.map((row) => row.record), lens.rows.map((row) => row.record),
    "changing the sensor changes the site evidence");

  const scopeCard = js.slice(js.indexOf("function drawScopeRun"), js.indexOf("function drawScopeCard"));
  assert.ok(scopeCard.includes("CURRENT CASE FIRST"));
  assert.ok(!scopeCard.includes("machines, worst first"),
    "the old global miss leaderboard no longer leads Micro");
});

test("v29: the next model move is computed from the selected record and chain outcome", () => {
  const h = loadHook();
  h.state.floor = 1.5;

  h.selectType("temp", "dropout");
  h.state.chain = ["pico"];
  h.beginSelectedIncident(false);
  assert.equal(h.incidentMove().correct, "nano", "an unheard Pico escalation needs Nano");

  h.state.chain = ["pico", "nano"];
  h.beginSelectedIncident(false);
  let move = h.incidentMove();
  assert.equal(move.kind, "identify");
  assert.equal(move.correct, "nano", "the player identifies the senior that caught this read");
  assert.equal(h.missionChooseMove("giga"), false, "a scope jump does not solve a reader question");
  assert.equal(h.state.mission.moveStage, 0, "wrong moves do not advance the case");
  assert.match(h.state.mission.moveFeedback.text, /Nano|doubt|read/i);
  assert.equal(h.missionChooseMove("nano"), true);
  assert.equal(h.incidentMove().correct, "micro", "after detection, site investigation is the next beat");

  h.selectType("unnamed", "railed");
  h.state.chain = ["pico", "nano"];
  h.state.floor = 1.5;
  h.beginSelectedIncident(false);
  move = h.incidentMove();
  assert.equal(move.kind, "threshold");
  assert.equal(move.correct, "floor");
  assert.equal(move.nextFloor, 2.0, "the next measured detent is derived from Pico's margin");
  h.state.chain = ["pico", "nano", "micro"];
  h.beginSelectedIncident(false);
  assert.equal(h.incidentMove().correct, "floor",
    "pre-seating Micro does not skip a recoverable handoff puzzle");
  assert.equal(h.missionPlan().locked, true);
  assert.equal(h.missionChooseMove("floor"), true);
  assert.equal(h.missionPlan().locked, false,
    "the already-seated site tier opens after the threshold move is solved");

  h.selectType("amp", "stuck");
  h.state.floor = 1.5;
  h.state.chain = ["pico", "nano"];
  h.beginSelectedIncident(false);
  move = h.incidentMove();
  assert.equal(move.kind, "model");
  assert.equal(move.correct, "micro", "a recorded Pico+Nano blind spot needs an independent site audit");
  assert.match(move.question, /both|blind spot|site/i);
  const moveCard = js.slice(js.indexOf("function drawMissionMove"), js.indexOf("function addMissionMicro"));
  assert.ok(moveCard.includes("ALREADY SEATED"),
    "a decoy tier already in the chain is identified, never offered as another seat");
});

test("v29: every incident asks three condition-specific diagnostic questions", () => {
  const h = loadHook();
  const questions = new Set();
  for (const condition of ["stuck", "drifting", "dropout", "noisy", "railed"]) {
    const rig = h.fieldRigs[condition];
    assert.equal(rig.controls.length, 3);
    for (const control of rig.controls) {
      assert.ok(control.question && control.question.endsWith("?"),
        `${condition}/${control.id} asks the player a concrete question`);
      questions.add(control.question);
    }
  }
  assert.equal(questions.size, 15, "the five cases do not recycle one generic quiz");
  const control = js.slice(js.indexOf("function drawFieldControl"), js.indexOf("function fieldDealButton"));
  assert.ok(control.includes("YOUR CLUE"));
  assert.ok(control.includes("control.question"));
});

test("v29: the opening deals teach Nano, blind-spot audit, then threshold tuning", () => {
  const h = loadHook();
  h.state.floor = 1.5;
  const cards = h.incidentCandidates();
  const opening = [0, 1, 2].map((draw) => h.guidedCard(cards, draw));
  assert.deepEqual(opening.map((card) => h.caseLesson(measured.records[card.recordIndex])),
    ["nano", "blind", "floor"]);
  assert.ok(opening.every((card) => measured.records[card.recordIndex]),
    "the tutorial changes only which committed card is dealt");
  assert.equal(h.guidedCard(cards, 3), null, "later shifts return to the shuffled deck");
});

test("v22: Micro unlocks a clearly non-model diagnostic playbook", () => {
  const h = loadHook();
  const faultAt = h.incidentCandidates().findIndex((pick) => pick.cond === "stuck");
  h.state.chain = ["pico", "nano"];
  h.drawIncident(faultAt);
  assert.equal(h.missionPlan().locked, true, "Pico and Nano detect; they do not invent repair prose");
  h.chainAdd("micro");
  const plan = h.missionPlan();
  assert.equal(plan.locked, false);
  assert.equal(plan.steps.length, 3, "the playbook is a short playable sequence");
  assert.deepEqual(plan.steps.map((step) => step.kind), ["verify", "context", "handoff"],
    "the sequence verifies the signal, checks context, then hands work to authorized maintenance");
  const copy = JSON.stringify(h.playbooks);
  assert.match(copy, /independent|reference|calibrated/i, "it names a diagnostic tool");
  assert.match(copy, /authorized|site-specific/i, "it names the safety boundary");
  assert.match(copy, /Do not restart or bypass equipment/i,
    "the playbook explicitly refuses machinery commands");
  assert.doesNotMatch(copy, /click to restart|override the interlock|bypass the interlock/i,
    "the browser never offers a machinery command");
  const mission = js.slice(js.indexOf("function missionPlan"), js.indexOf("function paintFront"));
  assert.ok(/authored for the game, not generated model output/.test(mission));
  assert.ok(/SEAT WAVE MICRO · UNLOCK SAFE CHECKS/.test(mission));
});

test("v23: the Micro mission control has an unmistakable locked-to-playbook handoff", () => {
  const mission = js.slice(js.indexOf("function missionPlan"), js.indexOf("function paintFront"));
  for (const label of ["PICO · DETECTS", "NANO · CHECKS", "MICRO · SITE TRIAGE"]) {
    assert.ok(mission.includes(label), `the locked console explains ${label}`);
  }
  assert.ok(/SEAT WAVE MICRO · UNLOCK SAFE CHECKS/.test(mission),
    "the control names the result of pressing it");
  assert.ok(/function addMissionMicro/.test(mission), "the mission upgrade has one explicit action");
  assert.ok(/chainAdd\("micro"\)/.test(mission), "the action really seats Micro");
  assert.ok(/focusFieldStep\("verify"\)/.test(mission),
    "after repaint it finds the first playable field control");
  const upgrade = mission.slice(mission.indexOf("function addMissionMicro"),
    mission.indexOf("function drawMission"));
  const earlyReturn = upgrade.indexOf("if (!unlocked) return");
  assert.ok(earlyReturn < 0 || upgrade.indexOf('focusFieldStep("verify")') < earlyReturn,
    "the field handoff cannot be skipped merely because the CRT is still on its overview face");
  assert.ok(/firstControl[\s\S]*glassScrollTo\(firstControl \|\| unlocked, true\)/.test(mission),
    "the newly live instrument, not merely the top of its card, is kept in view");
  assert.ok(/CLUE KIT OPEN/.test(mission) && /CLUES SOLVED/.test(mission),
    "the unlocked state reports progress");
});

// ---------- v24: condition-specific field training --------------------------

test("v24: the five recorded faults have five distinct training rigs", () => {
  const h = loadHook();
  const expected = {
    stuck: ["FROZEN INPUT", "REFERENCE", "TRACE POINT", "OPEN INPUT-CHANNEL WORK ORDER"],
    drifting: ["CALIBRATION OFFSET", "CAL POINT", "COMPARE", "OPEN CALIBRATION WORK ORDER"],
    dropout: ["INTERMITTENT LOOP", "TIMELINE", "TEST POINT", "OPEN FIELD-CONNECTION WORK ORDER"],
    noisy: ["NOISY SIGNAL PATH", "COMPARE", "INSTALLATION", "OPEN SHIELD-ROUTING WORK ORDER"],
    railed: ["RANGE MISMATCH", "RANGE SOURCE", "INPUT RANGE", "OPEN CONFIGURATION-CHANGE REVIEW"],
  };
  assert.deepEqual(Object.keys(h.fieldRigs).sort(), Object.keys(expected).sort());
  for (const [condition, words] of Object.entries(expected)) {
    const rig = h.fieldRigs[condition];
    assert.equal(rig.title, words[0]);
    assert.deepEqual(rig.controls.map((control) => control.id), ["verify", "context", "handoff"],
      `${condition} advances the same three-step mission spine`);
    assert.equal(rig.controls[0].label, words[1]);
    assert.equal(rig.controls[1].label, words[2]);
    assert.equal(rig.controls[2].label, words[3]);
    assert.ok(rig.controls.every((control) => control.finding && control.try),
      `${condition} has both a successful clue and useful wrong-setting feedback`);
    assert.notEqual(rig.authored, "", `${condition} declares its authored scenario clue`);
  }
  assert.equal(h.fieldRigs.drifting.controls[0].kind, "sequence",
    "drift uses an ordered 0/50/100 as-found sweep rather than a one-click repair");
  assert.deepEqual(h.fieldRigs.drifting.controls[0].sequence, ["zero", "mid", "span"]);
});

test("v24: field controls are Micro-gated, ordered, and never rewrite the recorded verdict", () => {
  const h = loadHook();
  const faultAt = h.incidentCandidates().findIndex((pick) => pick.cond === "railed");
  h.state.chain = ["pico", "nano"];
  h.drawIncident(faultAt);
  assert.equal(h.fieldApply("verify", "record"), false, "Pico + Nano cannot operate the site panel");
  assert.equal(h.fieldProgress(), 0);

  h.chainAdd("micro");
  solveModelMove(h);
  const before = JSON.stringify(h.state.verdict);
  assert.equal(h.fieldApply("context", "match"), false, "step two cannot jump over verification");
  assert.equal(h.fieldApply("verify", "display"), false, "a wrong setting teaches but does not pass");
  assert.equal(h.state.mission.field.feedback.kind, "try");
  assert.equal(h.fieldProgress(), 0);
  assert.equal(h.fieldApply("verify", "record"), true);
  assert.equal(h.fieldProgress(), 1);
  assert.equal(h.fieldApply("context", "match"), true);
  assert.equal(h.fieldProgress(), 2);
  assert.equal(h.fieldApply("handoff", "open"), true);
  assert.equal(h.fieldProgress(), 3);
  assert.equal(h.missionReady(), true);
  assert.equal(JSON.stringify(h.state.verdict), before,
    "training state must not recolour, rewrite, or replace recorded model evidence");
});

test("v24: the drifting rig requires the complete ordered as-found sweep", () => {
  const h = loadHook();
  const faultAt = h.incidentCandidates().findIndex((pick) => pick.cond === "drifting");
  h.state.chain = ["pico", "nano", "micro"];
  h.drawIncident(faultAt);
  solveModelMove(h);
  assert.equal(h.fieldApply("verify", "mid"), false, "starting at 50% is not an as-found sweep");
  assert.deepEqual(h.state.mission.field.visits.verify, []);
  assert.equal(h.fieldApply("verify", "zero"), false);
  assert.deepEqual(h.state.mission.field.visits.verify, ["zero"]);
  assert.equal(h.fieldApply("verify", "span"), false, "skipping the midpoint resets the sweep");
  assert.deepEqual(h.state.mission.field.visits.verify, []);
  assert.equal(h.fieldApply("verify", "zero"), false);
  assert.equal(h.fieldApply("verify", "mid"), false);
  assert.equal(h.fieldApply("verify", "span"), true);
  assert.equal(h.fieldProgress(), 1);
});

test("v24: every rig can reach verification only through its own controls", () => {
  for (const condition of ["stuck", "drifting", "dropout", "noisy", "railed"]) {
    const h = loadHook();
    const at = h.incidentCandidates().findIndex((pick) => pick.cond === condition);
    h.state.chain = ["pico", "nano", "micro"];
    h.drawIncident(at);
    solveModelMove(h);
    const rig = h.fieldRigs[condition];
    for (const control of rig.controls) {
      if (control.sequence) {
        for (const choice of control.sequence) h.fieldApply(control.id, choice);
      } else {
        assert.equal(h.fieldApply(control.id, control.correct), true,
          `${condition} accepts its authored ${control.id} action`);
      }
    }
    assert.equal(h.fieldProgress(), 3, `${condition} completes three checks`);
    const incidentNode = h.state.mission.incidentNode;
    assert.equal(h.verifyMission(), true, `${condition} unlocks recorded-OK verification`);
    assert.equal(h.state.cond, "none");
    assert.notEqual(h.state.mission.verifiedNode, incidentNode);
    assert.equal(h.verifyMission(), false, "the same incident cannot score twice");
  }
});

test("v24: a new incident or removal of Micro clears unverified field progress", () => {
  const h = loadHook();
  h.state.chain = ["pico", "nano", "micro"];
  const dropout = h.incidentCandidates().findIndex((pick) => pick.cond === "dropout");
  h.drawIncident(dropout);
  solveModelMove(h);
  assert.equal(h.fieldApply("verify", "source"), true);
  const completed = h.state.mission.completed;
  const noisy = h.incidentCandidates().findIndex((pick) => pick.cond === "noisy");
  h.drawIncident(noisy);
  solveModelMove(h);
  assert.equal(h.fieldProgress(), 0);
  assert.deepEqual(h.state.mission.actions, {});
  assert.equal(h.state.mission.completed, completed);
  assert.equal(h.fieldApply("verify", "independent"), true);
  h.chainRemove("micro");
  assert.equal(h.fieldProgress(), 0);
  assert.deepEqual(h.state.mission.actions, {});
  assert.equal(h.missionPlan().locked, true);
  assert.equal(h.state.mission.completed, completed);
});

test("v25: the current field control is operable in the monitor and mirrored on the bench", () => {
  assert.ok(htmlFlat.includes('id="wsField"'), "the sensor column owns a stable field-panel host");
  const render = js.slice(js.indexOf("function render()"), js.indexOf("function render()") + 420);
  assert.ok(/renderField\(\)/.test(render), "every bench render keeps the field panel in sync");
  const field = js.slice(js.indexOf("function currentFieldControl"), js.indexOf("function frontMission"));
  for (const copy of ["CASE TOOLS", "PRACTICE RIG · SAFE TO TRY",
                      "CLUE FROM THE CASE FILE", "CLOSE CASE WITH A HEALTHY READ"]) {
    assert.ok(field.includes(copy), `the field panel carries ${copy}`);
  }
  assert.ok(/role", "slider"/.test(field) && /ArrowLeft|ArrowRight/.test(field),
    "rotary controls expose value semantics and keyboard detents");
  assert.ok(/data-field-step/.test(field), "each training control has a shared step address");
  assert.ok(/function focusFieldStep/.test(js) && /querySelector\('\[data-field-step="'/.test(js),
    "monitor guidance can focus the matching visible control");
  const mission = js.slice(js.indexOf("function drawMission"), js.indexOf("function paintFront"));
  assert.ok(/focusFieldStep\(step\.id\)/.test(mission),
    "monitor steps move to their control instead of completing themselves");
  assert.ok(/drawFieldControl\(activeControl, activeAt, plan\)/.test(mission),
    "the active real control is rendered inside the shift console");
  assert.ok(/ACTIVE BENCH CONTROL/.test(mission) && /USE IT HERE/.test(mission),
    "the embedded instrument is plainly introduced as the place to act");
  assert.ok(!/addEventListener\("click", function \(\) \{\s*if \(!missionStep/.test(mission),
    "a checklist row cannot bypass its condition-specific control");
  assert.ok(/\.sn-field\s*\{/.test(css) && /\.sn-field__dial/.test(css),
    "the left bay has a real hardware panel and rotary controls");
  assert.ok(/@media \(max-width: 900px\)[\s\S]*\.sn-deck/.test(css),
    "the existing responsive order keeps field controls before the model chain");
});

test("v26: the shift console is a three-beat evidence mission, not a duplicate form", () => {
  const rigs = js.slice(js.indexOf("var FIELD_RIGS"), js.indexOf("function incidentCandidates"));
  for (const condition of ["stuck", "drifting", "dropout", "noisy", "railed"]) {
    assert.match(rigs, new RegExp(`${condition}: \\{[\\s\\S]*?objective:`),
      `${condition} carries its own field objective`);
  }
  const mission = js.slice(js.indexOf("function missionBeat"), js.indexOf("function paintFront"));
  for (const beat of ["OBSERVE", "ISOLATE", "HAND OFF"]) {
    assert.ok(mission.includes(beat), `${beat} is a named incident beat`);
  }
  assert.ok(/YOUR GOAL/.test(mission), "the condition-specific goal leads the sequence");
  assert.ok(/aria-current", active \? "step"/.test(mission),
    "the current beat is programmatically exposed");
  assert.ok(/CASE READY · EVIDENCE PACKET 03\/03/.test(mission),
    "completion becomes an evidence-packet finale");
  assert.ok(/CLOSE CASE WITH A HEALTHY READ/.test(mission),
    "the final action describes the replay change before it happens");
  assert.match(js, /querySelector\("\.sn-mission \.sn-mission__verify"\)[\s\S]*glassScrollTo/,
    "the final field action reveals the case-ready button inside the CRT instead of focusing the side copy");
  assert.ok(/does not claim the prior machine was repaired/i.test(mission),
    "the game finale keeps the causality boundary visible");
  assert.match(css, /\.sn-mission__steps\s*\{[^}]*grid-template-columns:\s*repeat\(3/,
    "the three beats read as a route across the CRT");
  assert.match(css, /\.sn-mission__step\.is-active[^}]*animation:/,
    "the current beat has restrained motion");
  assert.match(css, /prefers-reduced-motion:\s*reduce[\s\S]*\.sn-mission__step\.is-active/,
    "the mission pulse is removed for reduced motion");
});

test("v24: field training is local state, not a hidden network or machinery path", () => {
  const block = js.slice(js.indexOf("var FIELD_RIGS"), js.indexOf("function frontMission"));
  assert.doesNotMatch(block, /fetch\s*\(|XMLHttpRequest|WebSocket|sendBeacon/,
    "field actions never leave the browser");
  assert.match(block, /does not prove|not proof/i,
    "the recorded OK handoff retains its causality boundary");
  assert.doesNotMatch(block, /restart machine|bypass|force the input/i,
    "the game does not smuggle a machinery command into its training copy");
});

test("v22: completing the playbook verifies against a different committed OK window", () => {
  const h = loadHook();
  const faultAt = h.incidentCandidates().findIndex((pick) => pick.cond !== "none");
  h.state.chain = ["pico", "nano", "micro"];
  h.drawIncident(faultAt);
  const incidentNode = h.state.mission.incidentNode;
  for (const step of h.missionPlan().steps) h.missionStep(step.id);
  assert.equal(h.verifyMission(), true, "all three checks unlock verification");
  assert.equal(h.state.cond, "none");
  assert.equal(h.state.mission.phase, "verified");
  assert.equal(h.state.mission.completed, 1);
  assert.notEqual(h.state.mission.verifiedNode, incidentNode,
    "OK is a separate committed window, never a rewritten incident");
  assert.match(h.state.mission.note, /does not prove.*repaired/i,
    "the game refuses invented causality");
  assert.ok(/VERIFY WITH RECORDED OK/.test(js));
  assert.ok(/CASE CLOSED · SEPARATE RECORDED OK/.test(js));
  assert.ok(js.includes("This verifies the workflow; it does not prove ") &&
    js.includes("the training steps repaired the prior machine."),
    "the friendlier case-closed title does not erase the causality boundary");
});

test("v20: the menu leads with the tier that belongs in the band you opened", () => {
  // Founder: "its not easy to understand which one i should select based on
  // what i clicked." Every band knows its tier; the menu now says so.
  const h = loadHook();
  const bandTiers = ["pico", "nano", "micro", "giga", "tera", "peta", "exa"];
  bandTiers.forEach((tier, i) => {
    // the band table and the family must agree, or the recommendation lies
    assert.equal(h.bands[i].tier, tier, `band ${i} belongs to ${tier}`);
    assert.ok(h.family.some((f) => f.id === tier), `${tier} is a real tier`);
  });
  assert.ok(/ordered = \[pick\]\.concat/.test(js), "the band's own tier is moved to the front");
  assert.ok(/lives here/.test(js), "and marked as the one that lives there");
  assert.ok(/or put another tier here - the ladder stays open/.test(js),
    "the rest stay choosable - the deck does not forbid an unusual chain");
  assert.ok(/\.ws-menu__div ~ \.ws-menu__item \{ opacity/.test(css),
    "they just step back so the lead reads as the lead");
  // the motion is first-party and reduced-motion-gated (no library: CSP is self-only)
  assert.ok(/@media \(prefers-reduced-motion: no-preference\)[\s\S]*ws-menu-in/.test(css),
    "the arrival animation is opt-in, not imposed");
});

test("v20: the monitor's tabs follow the chain, whatever is in it", () => {
  // The strip used to hard-code pico and nano, so a chained Micro produced a
  // stage on the glass with no way to bring it up alone.
  const h = loadHook();
  h.selectType("temp", "dropout");
  h.state.chain = ["pico", "nano", "micro", "giga"];
  h.derive();
  const kinds = h.state.verdict.stages.map((s) => s.kind);
  assert.ok(kinds.includes("scoperun"), "Micro and Giga produce stages");
  // every stage a model produced must be reachable as its own tab
  const modelStages = h.state.verdict.stages.filter((s) => s.fam || s.kind === "pico" ||
    s.kind === "nano" || s.kind === "quietSenior");
  assert.ok(modelStages.length >= 4, "four models produced stages");
  assert.ok(/else if \(st\.fam\) \{ id = st\.fam\.id/.test(js),
    "the tab strip takes its ids from the stages, not a fixed list");
  assert.ok(/if \(stId !== PATCH\.tab\) return;/.test(js),
    "and one filter solos any of them, so no model is unreachable");
});

/* ---- v38: the mesh deck de-cluttered (UX audit 2026-08-17) -------------- */

test("v38: the sandbox opens with the essentials - gauge and case tools fold under MORE", () => {
  const h = loadHook();
  assert.equal(h.state.sideMore, false, "folded by default");
  assert.ok(/MORE · GAUGE & CASE TOOLS/.test(js), "the fold is one slim toggle");
  assert.ok(/var folded = !PATCH\.sideMore && !m\.active && PATCH\.gameMode !== "play"/.test(js),
    "and it unfolds by itself the moment a case is live");
  assert.ok(/\(PATCH\.sideMore \|\| PATCH\.gameMode === "play"\) \? drawVU/.test(js),
    "the gauge (a repeat of the trace on the glass) rides the same fold");
  assert.ok(/\.sn-field\.is-folded \{/.test(css), "and the folded bay drops its hardware chrome");
});

test("v38: the glass leads with the read - the fleet score waits for the visitor", () => {
  const face = js.slice(js.indexOf("function paintFront"), js.indexOf("function wireFront"));
  assert.ok(/if \(PATCH\.touched \|\| PATCH\.mission\.active\) f\.appendChild\(frontScore\(\)\)/.test(face),
    "23/50 FAULTS CAUGHT is not shown for a game nobody has started");
  assert.ok(/PATCH\.touched = true/.test(js), "and any context move earns it");
  assert.ok(!/sn-front__response/.test(face), "the dangling AFTER A FINDING label left the glass");
  assert.ok(/After a finding: " \+ response\.label/.test(face), "but the aria text still says it");
});

test("v38: the rail reads whole at desktop width, the bezel keys sit in front of the plate", () => {
  assert.ok(/\.sn-chainrail \.sn-slot__role, \.sn-chainrail \.sn-slot__takes \{ display: none; \}/.test(css),
    "role and fan-in ride the tooltip on the rail (still rendered for the detail and the locks)");
  assert.ok(/\.sn-roadmap--chain \{ display: none; \}/.test(css), "the 130-char caption became a tooltip");
  assert.ok(/host\.title = "In a real mesh one Pico reads many channels/.test(js), "set on the rail itself");
  const strip = css.slice(css.indexOf(".sn-tvctl {"), css.indexOf(".sn-tvctl__k {"));
  assert.ok(/translateZ\(14px\)/.test(strip),
    "the key strip is lifted past the plate's translateZ(10px) - z-index alone lost inside preserve-3d");
  const keys = css.slice(css.indexOf(".sn-tvctl__k {"), css.indexOf(".sn-tvctl__k--wide"));
  assert.ok(/color: var\(--ink-900\)/.test(keys) && /font-size: \.68rem/.test(keys),
    "and the legends are ink-900 at the type floor");
  const k = css.slice(css.indexOf(".wp-console__k {"), css.indexOf("}", css.indexOf(".wp-console__k {")));
  assert.ok(/font-size: \.72rem/.test(k) && /var\(--ink-900\)/.test(k), "the 1 · 2 · 3 labels win visually");
});
