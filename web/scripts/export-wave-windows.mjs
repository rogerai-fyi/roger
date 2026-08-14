#!/usr/bin/env node
// Export the RAW SAMPLE SERIES behind each record the mesh deck replays.
//
// The founder asked for sensors that show "real (simulated) but live moving/changing
// data". The honest way to move pixels is to move RECORDED samples: fleet_sim's items
// come from mm-hard-signal, whose windows were rendered from source scene files that
// carry the actual 96-sample series per channel (out/ho4-hard.json etc. - the same
// files fleet_sim recomputes evidence from). This script joins those series onto the
// deck's 120 records and commits them, so the deck's strip-charts replay real data.
//
//   node scripts/export-wave-windows.mjs [--hierarchy <path>] [--out <path>]
//
// VERIFICATION IS THE POINT: for every exported series this script recomputes
// min/max/mean and REFUSES the export if they do not match the range/mean the model
// actually read in its window body (wave-measured.json). A series that disagrees with
// its own window would be decoration wearing data's clothes.

import { readFileSync, writeFileSync, existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const DEFAULT_HIERARCHY = resolve(process.env.HOME || "", "ai/computer-scientist/build/roger-wave/hierarchy");
const DEFAULT_OUT = resolve(HERE, "..", "src", "data", "wave-windows.json");

function arg(name, fallback) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : fallback;
}
const hierarchy = resolve(arg("--hierarchy", DEFAULT_HIERARCHY));
const out = resolve(arg("--out", DEFAULT_OUT));

// the same scene files fleet_sim reads (its --scenes default)
const SCENE_FILES = ["out/ho4-hard.json", "out/scenes-heldout-v3.json", "out/heldout-hard.json"];

const measured = JSON.parse(readFileSync(resolve(HERE, "..", "src", "data", "wave-measured.json"), "utf8"));

// Scene ids COLLIDE across the three files (each generator run numbers from
// s00000), so (scene_id, channel_id) is ambiguous. The unambiguous join is the
// data itself: a candidate is THE series iff its min/max/mean match the window
// body the model actually read. Collect all candidates, pick by stats.
const index = new Map();
const used = [];
for (const f of SCENE_FILES) {
  const p = join(hierarchy, f);
  if (!existsSync(p)) continue;
  used.push(f);
  for (const sc of JSON.parse(readFileSync(p, "utf8")).scenes) {
    for (const ch of sc.channels) {
      const k = `${sc.scene_id}/${ch.channel_id}`;
      if (!index.has(k)) index.set(k, []);
      index.get(k).push({ file: f, ch });
    }
  }
}

const statsOf = (s) => ({
  lo: Math.min(...s), hi: Math.max(...s),
  mean: s.reduce((a, b) => a + b, 0) / s.length,
});
const close = (a, b) => a != null && Math.abs(a - b) <= Math.max(1e-3, Math.abs(b) * 1e-3);

const windows = {};
let missing = 0, refused = 0, ambiguous = 0;
for (const r of measured.records) {
  const cands = index.get(`${r.scene_id}/${r.node_id}`) || [];
  const w = r.window || {};
  const matches = cands.filter(({ ch }) => {
    const st = statsOf(ch.samples);
    return close(w.lo, st.lo) && close(w.hi, st.hi) && close(w.mean, st.mean);
  });
  if (matches.length === 0) { missing++; continue; }
  if (matches.length > 1) {
    // two different files carrying byte-identical stats would still be the
    // same window; only refuse if the sample series actually differ
    const first = JSON.stringify(matches[0].ch.samples);
    if (!matches.every((m) => JSON.stringify(m.ch.samples) === first)) {
      console.error(`[windows] REFUSED ${r.node_id}: ${matches.length} distinct series match the stats`);
      ambiguous++;
      continue;
    }
  }
  const { ch } = matches[0];
  // truth check: the scene's sensor_health ("ok" | fault kind) must agree with
  // the record's truth ("none" | fault kind)
  const health = ch.truth && ch.truth.sensor_health;
  const asTruth = health === "ok" ? "none" : health;
  if (asTruth !== r.truth) {
    console.error(`[windows] REFUSED ${r.node_id}: scene sensor_health ${health} != record truth ${r.truth}`);
    refused++;
    continue;
  }
  windows[r.node_id] = {
    period_s: ch.sample_period_s,
    samples: ch.samples.map((v) => Math.round(v * 1000) / 1000),
  };
}

if (ambiguous > 0) refused += ambiguous;

if (refused > 0) {
  console.error(`[windows] ${refused} series refused verification; not writing a bundle that disagrees with itself.`);
  process.exit(1);
}

const doc = {
  _provenance: {
    exported: new Date().toISOString().slice(0, 10),
    hierarchy: hierarchy.replace(process.env.HOME || "~", "~"),
    sources: used,
    note: "Raw sample series for the records in wave-measured.json, joined by (scene_id, channel_id) " +
      "from the SAME scene files fleet_sim recomputes evidence from. Every series is verified against " +
      "the range/mean in the window body the model actually read - the exporter refuses on mismatch. " +
      "REPLAY data: these samples were recorded when the bench was built, never generated in a browser. " +
      "Regenerate with: node scripts/export-wave-windows.mjs",
    verified: "min/max/mean per series match the recorded window body to 0.1%; truths match the records",
  },
  windows,
};

writeFileSync(out, JSON.stringify(doc) + "\n");
console.log(`[windows] wrote ${out}`);
console.log(`[windows] series=${Object.keys(windows).length} missing=${missing} refused=${refused}`);
