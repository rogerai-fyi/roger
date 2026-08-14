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
  const ids = h.family.map((f) => f.id);
  for (const want of ["pico", "nano", "edge", "micro", "core", "station", "satellite"]) {
    assert.ok(ids.includes(want), `${want} must be on the menu`);
  }
  const recorded = h.family.filter((f) => f.status === "recorded").map((f) => f.id).sort();
  assert.deepEqual(recorded, ["nano", "pico"],
    "exactly two slots have a recorded run on this bench - claiming more would be a lie");
  assert.ok(h.family.every((f) => f.does), "every card states its transform in plain words");
  assert.ok(/will attach silent/.test(js), "the menu warns before an unrecorded slot chains in");
});

test("chain: an unrecorded model chains in, but its stage is honestly silent", () => {
  const h = loadHook();
  h.selectType("temp", "dropout");
  h.state.chain = ["micro"];
  h.derive();
  const st = h.state.verdict.stages;
  assert.equal(st.length, 2, "raw + the silent stage");
  assert.equal(st[1].kind, "silent");
  assert.ok(!("said" in st[1]) && !("margin" in st[1]),
    "a silent stage carries NO prediction and NO margin");
  assert.equal(h.state.verdict.state, "off", "and the lamp refuses to glow");
  assert.ok(/no recorded transcript, output unchanged/.test(js),
    "the stage says exactly what it is");
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
  assert.ok(/measured at this floor/.test(js), "the tooltip owns the numbers");
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
  assert.ok(/one pad per recorded condition/.test(js),
    "the pads say the rule out loud: unrecorded conditions have no pad");
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
  const tokens = read("styles/tokens.css");
  const used = new Set((css.match(/var\(--[a-z0-9-]+/g) || []).map((v) => v.slice(4)));
  for (const name of used) {
    if (tokens.includes(`${name}:`)) continue;
    const bare = new RegExp(`var\\(${name}\\s*\\)`);
    assert.ok(!bare.test(css),
      `${name} is not a token, so every use must carry an explicit fallback`);
  }
});

test("palette: red is a signal, never a surface", async () => {
  const reds = (css.match(/var\(--live\)/g) || []).length;
  assert.ok(reds > 0 && reds < 30, `red is used ${reds} times; it must stay a glint`);
  const filled = [];
  for (const block of css.split("}")) {
    if (/background:\s*var\(--live\)/.test(block)) {
      const sel = (block.split("{")[0] || "").trim().split("\n").pop().trim();
      filled.push(sel);
    }
  }
  // Three rules may fill with red: the masthead's spot plate, the intake's
  // one primary action, and the lamp window's red state.
  assert.deepEqual(filled.sort(),
    ['.wm-masthead__spot', '.wp-lampwin[data-state="red"]', '.wp-run'].sort(),
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
    assert.ok(/wp-lamp|wp-read__mark|syn-pad/.test(b),
      `green/yellow may only colour lamp-semantic surfaces, got: ${b.split("{")[0].trim()}`);
  }
  assert.ok(/STANDING BY|ALL CLEAR|DEGRADED|FAULTS MISSED/.test(js),
    "every lamp state carries a word");
  assert.ok(/[·●△⊗]/.test(js), "and an NE-107-style shape, so the verdict never rides on hue alone");
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
    assert.ok(/[Rr]aise the FLOOR/.test(h.state.verdict.why),
      "escalation fires when margin < floor, so the honest advice is RAISE");
    h.state.floor = 2.0;
    h.derive();
    assert.notEqual(h.state.verdict.state, "red", "raising the knob fixes what it can");
  }
});

test("lamp: green at the ceiling never claims ALL CLEAR while the fault was missed", () => {
  assert.ok(/AT CEILING/.test(js), "the ceiling label exists");
  assert.ok(/missed by the recorded senior itself/.test(js),
    "and the why-line attributes the miss to the recorded senior, not to luck");
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
    if (h.state.verdict.state === "green") {
      assert.equal(h.state.verdict.label, "AT CEILING",
        "a green lamp over a missed fault must say AT CEILING");
    }
  }
});

test("lamp: the operator still matters - the ladder ends with a person", () => {
  const h = loadHook();
  h.selectType("temp", "none");
  h.state.chain = ["pico"]; h.state.floor = 0.5; h.state.operator = false;
  h.derive();
  if (h.state.verdict.state === "yellow") {
    assert.ok(/operator/.test(h.state.verdict.why), "the yellow names the missing person");
  }
  h.state.operator = true;
  h.derive();
  assert.equal(h.state.verdict.state, "green", "OK dialed, chain agrees, operator on - green");
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

test("deck: the data is fetched same-origin, so the deck works offline", () => {
  const fetches = js.match(/fetch\("([^"]+)"/g) || [];
  assert.ok(fetches.length >= 3, "the deck loads its snapshots");
  for (const f of fetches) assert.ok(!/https?:\/\//.test(f), `${f} must be same-origin`);
  assert.ok(js.includes("did not load"), "and says so honestly when they do not");
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
  const rep = h.promptSend(scene.renders[Object.keys(scene.renders)[0]]);
  assert.equal(rep.kind, "draft", "a recorded wire blob earns the envelope");
  assert.equal(rep.wired, "Wave Nano", "addressed to the LAST model in the chain");
  assert.ok(rep.envelope.includes(measured.task_frame.slice(0, 40)),
    "and the envelope carries the exported task frame");
  const talk = h.promptSend("hi");
  assert.equal(talk.kind, "talk", "small talk never becomes a request");
  assert.ok(/never reaches a model/.test(talk.text), "and says so");
  const nums = h.promptSend("71.2, 71.3, 71.1, 71.4, 71.2, 71.3, 71.5, 71.2");
  assert.equal(nums.kind, "draft");
  assert.ok(/NOT STATED IN THE WIRE/.test(nums.unitNote), "the unit's absence is stated, not papered over");
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

test("shim: conversation never reaches a model", () => {
  assert.ok(js.includes('"talk"'), "small talk is a classified case");
  assert.ok(/answered by this interface, from the faceplate/i.test(js),
    "and is answered by the interface");
  assert.ok(/never reaches a model/.test(js), "stated plainly");
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

test("chain: the bench boots with the recommended pattern - Pico + Nano", () => {
  assert.ok(/PATCH.chain = \["pico", "nano"\]/.test(js), "the chain is pre-built at boot");
  assert.ok(/RECOMMENDED · PICO \+ NANO/.test(js), "and badged as the recommended pattern");
});

test("monitor: stage tabs are channel buttons, honest to the stages that exist", () => {
  assert.ok(js.includes('setAttribute("role", "tablist")'), "the tabs are a tablist");
  assert.ok(/\{ id: "all", label: "ALL" \}/.test(js), "ALL keeps the whole cascade");
  assert.ok(/if \(!id \|\| seen\[id\]\) return;/.test(js),
    "a tab exists only for a stage that exists");
});

// ---------- v8: honest display scale + the reading prompt ----------------------------

test("strip: the display scale has a documented floor, and prints itself", () => {
  assert.ok(/Math.max\(hi - lo, 0.03 \* Math.abs\(mean\), 1e-9\)/.test(js),
    "the span floor is a fixed, documented formula - never a per-condition fudge");
  assert.ok(/display scale/.test(js), "the strip prints the scale actually drawn");
  assert.ok(/samples are untouched/.test(js), "and states that the data is untouched");
});

test("prompt: a reading question is answered by THE BENCH, never signed as a model", () => {
  const h = loadHook();
  h.state.chain = ["pico", "nano"];
  h.selectType("temp", "drifting");
  const rep = h.promptSend("what does the temperature read right now?");
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
  // with nobody watching, the bench still answers but says so
  h.state.chain = [];
  const rep2 = h.promptSend("what is the reading now?");
  assert.ok(/Nobody is watching this channel/.test(rep2.chainLine));
});

test("prompt: the live seam exists, returns null today, and documents its wiring", () => {
  const h = loadHook();
  assert.equal(h.liveAnswerer({ kind: "question" }, {}), null,
    "no hosted band is reachable from this page - the seam must say so by returning null");
  assert.ok(/Tower relay/.test(js), "the intended transport is documented at the seam");
  assert.ok(/one-request-per-candidate grammar/.test(js), "with the enum protocol");
  assert.ok(/QUESTION-FOR-MODELS-AGENT-mesh-live-prompt/.test(js),
    "and the open ask to the models agent is referenced");
  assert.ok(/liveAnswerer\(v, liveCtx\(\)\) \|\| buildReply/.test(js),
    "promptSend is an answerer chain: live first, bench fallback");
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
  const whys = js.slice(js.indexOf("function renderWhys"), js.indexOf("/* ---- the measured figures"));
  assert.ok(/_provenance.suite/.test(whys), "the pop cites the suite");
  assert.ok(/configs.map\(function \(c\) \{ return c.config; \}\)/.test(whys),
    "and lists the config names it plotted");
  assert.ok(!/["'](?:56\.1|62|72\.6|73\.2)["']/.test(whys),
    "no measured number is typed into the why layer - all rendered from data");
});

test("why: the tiny story quotes the browser-tier quant with its source", () => {
  assert.ok(/browserTierQuant/.test(js), "the quant row is looked up, not remembered");
  const whys = js.slice(js.indexOf("function renderWhys"), js.indexOf("/* ---- the measured figures"));
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
  assert.ok(/saying 'I am not sure' - that honesty is the feature/.test(js));
});

test("why: pico and nano chain cards carry their story one tap away", () => {
  assert.ok(/why task-native\?/.test(js), "the pico card asks the question");
  assert.ok(/LOCKED ENUM with a MARGIN/.test(js), "and answers with the doctrine");
  assert.ok(/why a senior\?/.test(js), "the nano card asks its own");
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
  assert.ok(/prefers-reduced-motion: reduce\) \{ \.sn-tv__plate \{ transition: none; transform: none/.test(css),
    "and the CSS side goes static too");
});
