// Strip the text chunks (tEXt, zTXt, iTXt) from PNG files, losslessly.
//
// A plate rendered with ComfyUI carries its whole recipe in a zTXt chunk named "prompt"
// (model, prompts, seed, sampler, filename prefix); Stable Diffusion tools write a tEXt
// "parameters". Nothing of that belongs on a public site. This walks the chunk list and
// writes every chunk back except the three text types, so IHDR, PLTE, IDAT and the rest are
// byte-identical and the pixels never change (test/png-metadata.test.mjs checks dist; the
// pixel hash of every master was compared before and after the first run).
//
//   node scripts/strip-png-text.mjs [file.png ...]     default: every PNG under src/assets, plus
//                                                        the social cards at the root of src
//
// derive-webp.mjs runs this over the masters before it derives the WebP copies, so a new
// plate never ships its recipe even when the stripper is forgotten.
import { readFileSync, writeFileSync, readdirSync, statSync } from "node:fs";
import { join, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";

const WEB = join(dirname(fileURLToPath(import.meta.url)), "..");
const SIGNATURE = "89504e470d0a1a0a";
const TEXT = new Set(["tEXt", "zTXt", "iTXt"]);

export function stripPngText(buf) {
  if (buf.toString("hex", 0, 8) !== SIGNATURE) throw new Error("not a PNG");
  const kept = [buf.subarray(0, 8)], dropped = [];
  for (let i = 8; i < buf.length;) {
    const n = buf.readUInt32BE(i), type = buf.toString("latin1", i + 4, i + 8), end = i + 12 + n;
    if (TEXT.has(type)) dropped.push(`${type} ${buf.toString("latin1", i + 8, buf.indexOf(0, i + 8))}`);
    else kept.push(buf.subarray(i, end));
    i = end;
  }
  return { out: Buffer.concat(kept), dropped };
}

export function walkPngs(dir) {
  return readdirSync(dir).flatMap((n) => { const p = join(dir, n); return statSync(p).isDirectory() ? walkPngs(p) : /\.png$/i.test(p) ? [p] : []; });
}

export function stripFiles(files, log = () => {}) {
  let changed = 0;
  for (const f of files) {
    const { out, dropped } = stripPngText(readFileSync(f));
    if (!dropped.length) continue;
    writeFileSync(f, out); changed++;
    log(`${relative(WEB, f)}: dropped ${dropped.join(", ")}`);
  }
  return changed;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  // every PNG the site ships: the assets tree, and the social cards at the root of src
  const files = process.argv.length > 2 ? process.argv.slice(2)
    : [...walkPngs(join(WEB, "src", "assets")), ...readdirSync(join(WEB, "src")).filter((n) => /\.png$/i.test(n)).map((n) => join(WEB, "src", n))];
  const n = stripFiles(files, (m) => console.log(m));
  console.log(`${files.length} png(s), ${n} stripped`);
}
