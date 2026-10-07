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
//   <name>-1920.webp  only for a master wider than 1600: min(master width, 1920) px, the
//                     sharp step for a 960px article column at 2x (charts, diagrams)
// test/perf-guards.test.mjs checks every copy exists and keeps the master's shape.
//
// The App page's screenshots (APP_SHOTS) have no PNG master: the shipped WebP is the master,
// and each gets one smaller copy beside it, <name>-<n>.webp (appNarrow: 400w for a phone
// screenshot, 640w for a desktop one), for its srcset (test/qa-polish3.test.mjs).

import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
export const WEBP_DIR = path.join(WEB, "src", "assets", "broadcasts");
export const WIDE = 1600;
export const NARROW = 800;
export const SHARP = 1920;
const QUALITY = "82";

// every PNG master a page shows in an <img> (the social cards, *-og.png, stay PNG only)
export const MASTERS = [
  "agent-governance-hero", "alt-hero", "alt-tunein", "connect-hero", "gpu-wall-hero",
  "jev-wave-hero", "mtp-hero", "mtp-knowledge-vs-tools", "mtp-nextn-layer", "mtp-nmax-sweep",
  "mtp-quality-vs-speed", "mtp-spill-vs-speedup", "quantization", "router-airwaves-hero",
  "router-exact-failover", "router-three-stage", "share-hero", "share-reach", "tower-hero",
  "vram-hero",
];

export const APP_DIR = path.join(WEB, "src", "assets", "app");
export const APP_SHOTS = [
  "iphone-agent-action", "iphone-band", "iphone-connect", "iphone-operator-tools",
  "iphone-operator-voice", "iphone-tune-routing", "iphone-tunein-free", "iphone-tunein-private",
  "mac-band", "mac-confidential", "mac-connect", "mac-tunein",
  "tui-agent", "tui-band", "tui-help", "tui-share",
  "webui-account", "webui-browse", "webui-settings", "webui-share",
];
// a phone screenshot (under 1000 wide) shows at 130-340 CSS px, a desktop one at 330-960
export const appNarrow = (w) => (w < 1000 ? 400 : 640);

// width x height from a WebP's first chunk (VP8 lossy, VP8L lossless, VP8X extended)
export function webpSize(file) {
  const b = readFileSync(file), kind = b.toString("ascii", 12, 16);
  if (kind === "VP8 ") return { w: b.readUInt16LE(26) & 0x3fff, h: b.readUInt16LE(28) & 0x3fff };
  if (kind === "VP8L") { const v = b.readUInt32LE(21); return { w: (v & 0x3fff) + 1, h: ((v >> 14) & 0x3fff) + 1 }; }
  if (kind === "VP8X") return { w: b.readUIntLE(24, 3) + 1, h: b.readUIntLE(27, 3) + 1 };
  throw new Error(`${file}: not a WebP`);
}

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
    const steps = [[`${name}.webp`, Math.min(w, WIDE)], [`${name}-${NARROW}.webp`, NARROW]];
    if (w > WIDE) steps.push([`${name}-${SHARP}.webp`, Math.min(w, SHARP)]);
    for (const [out, width] of steps) {
      execFileSync("cwebp", ["-quiet", "-q", QUALITY, "-m", "6", "-sharp_yuv", "-metadata", "none",
        "-resize", String(width), "0", src, "-o", path.join(WEBP_DIR, out)]);
      console.log(`${out}  ${width}w`);
    }
  }
  for (const name of APP_SHOTS.filter((n) => !only || n.includes(only))) {
    const src = path.join(APP_DIR, `${name}.webp`);
    const n = appNarrow(webpSize(src).w);
    execFileSync("cwebp", ["-quiet", "-q", QUALITY, "-m", "6", "-sharp_yuv", "-metadata", "none",
      "-resize", String(n), "0", src, "-o", path.join(APP_DIR, `${name}-${n}.webp`)]);
    console.log(`${name}-${n}.webp  ${n}w`);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
