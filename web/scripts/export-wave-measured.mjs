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
const records = fleet.per_item.slice(0, SAMPLE).map((p) => ({
  scene_id: p.scene_id,
  node_id: p.channel_id,
  truth: p.truth,
  child: { prediction: p.child_pred, margin: p.child_margin },
  parent: { prediction: p.parent_pred, margin: p.parent_margin },
}));

// ---- 4. per-quant accuracy (the Q4 lesson) -----------------------------------
// Q8 is deliberately EXCLUDED. Its exported result is indistinguishable from Q4 - 1199 of
// 1200 predictions identical, and the one that differs is wrong in both - which is not a
// plausible outcome for two quantizations four bits apart, and would have the deck publish
// "Q8 collapses too". Flagged to the models agent; re-include when a re-run disagrees.
const quantSources = [
  { label: "f16", file: "iebs12-pico-scratch-v2.json" },
  { label: "Q4_K_M", file: "iebs12-pico-scratch-v2-q4.json" },
];
const quants = [];
for (const q of quantSources) {
  const d = readJSON(q.file);
  if (!d || !d.results || !d.results.T01_fault_id) continue;
  quants.push({
    quant: q.label,
    source: q.file,
    fault_id_macro: d.results.T01_fault_id.macro_recall,
    fault_id_raw: d.results.T01_fault_id.raw,
    n: d.results.T01_fault_id.n,
  });
}
if (quants.length < 2) {
  console.error("[measured] need at least f16 and Q4 to state the quantization lesson.");
  process.exit(1);
}

// ---- write -------------------------------------------------------------------
const doc = {
  _provenance: {
    exported: new Date().toISOString().slice(0, 10),
    hierarchy: hierarchy.replace(process.env.HOME || "~", "~"),
    suite: "IEB-Signals v1.2",
    sources: {
      frames: "out/margin-per-frame.json",
      escalation: `out/${fleetName}`,
      records: `out/${fleetName} (per_item)`,
      quants: quantSources.map((q) => `out/${q.file}`),
    },
    excluded: {
      Q8: "out/iebs12-pico-scratch-v2-q8.json is indistinguishable from the Q4 run " +
          "(1199/1200 identical predictions); not published until re-measured",
    },
    note: "Regenerate with: node scripts/export-wave-measured.mjs",
  },
  frames,
  escalation,
  records,
  quants,
};

mkdirSync(dirname(out), { recursive: true });
writeFileSync(out, JSON.stringify(doc, null, 1) + "\n");
console.log(`[measured] wrote ${out}`);
console.log(`[measured] frames=${Object.keys(frames).length} configs=${escalation.configs.length} ` +
            `records=${records.length} quants=${quants.map((q) => q.quant).join("/")}`);
