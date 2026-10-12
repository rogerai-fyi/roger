// A rendered plate must not ship its recipe. ComfyUI writes the whole workflow (model name,
// prompts, seed, sampler, filename prefix) into a zTXt chunk named "prompt" (and "workflow");
// Stable Diffusion tools use tEXt "parameters". None of it belongs on a public site, so every
// PNG under dist/assets is checked chunk by chunk. scripts/strip-png-text.mjs removes the
// text chunks losslessly (IHDR, PLTE, IDAT and the rest pass through untouched) and
// scripts/derive-webp.mjs runs it over the masters before deriving.
import { test, before } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const WEB = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const DIST = path.join(WEB, "dist", "assets");

before(() => execFileSync("node", ["build.mjs"], { cwd: WEB }));

function walk(dir) {
  return readdirSync(dir).flatMap((n) => { const p = path.join(dir, n); return statSync(p).isDirectory() ? walk(p) : p.endsWith(".png") ? [p] : []; });
}
export function textChunks(file) {
  const b = readFileSync(file), out = [];
  assert.equal(b.toString("hex", 0, 8), "89504e470d0a1a0a", `${file}: a PNG signature`);
  for (let i = 8; i < b.length;) {
    const n = b.readUInt32BE(i), type = b.toString("latin1", i + 4, i + 8);
    if (type === "tEXt" || type === "zTXt" || type === "iTXt") out.push({ type, keyword: b.toString("latin1", i + 8, b.indexOf(0, i + 8)) });
    i += 12 + n;
  }
  return out;
}

test("no shipped PNG carries a generator recipe (prompt, workflow or parameters text chunk)", () => {
  const files = walk(DIST);
  assert.ok(files.length >= 20, `found ${files.length} PNGs under dist/assets`);
  const bad = [];
  for (const f of files) for (const c of textChunks(f)) if (/^(prompt|workflow|parameters)$/i.test(c.keyword)) bad.push(`${path.relative(DIST, f)}: ${c.type} ${c.keyword}`);
  assert.deepEqual(bad, []);
});

test("the masters are stripped before the WebP copies are derived", () => {
  const derive = readFileSync(path.join(WEB, "scripts", "derive-webp.mjs"), "utf8");
  assert.match(derive, /strip-png-text\.mjs/, "derive-webp.mjs runs the stripper");
  const strip = readFileSync(path.join(WEB, "scripts", "strip-png-text.mjs"), "utf8");
  assert.match(strip, /tEXt|zTXt|iTXt/, "the stripper names the text chunk types");
  assert.match(strip, /IDAT/, "and leaves the image data alone");
});
