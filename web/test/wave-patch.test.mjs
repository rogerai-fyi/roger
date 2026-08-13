// Regression locks for THE REACTIVE BENCH (Playbox MESH deck).
//
// REBUILT 2026-08 (founder direction): no templates, no manual cabling. The sheet starts
// as just the signal; models are dragged in and the cables draw themselves; each Pico
// carries a rotary FLOOR knob whose detents are exactly the measured sweep points; the
// chain ends in a green/yellow/red lamp window and a chart strip. Every HONESTY lock from
// the previous suite is carried forward - those tests are the only enforcement the rails
// have - and the derivation itself is now EXECUTED under a stub DOM, not just grepped.
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

// ---------- the module, EXECUTED under a stub DOM ------------------------------
// Static greps let classification bugs ship twice. The hook exposes the pure core -
// classify, derive, addModule, bootSheet, PATCH - so the rules below are exercised as
// code, with the committed snapshots as input.

function loadHook() {
  const stubEl = () => ({ addEventListener() {}, setAttribute() {}, style: {}, classList: { add() {}, remove() {}, toggle() {} }, appendChild() {}, textContent: "", hidden: true, focus() {}, dataset: {}, closest: () => null });
  const sandbox = {
    window: { matchMedia: () => ({ matches: true }), localStorage: { getItem: () => null, setItem() {} }, location: { hash: "" } },
    document: {
      readyState: "complete", addEventListener() {}, removeEventListener() {},
      getElementById: () => null, querySelector: () => null, querySelectorAll: () => [],
      createElement: stubEl, createElementNS: stubEl, body: { classList: { add() {}, remove() {} }, appendChild() {} },
      activeElement: null,
    },
    fetch: () => new Promise(() => {}),
    setTimeout: () => 0,
  };
  sandbox.window.document = sandbox.document;
  const fn = new Function("window", "document", "fetch", "setTimeout", js + "; return window.__wavePatchTest;");
  const hook = fn(sandbox.window, sandbox.document, sandbox.fetch, sandbox.setTimeout);
  hook.setScene(scene, catalog);
  hook.setMeasured(measured);
  return hook;
}

function bench() {
  // a fresh sheet: feed + intake, nothing else
  const h = loadHook();
  h.state.tiles = [];
  h.bootSheet();
  return h;
}

const mod = (h, tier) => h.modules.find((m) => m.tier === tier);

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
});

test("honesty: the packet flies once per interaction, never as a stream", () => {
  assert.ok(js.includes("flyOnce"), "there is a single packet animation");
  assert.ok(/kind === "escalate"/.test(js.slice(js.indexOf("function flyOnce"))),
    "and it rides only an escalation cable that a real record moved on");
  assert.ok(!/setInterval/.test(js), "nothing may animate continuously");
});

test("honesty: no evidence dict is invented for escalations", () => {
  for (const r of measured.records) assert.ok(!("evidence" in r));
  assert.ok(/carry no evidence dict/i.test(js),
    "the wire inspector must say the evidence dict is absent, not omit it silently");
});

test("honesty: the model faceplate leaves the digest empty rather than inventing one", () => {
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
  assert.ok(!/\$/.test(tipBlock), "no currency near the measured figures");
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

// ---------- the reactive bench: concept locks -----------------------------------

test("bench: the sheet starts as just the signal - no templates, no shelf", () => {
  const h = bench();
  assert.deepEqual(h.state.tiles.map((t) => t.kind).sort(), ["feed", "intake"],
    "boot = the plant feed and the intake, nothing else");
  assert.ok(!htmlFlat.includes("wpShelf"), "the template shelf is gone");
  assert.ok(!/var TEMPLATES|TEMPLATES\s*=/.test(js), "and no template table survives in the module");
  assert.ok(!htmlFlat.includes('id="wpRun"'), "no RUN button - the bench reacts, it is not run");
});

test("bench: cables draw themselves - there is no manual wiring state machine", () => {
  assert.ok(js.includes("function rewire"), "topology is derived, not clicked together");
  assert.ok(!js.includes("armPort") && !js.includes("tryConnect") && !js.includes("markCompatible"),
    "click-to-connect is gone with the freedom to miswire");
  const h = bench();
  h.addModule(mod(h, "pico"));
  assert.ok(h.state.wires.some((w) => w.kind === "channel"),
    "dropping a pico wires it to the feed by itself");
  h.addModule(mod(h, "nano"));
  assert.ok(h.state.wires.some((w) => w.kind === "escalate"),
    "and a nano attracts the escalation cable by itself");
});

test("bench: an operator refuses to connect before a nano exists", () => {
  const h = bench();
  assert.equal(h.addModule(mod(h, "human")), null,
    "a human in front of raw channels is refused");
  assert.ok(/reads rollups, not raw channels/.test(js),
    "and the refusal teaches the reason");
  h.addModule(mod(h, "pico"));
  assert.equal(h.addModule(mod(h, "human")), null, "a pico alone is still not enough");
  h.addModule(mod(h, "nano"));
  assert.ok(h.addModule(mod(h, "human")), "with a nano, the operator takes the shift");
});

test("bench: the nano takes many children - picos fan in, and partition the records", () => {
  const h = bench();
  const pico = mod(h, "pico");
  assert.ok(pico.max >= 3, "nano fan-in is the mesh - at least 3 picos");
  assert.equal(mod(h, "nano").max, 1, "one nano is the mesh's shape here");
  assert.equal(mod(h, "human").max, 1, "one operator");
  h.addModule(pico); h.addModule(pico); h.addModule(pico);
  h.derive();
  const stats = h.state.tiles.filter((t) => t.tier === "pico").map((t) => t.stats);
  const total = stats.reduce((a, s) => a + s.read, 0);
  assert.equal(total, h.state.online,
    "the picos PARTITION the live records - nothing is double-counted or invented");
  assert.ok(/i % picos.length/.test(js), "partition is arithmetic on the recorded sample");
});

test("bench: knob detents are exactly the measured floors - nothing else is dialable", () => {
  const h = loadHook();
  const measuredFloors = measured.escalation.configs
    .map((c) => /^child\+parent@([\d.]+)$/.exec(c.config))
    .filter(Boolean).map((m) => parseFloat(m[1])).sort((a, b) => a - b);
  assert.deepEqual(h.detents, measuredFloors,
    "the knob physically cannot ask for an unmeasured number");
  assert.ok(/DETENTS.indexOf/.test(js), "and the knob snaps to detents, never between them");
});

test("bench: the measured figures live on the knob, with their suite", () => {
  assert.ok(/measured at this floor/.test(js), "the tooltip owns the numbers");
  assert.ok(/_provenance.suite/.test(js.slice(js.indexOf("function knobTip"))),
    "and cites the suite they were measured on");
});

// ---------- the lamp: derived, honest, playable ----------------------------------

test("lamp: every state is a recount of the recorded records", () => {
  const h = bench();
  assert.ok(!h.state.verdict, "no verdict before data flows");
  h.derive();
  assert.equal(h.state.verdict.state, "off", "no reader, no verdict colour");

  h.addModule(mod(h, "pico"));
  const red = h.state.verdict;
  assert.equal(red.state, "red", "at the default floor the recorded sample has fixable misses");
  assert.ok(red.totals.missed > 0, "and the misses are counted, not asserted");

  h.addModule(mod(h, "nano"));
  // raise the knob to the top measured floor: every knob-fixable miss escalates
  h.state.tiles.filter((t) => t.tier === "pico").forEach((t) => { t.data.floor = h.detents[h.detents.length - 1]; });
  h.derive();
  assert.equal(h.state.verdict.state, "yellow", "all-caught-that-can-be, but no operator yet");

  h.addModule(mod(h, "human"));
  const green = h.state.verdict;
  assert.equal(green.state, "green", "complete chain at the top floor goes green");
  assert.ok(green.totals.fixable === 0, "green requires zero knob-fixable misses");
});

test("lamp: green at the ceiling never claims ALL CLEAR while faults were missed", () => {
  const h = bench();
  h.addModule(mod(h, "pico")); h.addModule(mod(h, "nano")); h.addModule(mod(h, "human"));
  h.state.tiles.filter((t) => t.tier === "pico").forEach((t) => { t.data.floor = h.detents[h.detents.length - 1]; });
  h.derive();
  const v = h.state.verdict;
  assert.equal(v.state, "green");
  if (v.totals.missed > 0) {
    assert.equal(v.label, "AT CEILING", "a green lamp over missed faults must say AT CEILING");
    assert.ok(/missed by the senior model itself/.test(v.why),
      "and the why-line attributes the remainder to the recorded parent, not to luck");
  }
});

test("lamp: red means fixable - the why-line points at the knob, in the right direction", () => {
  const h = bench();
  h.addModule(mod(h, "pico")); h.addModule(mod(h, "nano")); h.addModule(mod(h, "human"));
  h.state.tiles.filter((t) => t.tier === "pico").forEach((t) => { t.data.floor = h.detents[0]; });
  h.derive();
  assert.equal(h.state.verdict.state, "red", "the lowest floor misses catchable faults");
  assert.ok(/[Rr]aise the FLOOR/.test(h.state.verdict.why),
    "escalation fires when margin < floor, so the honest advice is RAISE, not lower");
  // and the arithmetic behind 'fixable' is stated in the code
  assert.ok(/r.parent.prediction === r.truth && r.child.margin < TOP/.test(js),
    "fixable = a higher detent would have escalated it AND the recorded parent was right");
});

test("lamp: a pico without a nano dead-ends its escalations, and the lamp says so", () => {
  const h = bench();
  h.addModule(mod(h, "pico"));
  h.derive();
  const t = h.state.verdict.totals;
  assert.ok(t.deadEnd > 0, "escalations with nobody to hear them are counted");
  assert.equal(t.escalated, t.deadEnd + 0, "every escalation is unheard without a nano");
  assert.equal(h.state.verdict.state, "red", "and the lamp does not pretend otherwise");
});

test("lamp: the founder's colours are literals, fenced to the lamp, with shapes riding along", () => {
  // tokens.css deliberately has no green/yellow. The lamp window is the ONE
  // place those hues exist, by founder mandate - as literals, so the
  // only-real-tokens rule below stays true.
  assert.ok(/FOUNDER MANDATE/.test(css), "the exception is documented where it lives");
  const hueRules = css.split("}").filter((b) => /#1E7A3C|#C99700/.test(b));
  for (const b of hueRules) {
    assert.ok(/wp-lamp/.test(b), `green/yellow may only colour lamps, got: ${b.split("{")[0].trim()}`);
  }
  assert.ok(/NO READER|ALL CLEAR|DEGRADED|FAULTS MISSED/.test(js),
    "every lamp state carries a word");
  assert.ok(/[·●△⊗]/.test(js), "and an NE-107-style shape, so the verdict never rides on hue alone");
});

test("strip: the chart recorder narrates reactions, capped and newest-first", () => {
  assert.ok(htmlFlat.includes('id="wpStrip"'), "the strip window exists");
  assert.ok(htmlFlat.includes('id="wpLamp2"'), "beside the lamp window");
  assert.ok(/insertBefore\(li, strip.firstChild\)/.test(js), "newest line on top");
  assert.ok(/> 6\) strip.removeChild/.test(js), "capped - a strip, not a log");
});

// ---------- geometry + interaction ----------------------------------------------

test("graph: the ladder runs left to right, and geometry is semantics", () => {
  assert.ok(/COLS = \["feed", "pico", "nano", "human"\]/.test(js),
    "feed -> pico -> nano -> human, left to right");
  assert.ok(/colX\(/.test(js) && /slotY\(/.test(js), "tiles land on typed columns");
});

test("graph: modules snap to typed columns, never a free canvas", () => {
  assert.ok(/SLOT_PITCH = TILE_H \+ GAP_Y/.test(js), "slot pitch = tile + gap, so tidy-up is a no-op");
  assert.ok(/pointercancel/.test(js), "drags clean up on pointercancel, where stuck-drag bugs live");
  assert.ok(/< 6\)/.test(js), "6px of drift before a press becomes a drag");
});

test("interaction: no HTML5 drag for wiring; the intake is the one drop target", () => {
  assert.ok(!/dragstart/.test(js), "we never initiate HTML5 drags");
  const dndUses = (js.match(/dataTransfer/g) || []).length;
  const dropBlock = js.slice(js.indexOf('addEventListener("drop"'), js.indexOf('addEventListener("drop"') + 600);
  assert.ok(dndUses > 0 && dndUses === (dropBlock.match(/dataTransfer/g) || []).length,
    "dataTransfer appears only in the intake's external-drop handler");
});

test("interaction: the knob and fader do not fight the tile drag", () => {
  assert.ok(/closest\(".wp-knob"\) \|\| e.target.closest\(".wp-fader"\)/.test(js),
    "grabbing an instrument must never lift the tile it sits on");
  assert.ok(/setPointerCapture/.test(js), "the knob captures its pointer for the whole turn");
});

test("interaction: the fader defers the re-render to change, so it survives its own drag", () => {
  const faderBlock = js.slice(js.indexOf("function drawFader"), js.indexOf("var LAMPS"));
  assert.ok(/'input'|"input"/.test(faderBlock) && /'change'|"change"/.test(faderBlock),
    "input recounts, change re-renders");
  assert.ok(/MID-DRAG/.test(faderBlock), "and the reason is written down");
});

test("interaction: the escalation wire is dotted, the draft wire is its own mark", () => {
  assert.ok(/\.wp-wire--escalate .wp-wire__line \{ stroke-dasharray/.test(css));
  assert.ok(/\.wp-wire--user .wp-wire__line \{ stroke-dasharray/.test(css),
    "a visitor's channel rides a visibly different cable");
});

test("interaction: wires are hit-testable with a fat invisible path", () => {
  assert.ok(/stroke-opacity: 0; stroke-width: 20/.test(css), "20px hit target");
  assert.ok(/pointer: coarse\) \{ \.wp-wire__hit \{ stroke-width: 32/.test(css),
    "wider on touch, where 20px is marginal");
});

// ---------- the palette ----------------------------------------------------------

test("palette: only tokens that exist are referenced", () => {
  const tokens = read("styles/tokens.css");
  const used = new Set((css.match(/var\(--[a-z0-9-]+/g) || []).map((v) => v.slice(4)));
  for (const name of used) {
    assert.ok(tokens.includes(`${name}:`), `${name} must be a real token`);
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
  // Three rules may fill with red: the one primary action (the intake's SEND),
  // the masthead's SPOT PLATE (clipped by a <16KB alpha mask to the engraving's
  // lamp), and the LAMP WINDOW in its red state - the founder-mandated verdict
  // light, which is the page's largest red on purpose: it means recorded faults
  // were missed that the knobs could have caught.
  assert.deepEqual(filled.sort(),
    ['.wm-masthead__spot', '.wp-lampwin[data-state="red"]', '.wp-run'].sort(),
    `only SEND, the spot plate and the red lamp may fill red; got ${filled.join(", ")}`);
  const spotBytes = statSync(path.join(SRC, "assets/wave/mesh-console-spot.png")).size;
  assert.ok(spotBytes < 16 * 1024,
    `the spot mask is ${spotBytes} bytes; if it grows past ~16KB it has stopped being a glint`);
});

// ---------- accessibility ---------------------------------------------------------

test("a11y: the bench is keyboard operable", () => {
  assert.ok(/e.key === "Delete" \|\| e.key === "Backspace"/.test(js), "Delete removes a model");
  assert.ok(/e.key.indexOf\("Arrow"\) === 0/.test(js), "arrows move between tiles");
  assert.ok(js.includes("moveFocus"), "and focus follows the ladder");
  assert.ok(/role", "slider"/.test(js) || /"role", "slider"/.test(js.replace(/setAttribute\(/g, '"role", ')) || js.includes('setAttribute("role", "slider")'),
    "the knob is a slider to assistive tech");
  assert.ok(/aria-valuenow/.test(js), "with its value exposed");
  assert.ok(/ArrowUp|ArrowRight/.test(js.slice(js.indexOf("function drawKnob"))),
    "and arrow keys turn it");
});

test("a11y: there is a list mirror of the patch, always in the DOM", () => {
  assert.ok(htmlFlat.includes('id="wpMirror"'), "the mirror element exists");
  assert.ok(js.includes("renderMirror"), "and is kept in sync");
  assert.ok(/View this patch as a list/i.test(htmlFlat), "and is reachable");
});

test("a11y: tiles are described, and reactions are announced once", () => {
  assert.ok(js.includes('setAttribute("aria-label", describe(t))'), "each tile is described");
  assert.ok(htmlFlat.includes('aria-live="polite"'), "reactions are announced");
  assert.ok(!/role="application"/.test(htmlFlat), "never role=application - it kills browse mode");
  assert.ok(/screen-reader-only/.test(css), "the announcer is sr-only; the strip is the visible narrator");
});

test("a11y: reduced motion is honoured", () => {
  assert.ok(css.includes("prefers-reduced-motion"), "animations have a static equivalent");
  assert.ok(/@media \(prefers-reduced-motion: reduce\) \{ \.wp-packet \{ display: none/.test(css),
    "the packet does not fly for people who asked for less motion");
  assert.ok(js.includes("REDUCED"), "and the module checks the preference too");
});

// ---------- structure + offline ----------------------------------------------------

test("deck: the sheet, the console and the results all exist", () => {
  for (const id of ["wpSheet", "wpWires", "wpLamp2", "wpWhy", "wpStrip", "wpFeed", "wpInspect", "wpProv"]) {
    assert.ok(htmlFlat.includes(`id="${id}"`), `${id} must exist`);
  }
});

test("deck: the data is fetched same-origin, so the deck works offline", () => {
  const fetches = js.match(/fetch\("([^"]+)"/g) || [];
  assert.ok(fetches.length >= 3, "the deck loads its snapshots");
  for (const f of fetches) assert.ok(!/https?:\/\//.test(f), `${f} must be same-origin`);
  assert.ok(js.includes("did not load"), "and says so honestly when they do not");
});

test("deck: the scope only draws for the scene that has committed samples", () => {
  assert.ok(/sc.asset_type|scene_id/.test(js.slice(js.indexOf("function drawScopeInto"))),
    "the trace is the recorded pump scene, drawn only where it belongs");
});

// ---------- the translation shim (handoff 3) ----------------------------------------

test("shim: pasted bytes earn an envelope, never a result", () => {
  assert.ok(js.includes("DRAFT · NOT RUN"), "the draft state is named");
  assert.ok(js.includes("envelopeFor"), "what a paste earns is the request envelope");
  assert.ok(/pending export/.test(js.slice(js.indexOf("function envelopeFor"))),
    "the task frame text is not invented - its slot says pending, like the digest");
  const h = bench();
  const intake = h.state.tiles.find((t) => t.kind === "intake");
  intake.data.channels = [{ name: "your channel", unit: "" }];
  intake.data.body = "71.2, 71.3, 71.1";
  h.addModule(mod(h, "pico"));
  const userWire = h.state.wires.find((w) => w.user);
  assert.ok(userWire, "the pasted channel patches itself into the first pico");
  h.derive();
  assert.ok(h.state.tiles.every((t) => !(t.margin && t.userOnly)),
    "and no margin is ever computed for it - a margin is a logprob difference");
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
  assert.ok(/answered by this interface, from the faceplate/.test(js),
    "and is answered by the interface");
  assert.ok(/never reaches a model/.test(js), "stated plainly");
});

test("shim: scenario words are recognised, and never free-texted to a model", () => {
  assert.ok(js.includes("scenario-asset"), "a described scenario is a classified case");
  assert.ok(/words are never sent to a Wave model/.test(js));
  assert.ok(!/LOAD .*template/i.test(js), "with the shelf gone, no dead LOAD affordance survives");
});

test("shim: the intake drawer exists and the samples are the recorded renders", () => {
  for (const id of ["wpIntake", "wpPaste", "wpDetMod", "wpDetShape", "wpDetFrame", "wpSend", "wpSamples"]) {
    assert.ok(htmlFlat.includes(`id="${id}"`), `${id} must exist`);
  }
  assert.ok(js.includes("sc.renders[mo]"), "samples load the committed renderer output, not mock text");
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
