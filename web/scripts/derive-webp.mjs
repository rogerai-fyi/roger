#!/usr/bin/env node
// Derive the WebP copies the pages show from our own PNG masters.
//
// The article illustrations and charts are drawn as large PNGs (1-2.6 MB each). The PNG
// stays the master (and the social card, which scrapers want as PNG); the page shows a
// WebP at two widths through srcset, about a twentieth of the bytes. Re-run after editing
// or replacing a master:
//
//     node web/scripts/derive-webp.mjs            # every entry below
//     node web/scripts/derive-webp.mjs vram-hero  # just the ones whose name contains this
//
// Needs `cwebp` (libwebp) on PATH. Output is written next to the master:
//   <name>.webp       the wide copy, min(master width, 1600) px wide
//   <name>-800.webp   the phone / 1x copy, 800 px wide
// test/perf-guards.test.mjs checks every pair exists and keeps the master's shape.

import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
export const WEBP_DIR = path.join(WEB, "src", "assets", "broadcasts");
export const WIDE = 1600;
export const NARROW = 800;
const QUALITY = "82";

// every PNG master a page shows in an <img> (the social cards, *-og.png, stay PNG only)
export const MASTERS = [
  "agent-governance-hero", "alt-hero", "alt-tunein", "connect-hero", "gpu-wall-hero",
  "jev-wave-hero", "mtp-hero", "mtp-knowledge-vs-tools", "mtp-nextn-layer", "mtp-nmax-sweep",
  "mtp-quality-vs-speed", "mtp-spill-vs-speedup", "quantization", "router-airwaves-hero",
  "router-exact-failover", "router-three-stage", "share-hero", "share-reach", "tower-hero",
  "vram-hero",
];

// width x height from a PNG's IHDR chunk
export function pngSize(file) {
  const b = readFileSync(file);
  return { w: b.readUInt32BE(16), h: b.readUInt32BE(20) };
}

function main() {
  const only = process.argv[2];
  for (const name of MASTERS.filter((n) => !only || n.includes(only))) {
    const src = path.join(WEBP_DIR, `${name}.png`);
    const { w } = pngSize(src);
    for (const [out, width] of [[`${name}.webp`, Math.min(w, WIDE)], [`${name}-${NARROW}.webp`, NARROW]]) {
      execFileSync("cwebp", ["-quiet", "-q", QUALITY, "-m", "6", "-sharp_yuv", "-metadata", "none",
        "-resize", String(width), "0", src, "-o", path.join(WEBP_DIR, out)]);
      console.log(`${out}  ${width}w`);
    }
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
