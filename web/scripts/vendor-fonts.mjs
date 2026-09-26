#!/usr/bin/env node
// Vendor the site's two typefaces (Space Grotesk, JetBrains Mono; both SIL OFL 1.1) into
// src/assets/fonts, so every page gets its fonts from this origin: no render-blocking
// third-party stylesheet and no extra connections before first paint.
//
//     node web/scripts/vendor-fonts.mjs
//
// Asks the public Google Fonts CSS API for the same families and weights the site used to
// link, saves each unicode-range subset's variable woff2 as <family>-<subset>.woff2, and
// prints the matching @font-face block for the top of src/styles/base.css. One file per
// subset serves every weight (they are variable fonts). The browser fetches a subset only
// when a page uses a character in its range, so the non-latin files cost nothing unless
// needed. The licence texts (OFL-*.txt) sit beside the files.

import { mkdirSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const OUT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..", "src", "assets", "fonts");
const CSS = "https://fonts.googleapis.com/css2?family=Space+Grotesk:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500;600&display=swap";
const UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36";
const LICENCES = {
  "OFL-SpaceGrotesk.txt": "https://raw.githubusercontent.com/google/fonts/main/ofl/spacegrotesk/OFL.txt",
  "OFL-JetBrainsMono.txt": "https://raw.githubusercontent.com/google/fonts/main/ofl/jetbrainsmono/OFL.txt",
};

const css = await (await fetch(CSS, { headers: { "user-agent": UA } })).text();
mkdirSync(OUT, { recursive: true });
const seen = new Map();   // file -> { family, range, weights: [] }
for (const m of css.matchAll(/\/\*\s*([\w-]+)\s*\*\/\s*@font-face\s*{([^}]*)}/g)) {
  const [, subset, body] = m;
  const family = body.match(/font-family:\s*'([^']+)'/)[1];
  const weight = Number(body.match(/font-weight:\s*(\d+)/)[1]);
  const url = body.match(/url\(([^)]+)\)/)[1];
  const range = body.match(/unicode-range:\s*([^;]+);/)[1].trim();
  const file = `${family.toLowerCase().replace(/\s+/g, "-")}-${subset}.woff2`;
  if (!seen.has(file)) {
    writeFileSync(path.join(OUT, file), Buffer.from(await (await fetch(url)).arrayBuffer()));
    seen.set(file, { family, range, weights: [] });
  }
  seen.get(file).weights.push(weight);
}
for (const [file, url] of Object.entries(LICENCES))
  writeFileSync(path.join(OUT, file), await (await fetch(url)).text());

for (const [file, { family, range, weights }] of seen) {
  console.log(`@font-face { font-family: "${family}"; font-style: normal; font-weight: ${Math.min(...weights)} ${Math.max(...weights)}; font-display: swap;
  src: url("../assets/fonts/${file}") format("woff2");
  unicode-range: ${range}; }`);
}
