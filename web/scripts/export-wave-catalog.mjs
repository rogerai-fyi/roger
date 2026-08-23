#!/usr/bin/env node
// Export the Wave Mesh device catalog from wavesim.py into a COMMITTED static snapshot.
//
// WHY A SNAPSHOT AND NOT A BUILD STEP. wavesim.py lives in a different repo
// (the wave-mesh research repo, outside this one) and needs a working Python
// environment. Shelling out to it during `npm run build` would couple this site's build -
// and CI - to a checkout that is not there. So the catalog is exported deliberately, by a
// human running this script, and the result is committed. The site build stays hermetic and
// the deck works with no network and no Python.
//
// The snapshot carries its own provenance, because a number on the deck has to be traceable
// to the thing that produced it. If _provenance.exported drifts far from today, that is a
// signal to re-run this - not a reason to hand-edit the JSON.
//
//   node scripts/export-wave-catalog.mjs [--hierarchy <path>] [--out <path>]
//
// Contract (wavesim.py's own DESIGN CONTRACT block): catalog() is PURE DATA - every device
// type, its channels, its root causes, its sensor faults, and the renderable modalities.

import { execFileSync } from "node:child_process";
import { writeFileSync, existsSync, mkdirSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
// No baked-in default: the research tree lives outside this repo, and where it sits is
// the operator's business. Pass --hierarchy <path> or set WAVE_HIERARCHY.
const DEFAULT_HIERARCHY = process.env.WAVE_HIERARCHY || "";
const DEFAULT_OUT = resolve(HERE, "..", "src", "data", "wave-catalog.json");

function arg(name, fallback) {
  const i = process.argv.indexOf(name);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : fallback;
}

const hierarchy = resolve(arg("--hierarchy", DEFAULT_HIERARCHY));
const out = resolve(arg("--out", DEFAULT_OUT));

if (!existsSync(join(hierarchy, "wavesim.py"))) {
  console.error(`[wave-catalog] no wavesim.py under ${hierarchy}`);
  console.error(`[wave-catalog] pass --hierarchy <path> if the models repo lives elsewhere.`);
  console.error(`[wave-catalog] the committed snapshot is unchanged; nothing was written.`);
  process.exit(1);
}

let raw;
try {
  raw = execFileSync("python3", ["wavesim.py", "catalog"], {
    cwd: hierarchy,
    encoding: "utf8",
    maxBuffer: 32 << 20,
  });
} catch (e) {
  console.error(`[wave-catalog] wavesim.py catalog failed: ${e.message}`);
  process.exit(1);
}

let catalog;
try {
  catalog = JSON.parse(raw);
} catch {
  console.error("[wave-catalog] wavesim.py did not emit JSON; refusing to write a broken snapshot.");
  process.exit(1);
}

// Sanity floors. A catalog that lost its live box or its modalities is a broken export, and
// overwriting a good snapshot with it would quietly strip the deck of its device browser.
const names = Object.keys(catalog);
if (names.length < 2) {
  console.error(`[wave-catalog] only ${names.length} asset type(s); refusing to overwrite.`);
  process.exit(1);
}
if (!catalog.roggentoo) {
  console.error("[wave-catalog] the live roggentoo entry is missing; refusing to overwrite.");
  process.exit(1);
}
// The live box must never carry sensor faults - its truth is null and the deck must never
// invent one. Catching it here means the honesty rail cannot be broken by a bad export.
if ((catalog.roggentoo.sensor_faults || []).length > 0) {
  console.error("[wave-catalog] roggentoo declares sensor faults; live data has no invented truth.");
  process.exit(1);
}

const channels = names.reduce((n, k) => n + Object.keys(catalog[k].channels || {}).length, 0);
const doc = {
  _provenance: {
    source: "wavesim.catalog()",
    // Provenance names WHICH tree, never where it sits on someone's disk: the
    // snapshot is committed to a public repo, and a home path is not data.
    hierarchy: "roger-wave/hierarchy",
    exported: new Date().toISOString().slice(0, 10),
    assets: names.length,
    channels,
    note: "Committed snapshot. Regenerate with: node scripts/export-wave-catalog.mjs",
  },
  catalog,
};

mkdirSync(dirname(out), { recursive: true });
writeFileSync(out, JSON.stringify(doc, null, 1) + "\n");
console.log(`[wave-catalog] wrote ${out}`);
console.log(`[wave-catalog] ${names.length} asset types, ${channels} channels`);

// ---- the recorded wire-format bundle ----------------------------------------
//
// The same channel rendered into all 8 dialects a device might actually speak. These are
// REAL renderer outputs, not mock-ups - produced by renderers.py through wavesim.render() -
// and they are exported rather than generated in the browser because the renderers are
// Python. The deck labels them RECORDED, exactly as the cassette deck already labels its
// replayed contracts: real output, captured, never presented as live.
//
// It is reproducible on purpose. make(spec) is byte-identical for a given seed, so anyone
// can re-run the spec below and get these bytes back. That is what makes a canned demo
// honest - it is a recording of something that happens, not an illustration of it.
const SCENE_SPEC = {
  asset_type: "centrifugal_pump",
  root_cause: "cavitation",
  severity: 0.7,
  seed: 42,
  faults: { vibration: { kind: "stuck", severity: 0.9, onset: 0.4 } },
};

const PY = `
import json, sys, wavesim
spec = json.loads(sys.argv[1])
scene = wavesim.make(spec)
ch = [c for c in scene["channels"] if c["truth"]["role"] == "vibration"][0]
renders = {m: wavesim.render(ch, m) for m in wavesim.MODALITIES}
# The stepping API over the same scene: what the transport bar will drive. Carry the
# feature the fault actually shows up in, so the UI can plot emergence over time.
import features
steps = []
for t, win in wavesim.windows(scene, width=32, stride=16):
    w = [c for c in win["channels"] if c["truth"]["role"] == "vibration"][0]
    f = features.summarize(w["samples"], w["sample_period_s"])
    steps.append({"t": t, "longest_run": f["longest_run"], "sd_tail": round(f["sd_tail"], 6)})
json.dump({"spec": spec, "scene_id": scene["scene_id"], "asset_type": scene["asset_type"],
           "root_cause": scene["root_cause"], "severity": scene["severity"],
           "channel": ch, "renders": renders, "steps": steps}, sys.stdout)
`;

let sceneRaw;
try {
  sceneRaw = execFileSync("python3", ["-c", PY, JSON.stringify(SCENE_SPEC)], {
    cwd: hierarchy,
    encoding: "utf8",
    maxBuffer: 32 << 20,
  });
} catch (e) {
  console.error(`[wave-scene] render export failed: ${e.message}`);
  console.error("[wave-scene] the catalog was written; the recorded bundle was not.");
  process.exit(1);
}

let scene;
try {
  scene = JSON.parse(sceneRaw);
} catch {
  console.error("[wave-scene] non-JSON from the renderer; refusing to write.");
  process.exit(1);
}

const modalities = Object.keys(scene.renders || {});
if (modalities.length < 8) {
  console.error(`[wave-scene] only ${modalities.length} dialect(s); refusing to overwrite.`);
  process.exit(1);
}
// The point of this bundle is that the SAME truth reads differently in each dialect. If two
// dialects ever came back byte-identical the demo would be quietly lying about that.
const seen = new Map();
for (const [m, text] of Object.entries(scene.renders)) {
  if (seen.has(text)) {
    console.error(`[wave-scene] ${m} and ${seen.get(text)} rendered identically; refusing.`);
    process.exit(1);
  }
  seen.set(text, m);
}

const sceneOut = resolve(dirname(out), "wave-scene-recorded.json");
writeFileSync(
  sceneOut,
  JSON.stringify(
    {
      _provenance: {
        source: "wavesim.make(spec) + wavesim.render(channel, modality)",
        // Provenance names WHICH tree, never where it sits on someone's disk: the
    // snapshot is committed to a public repo, and a home path is not data.
    hierarchy: "roger-wave/hierarchy",
        exported: new Date().toISOString().slice(0, 10),
        reproduce: "deterministic for this seed - re-run the spec to get these bytes back",
        label: "RECORDED - real renderer output, captured. Never present as live.",
      },
      ...scene,
    },
    null,
    1,
  ) + "\n",
);
console.log(`[wave-scene] wrote ${sceneOut}`);
console.log(`[wave-scene] ${modalities.length} dialects, ${scene.steps.length} transport steps`);
