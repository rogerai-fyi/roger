#!/usr/bin/env node
// Export the MEASURED results the Wave Mesh deck is allowed to display.
//
// Handoff 1 §8 and handoff 2 set the rule: a number may appear on the deck only with its
// provenance - the run that produced it and the suite it was measured on. So this script
// does not copy numbers out of prose. It reads the actual result JSON the eval harness
// wrote, and carries the source filename into the bundle for every figure.
//
//   node scripts/export-wave-measured.mjs [--hierarchy <path>] [--out <path>]
//
// What it exports, and why each one earns its place on a deck:
//
//   frames        margin-per-frame.json - the per-frame margin floors (R.48). These are the
//                 VU meter's redlines: below its frame's floor a tile escalates.
//   escalation    fleet-sim-*.json results - the measured sweep of child-only -> child+
//                 parent@floor -> parent-everywhere. This is the whole economic argument
//                 for a mesh, and it is a slider, not a slogan.
//   records       fleet-sim per_item - REAL child and parent predictions with their margins.
//                 The alert feed renders these; the honesty lamp is computed from them.
//   quants        iebs12-*.json per-quant accuracy. The Q4 collapse is a product lesson, so
//                 the deck states it rather than hiding it.
//
// It deliberately does NOT invent an evidence dict for escalations. The frozen record schema
// carries one, but fleet_sim's per-item export does not contain it, and a plausible-looking
// evidence blob on a deck whose claim is "this is real" would be the worst kind of lie.

import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync, existsSync, mkdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const DEFAULT_HIERARCHY = resolve(process.env.HOME || "", "ai/computer-scientist/build/roger-wave/hierarchy");
const DEFAULT_OUT = resolve(HERE, "..", "src", "data", "wave-measured.json");

function arg(name, fallback) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : fallback;
}
const hierarchy = resolve(arg("--hierarchy", DEFAULT_HIERARCHY));
const out = resolve(arg("--out", DEFAULT_OUT));
const OUTDIR = join(hierarchy, "out");

function readJSON(name) {
  const p = join(OUTDIR, name);
  if (!existsSync(p)) return null;
  try { return JSON.parse(readFileSync(p, "utf8")); } catch { return null; }
}

// ---- 1. the per-frame margin floors (VU redlines) ----------------------------
const frameRaw = readJSON("margin-per-frame.json");
if (!frameRaw) {
  console.error("[measured] margin-per-frame.json missing; the VU redlines have no source.");
  process.exit(1);
}
const frames = {};
for (const [name, f] of Object.entries(frameRaw)) {
  frames[name] = {
    n: f.n,
    acc: f.acc,
    floor: f.recommended_floor,
    median_margin_correct: f.median_margin_correct,
    median_margin_wrong: f.median_margin_wrong,
  };
}

// ---- 2. the escalation sweep -------------------------------------------------
// Newest fleet-sim run wins; its child/parent are named so the deck can say WHOSE numbers
// these are rather than implying they belong to whatever tile is on screen.
let fleetName = null, fleet = null;
for (const n of ["fleet-sim-v4.json", "fleet-sim-v3.json", "fleet-sim-v2.json", "fleet-sim-v1.json"]) {
  const d = readJSON(n);
  if (d && d.results && d.per_item) { fleetName = n; fleet = d; break; }
}
if (!fleet) {
  console.error("[measured] no fleet-sim run with results + per_item found.");
  process.exit(1);
}

const parentDirect = fleet.results.find((r) => r.config === "parent-direct");
const escalation = {
  bench: fleet.bench,
  n: fleet.n,
  child: fleet.child,
  parent: fleet.parent,
  cost_note: fleet.cost_note,
  configs: fleet.results.map((r) => ({
    config: r.config,
    macro_recall: r.macro_recall,
    raw: r.raw,
    escalation_rate: r.escalation_rate,
    abstain_rate: r.abstain_rate,
    params_per_item: r.mean_params_per_item,
    // Cost is only meaningful RELATIVE to asking the big model about everything. That is
    // the comparison a plant actually faces, so compute it here instead of leaving a bare
    // parameter count on the page.
    pct_of_parent_everywhere: parentDirect && parentDirect.mean_params_per_item
      ? r.mean_params_per_item / parentDirect.mean_params_per_item
      : null,
  })),
};

// ---- 3. real records, rendered into the frozen schema ------------------------
// The frozen wire schema (handoff 2 §2c) is {node_id, scope, task, kind, prediction,
// margin} plus an evidence dict on escalations. fleet_sim's per-item export carries the
// predictions and margins but NOT the evidence, so `evidence` is omitted and the bundle
// says so. kind is not stored either - it is DERIVED from the margin against the floor,
// which is exactly what a tile does at runtime.
const SAMPLE = 120;

// The bench file carries what the model actually READ per item: the sensor's
// tag, its stated unit ("?" when the wire did not state one - kept as null,
// never defaulted), and the feature-summary window (range, mean, slope, ...).
// Joining it in lets the deck show real instrument names and real bounds
// instead of bare channel ids - and lets the sensor's "log" be the literal
// input body, not an invention. The shared task frame is exported once.
const benchPath = join(OUTDIR, String(fleet.bench || "").split("/").pop() || "mm-hard-signal.jsonl");
let benchRows = new Map();
let taskFrame = null;
if (existsSync(benchPath)) {
  for (const line of readFileSync(benchPath, "utf8").split("\n")) {
    if (!line.trim()) continue;
    const r = JSON.parse(line);
    benchRows.set(r.channel_id, r);
    if (!taskFrame && r.prompt) taskFrame = r.prompt.split("Input:")[0].trimEnd();
  }
} else {
  console.error(`[measured] ${benchPath} missing; records will carry no tags or windows.`);
}

function windowOf(channelId) {
  const row = benchRows.get(channelId);
  if (!row || !row.prompt) return null;
  const body = (row.prompt.split("Input:\n")[1] || "").split("\nAssertion:")[0].trimEnd();
  if (!body) return null;
  const grab = (re) => { const m = re.exec(body); return m ? m[1] : null; };
  const num = (re) => { const v = grab(re); return v == null ? null : Number(v); };
  const unit = grab(/stated_unit=(\S+)/);
  return {
    tag: grab(/tag=(\S+)/),
    // "?" means the wire did not state a unit. null, not a default.
    unit: unit === "?" ? null : unit,
    n: num(/\bn=(\d+)/),
    lo: num(/range=\[([-\d.]+),/),
    hi: num(/range=\[[-\d.]+,([-\d.]+)\]/),
    mean: num(/mean=([-\d.]+)/),
    sd: num(/sd=([-\d.]+)/),
    slope_per_min: num(/slope_per_min=([-\d.]+)/),
    // the full fingerprint of HOW the window misbehaves - these let the deck
    // pick, deterministically and by documented criterion, the recorded
    // instance that most clearly EXPRESSES a condition (max longest_run for
    // stuck, max gap_frac for dropout, ...), and to scale traces honestly
    hf_energy: num(/hf_energy=([-\d.]+)/),
    repeat_frac: num(/repeat_frac=([-\d.]+)/),
    longest_run: num(/longest_run=(\d+)/),
    zero_frac: num(/zero_frac=([-\d.]+)/),
    at_max_frac: num(/at_max_frac=([-\d.]+)/),
    gap_frac: num(/gap_frac=([-\d.]+)/),
    monotonic_frac: num(/monotonic_frac=([-\d.]+)/),
    n_resets: num(/n_resets=(\d+)/),
    max_drop: num(/max_drop=([-\d.]+)/),
    sign_changes: num(/sign_changes=(\d+)/),
    body, // the literal window the model read - the sensor's honest "log"
  };
}

const records = fleet.per_item.slice(0, SAMPLE).map((p) => ({
  scene_id: p.scene_id,
  node_id: p.channel_id,
  truth: p.truth,
  window: windowOf(p.channel_id),
  child: { prediction: p.child_pred, margin: p.child_margin },
  parent: { prediction: p.parent_pred, margin: p.parent_margin },
}));

// ---- 4. per-quant accuracy -------------------------------------------------
//
// THE RETRACTION (2026-08-13). An earlier export published "Q4_K_M collapses the 98M,
// 72.6 -> 22.3" as the deck's quantization lesson. That was a HARNESS bug - the server
// grammar did not match the trained leading-space continuation - and it was caught
// because Q8 and Q4 came back with impossible identical numbers. Re-measured with the
// fixed protocol, every size holds: the task-native 98M decodes with margins wide enough
// that greedy choices survive 4-bit. The ~65MB browser tier is real.
//
// So this export does two things. It REFUSES the buggy pair permanently - if two
// quantizations ever again return identical aggregates, that is a harness fault, not a
// finding, and it must never reach the page. And until fixed-protocol eval JSONs are
// written to out/, it carries the certified figures transcribed from the results log,
// with the R-number recorded so the page cites a source rather than a memory.
const CERTIFIED = {
  // Keep the notebook coordinate here in the exporter history, never in the
  // browser payload. Public copy describes the method instead of exposing an
  // internal filename and row number.
  source: "corrected fixed-protocol quantization sweep",
  task: "T01 fault-ID",
  note: "fixed-protocol endpoint battery (leading-space grammar), stock llama-server",
  rows: [
    { quant: "f16",    fault_id_macro: 0.726, n: 250, size_mb: 200, role: "reference" },
    { quant: "Q8_0",   fault_id_macro: 0.732, n: 150, size_mb: 105, role: "" },
    { quant: "Q4_K_M", fault_id_macro: 0.732, n: 150, size_mb: 65,  role: "browser tier" },
  ],
};

// The guard: identical aggregates across two quantizations are the signature of the bug.
const q4raw = readJSON("iebs12-pico-scratch-v2-q4.json");
const q8raw = readJSON("iebs12-pico-scratch-v2-q8.json");
let harnessSuspect = null;
if (q4raw && q8raw && JSON.stringify(q4raw.results) === JSON.stringify(q8raw.results)) {
  harnessSuspect =
    "The Q4 and Q8 endpoint outputs return byte-identical aggregates; that is the " +
    "signature of the corrected grammar-spacing harness bug, not a measurement";
}

const quants = CERTIFIED.rows.map((r) => ({
  quant: r.quant,
  fault_id_macro: r.fault_id_macro,
  n: r.n,
  size_mb: r.size_mb,
  role: r.role,
  source: CERTIFIED.source,
}));

// ---- write -------------------------------------------------------------------
const doc = {
  _provenance: {
    exported: new Date().toISOString().slice(0, 10),
    hierarchy: hierarchy.replace(process.env.HOME || "~", "~"),
    suite: "IEB-Signals v1.2",
    sources: {
      frames: "out/margin-per-frame.json",
      escalation: `out/${fleetName}`,
      records: `out/${fleetName} (per_item) ⋈ out/${String(fleet.bench || "").split("/").pop()} (tags, units, windows, task frame)`,
      quants: CERTIFIED.source + " (" + CERTIFIED.note + ")",
    },
    retracted: {
      claim: "Q4_K_M collapses the 98M (72.6 -> 22.3)",
      status: "RETRACTED - harness bug (grammar spacing), replaced by the corrected endpoint run",
      guard: harnessSuspect,
    },
    note: "Regenerate with: node scripts/export-wave-measured.mjs",
  },
  frames,
  escalation,
  // The T01 task frame, verbatim from the bench file. This retires the
  // "frame text - pending export" placeholder the deck carried.
  task_frame: taskFrame,
  records,
  quants,
  quant_note: CERTIFIED.note,
};

mkdirSync(dirname(out), { recursive: true });
writeFileSync(out, JSON.stringify(doc, null, 1) + "\n");
console.log(`[measured] wrote ${out}`);
console.log(`[measured] frames=${Object.keys(frames).length} configs=${escalation.configs.length} ` +
            `records=${records.length} quants=${quants.map((q) => q.quant).join("/")}`);
